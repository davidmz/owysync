//go:build integration

package syncer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"owysync/internal/standtest"
)

// Benchmark tree: benchDirs × benchSubdirs × benchFiles files, sizes log-normal around ~3 KB.
const (
	benchDirs    = 50
	benchSubdirs = 40
	benchFiles   = 50
)

func benchPath(d, e, f int) string { return fmt.Sprintf("src/d%02d/e%02d/f%03d.txt", d, e, f) }

// netem sets a one-way delay on the stand's outgoing traffic; RTT grows by the same amount.
// The large queue keeps netem from capping the bandwidth.
func netem(t *testing.T, target standtest.Target, delay time.Duration) {
	t.Helper()
	c := standContainer(target)
	if delay == 0 {
		exec.Command("docker", "exec", c, "tc", "qdisc", "del", "dev", "eth0", "root").Run()
		return
	}
	docker(t, "exec", c, "tc", "qdisc", "replace", "dev", "eth0", "root", "netem", "delay", delay.String(), "limit", "100000")
}

// TestBench measures criteria 3, 4, 5 and the initial upload (spec §12) on a 100k-file tree
// with RTT +50 ms, and the SFTP/bulk crossover. Run it on purpose:
//
//	OWYSYNC_BENCH=1 go test -tags integration -run TestBench -v -timeout 2h ./internal/syncer/
func TestBench(t *testing.T) {
	if os.Getenv("OWYSYNC_BENCH") == "" {
		t.Skip("set OWYSYNC_BENCH=1 to run the benchmark")
	}
	const delay = 50 * time.Millisecond
	target := standtest.Targets(t)[0]
	user := "bashuser"
	ctx := context.Background()
	p, root := reconnectSetup(t, ctx, target, user, "bench", "")
	defer netem(t, target, 0)
	os.WriteFile(filepath.Join(p.Dir, ".owysync", "ignore"), []byte(".git\n"), 0o644)
	var results []string
	report := func(format string, a ...any) {
		line := fmt.Sprintf(format, a...)
		results = append(results, line)
		t.Log(line)
	}
	defer func() { t.Logf("results:\n%s", strings.Join(results, "\n")) }()

	// The tree, committed to git, plus a second branch for criterion 5.
	start := time.Now()
	rng := rand.New(rand.NewPCG(1, 2))
	buf := make([]byte, 1<<20)
	for i := range buf {
		buf[i] = byte('a' + rng.IntN(26))
	}
	size := func() int { return min(int(math.Exp(rng.NormFloat64()*1.2+7.3)), len(buf)) }
	var total int64
	for d := range benchDirs {
		for e := range benchSubdirs {
			dir := filepath.Join(p.Dir, filepath.FromSlash(benchPath(d, e, 0)), "..")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for f := range benchFiles {
				n := size()
				total += int64(n)
				if err := os.WriteFile(filepath.Join(p.Dir, filepath.FromSlash(benchPath(d, e, f))), buf[:n], 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	files := benchDirs * benchSubdirs * benchFiles
	report("tree: %d files, %d MB, created in %s", files, total>>20, time.Since(start).Round(time.Second))
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "core.autocrlf=false", "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		cmd.Dir = p.Dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
	}
	start = time.Now()
	git("init", "-q", "-b", "one")
	git("add", "-A")
	git("commit", "-q", "-m", "one")
	git("checkout", "-q", "-b", "two")
	os.MkdirAll(filepath.Join(p.Dir, "new"), 0o755)
	changed := 0
	for i := range 6000 { // 3000 modified, 1500 deleted, 1500 new
		d, e, f := i%benchDirs, i/benchDirs%benchSubdirs, i/(benchDirs*benchSubdirs)
		full := filepath.Join(p.Dir, filepath.FromSlash(benchPath(d, e, f)))
		switch {
		case i < 3000:
			os.WriteFile(full, buf[:size()], 0o644)
		case i < 4500:
			os.Remove(full)
		default:
			os.WriteFile(filepath.Join(p.Dir, "new", fmt.Sprintf("n%04d.txt", i)), buf[:size()], 0o644)
		}
		changed++
	}
	git("add", "-A")
	git("commit", "-q", "-m", "two")
	git("checkout", "-q", "one")
	report("git branches: %d changed files, prepared in %s", changed, time.Since(start).Round(time.Second))

	// The first open of a fresh file costs ~5 ms here (antivirus); read everything once so that
	// both initial uploads read warm files.
	start = time.Now()
	filepath.WalkDir(filepath.Join(p.Dir, "src"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			os.ReadFile(path)
		}
		return nil
	})
	report("warm-up read of all files: %s", time.Since(start).Round(time.Second))

	once := func(name string) time.Duration {
		t.Helper()
		var out bytes.Buffer
		start := time.Now()
		env, err := Check(ctx, p.Dir, &out)
		if err != nil {
			t.Fatalf("%s: check: %v\n%s", name, err, out.String())
		}
		defer env.Close()
		err = Resync(ctx, env, p.Dir, Options{}, &out, &out)
		took := time.Since(start)
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out.String())
		}
		report("%s: %s (%s)", name, took.Round(10*time.Millisecond), strings.TrimSpace(lastLine(out.String())))
		return took
	}
	wipe := func() {
		t.Helper()
		script := fmt.Sprintf(`r=%s; rm -rf "$r" </dev/null; mkdir -p "$r/.owysync" </dev/null && touch "$r/.owysync/remote" </dev/null && echo wiped`+"\n", shq(root))
		if out, _, err := connect(t, p.Dir).Client.RunScript(ctx, script); err != nil || !bytes.Contains(out, []byte("wiped")) {
			t.Fatalf("wipe: %v %s", err, out)
		}
	}
	rtt := func() {
		t.Helper()
		s, _ := connect(t, p.Dir).Client.SFTP()
		var ds []time.Duration
		for range 10 {
			start := time.Now()
			s.Lstat(root)
			ds = append(ds, time.Since(start))
		}
		slices.Sort(ds)
		report("RTT (SFTP lstat, median of 10): %s", ds[5].Round(time.Millisecond))
	}

	// Initial upload without and with delay: bandwidth-bound means close times (criterion 5).
	rtt()
	fast := once("initial upload, no delay")
	wipe()
	netem(t, target, delay)
	rtt()
	slow := once("initial upload, +50 ms")
	report("initial upload: %.0f MB/s with delay, %.2fx the time without", float64(total)/(1<<20)/slow.Seconds(), slow.Seconds()/fast.Seconds())

	// Criterion 3: a cold resync (new process: check + resync) without changes.
	for i := range 3 {
		once(fmt.Sprintf("cold resync without changes #%d", i+1))
	}

	// SFTP vs bulk for n changed files (bulk_threshold crossover).
	for _, n := range []int{20, 100, 500} {
		for _, bulk := range []bool{false, true} {
			for i := range n {
				full := filepath.Join(p.Dir, filepath.FromSlash(benchPath(benchDirs-1, i/benchFiles, i%benchFiles)))
				os.WriteFile(full, buf[:size()], 0o644)
				mt := time.Unix(1_700_000_000+int64(len(results))*1000+int64(i), 0) // differs on every round
				os.Chtimes(full, mt, mt)
			}
			mode, thr := "sftp", "100000"
			if bulk {
				mode, thr = "bulk", "1"
			}
			setConfig(t, p.Dir, "bulk_threshold", thr)
			once(fmt.Sprintf("%d changed files via %s", n, mode))
		}
	}
	setConfig(t, p.Dir, "bulk_threshold", "")
	git("reset", "-q", "--hard") // the checkout below needs a clean work tree

	// Live: criterion 4 (one saved file) and criterion 5 (git checkout).
	env, err := Check(ctx, p.Dir, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut lockedBuffer
	lctx, lcancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- Live(lctx, env, p.Dir, Options{}, &out, &errOut) }()
	defer func() {
		lcancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("Live returned %v", err)
		}
		env.Close()
		if t.Failed() {
			t.Logf("live output:\n%s\n%s", out.String(), errOut.String())
		}
	}()
	waitFor(t, "the live resync", 10*time.Minute, func() bool { return strings.Contains(out.String(), "resync: done") })
	time.Sleep(time.Second)

	var saves []time.Duration
	for i := range 10 {
		rel := benchPath(benchDirs-1, benchSubdirs-1, 10+i) // branch two does not touch it
		full := filepath.Join(p.Dir, filepath.FromSlash(rel))
		os.WriteFile(full, fmt.Appendf(nil, "save %d", i), 0o644)
		saves = append(saves, waitFor(t, "a saved file", 10*time.Second, func() bool { return strings.Contains(out.String(), "~ "+rel+"\n") }))
		time.Sleep(300 * time.Millisecond)
	}
	slices.Sort(saves)
	report("live save of one file (criterion 4): median %s, max %s", saves[len(saves)/2].Round(time.Millisecond), saves[len(saves)-1].Round(time.Millisecond))

	start = time.Now()
	git("checkout", "-q", "two")
	checkout := time.Since(start)
	// A bulk part prints its lines only once extracted, so quiet output proves nothing: poll the
	// remote (a scan takes ~1.5 s here, which is the resolution of this number).
	verify := connect(t, p.Dir)
	converged := checkout + waitFor(t, "the remote after the checkout", 20*time.Minute, func() bool {
		return len(treeDiff(t, verify, p.Dir, false)) == 0
	})
	report("git checkout of %d changes (criterion 5): checkout took %s, remote matched %s after its start (%d bulk phases)",
		changed, checkout.Round(100*time.Millisecond), converged.Round(time.Second), strings.Count(out.String(), "bulk "))
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	return s[strings.LastIndexByte(s, '\n')+1:]
}

// setConfig sets key in .owysync/config; an empty value removes it.
func setConfig(t *testing.T, dir, key, value string) {
	t.Helper()
	file := filepath.Join(dir, ".owysync", "config")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if !strings.HasPrefix(l, key+" ") {
			lines = append(lines, l)
		}
	}
	if value != "" {
		lines = append(lines, key+" = "+value)
	}
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
