//go:build integration

package syncer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"owysync/internal/standtest"
)

// lockedBuffer is a bytes.Buffer safe for the live loop and the watcher goroutine.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// waitFor polls cond until it holds or timeout passes and returns how long it took.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for !cond() {
		if time.Since(start) > timeout {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return time.Since(start)
}

// TestLiveStand covers live mode (spec §7): criterion 4 (one saved file reaches the remote within
// 1 s), a directory moved in and one deleted, a warning about a changed ignore file, criterion 18
// (a remote directory deleted by someone else triggers a resync) and a git checkout of 10k+ files.
func TestLiveStand(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	for _, target := range standtest.Targets(t) {
		user := "bashuser"
		t.Run(target.Name+"/"+user, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			root := "/home/" + user + "/live"
			p := standtest.Setup(t, target, user, "/home/"+user+"/proj")
			setup := fmt.Sprintf(`r=%s; rm -rf "$r" </dev/null
mkdir -p "$r/.owysync" </dev/null && touch "$r/.owysync/remote" </dev/null && echo setup-done
`, shq(root))
			if out, _, err := connect(t, p.Dir).Client.RunScript(ctx, setup); err != nil || !bytes.Contains(out, []byte("setup-done")) {
				t.Fatalf("setup: %v %s", err, out)
			}
			p.WriteConfig(t, target.Port, root)
			if err := os.WriteFile(filepath.Join(p.Dir, ".owysync", "ignore"), []byte(".git\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			writeLocal(t, p.Dir, map[string]string{"start.txt": "start", "sub/a.txt": "a", "gone/x.txt": "x"})

			env := connect(t, p.Dir)
			s, _ := env.Client.SFTP()
			liveEnv, err := Check(ctx, p.Dir, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			defer liveEnv.Close()
			var out, errOut lockedBuffer
			lctx, lcancel := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { done <- Live(lctx, liveEnv, p.Dir, Options{}, &out, &errOut) }()
			defer func() {
				lcancel()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Errorf("Live returned %v", err)
				}
				t.Logf("live output:\n%s\n%s", out.String(), errOut.String())
			}()
			waitFor(t, "the first resync", time.Minute, func() bool { return strings.Contains(out.String(), "resync: done") })

			remoteIs := func(rel, data string) func() bool {
				return func() bool {
					f, err := s.Open(path.Join(root, rel))
					if err != nil {
						return false
					}
					defer f.Close()
					var b bytes.Buffer
					b.ReadFrom(f)
					return b.String() == data
				}
			}
			remoteGone := func(rel string) func() bool {
				return func() bool { _, err := s.Lstat(path.Join(root, rel)); return errors.Is(err, os.ErrNotExist) }
			}

			// Criterion 4: one saved file.
			os.WriteFile(filepath.Join(p.Dir, "start.txt"), []byte("saved"), 0o644)
			if took := waitFor(t, "the saved file", 5*time.Second, remoteIs("start.txt", "saved")); took > time.Second {
				t.Errorf("the saved file took %s to reach the remote, want < 1s", took)
			}

			// A directory moved in from outside (only the directory itself is reported) and one deleted.
			outside := t.TempDir()
			writeLocal(t, outside, map[string]string{"moved/deep/m.txt": "moved"})
			if err := os.Rename(filepath.Join(outside, "moved"), filepath.Join(p.Dir, "moved")); err != nil {
				t.Fatal(err)
			}
			os.RemoveAll(filepath.Join(p.Dir, "gone"))
			waitFor(t, "the moved directory", 5*time.Second, remoteIs("moved/deep/m.txt", "moved"))
			waitFor(t, "the deleted directory", 5*time.Second, remoteGone("gone"))

			// A changed service file is reported, not applied.
			os.WriteFile(filepath.Join(p.Dir, ".owysync", "ignore"), []byte(".git\n*.tmp\n"), 0o644)
			waitFor(t, "the ignore warning", 5*time.Second, func() bool {
				return strings.Contains(errOut.String(), "warning: .owysync/ignore changed")
			})

			// Criterion 18: someone deletes sub on the remote; the next save there resyncs.
			if _, _, err := env.Client.RunScript(ctx, "rm -rf "+shq(root+"/sub")+" </dev/null\n"); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(p.Dir, "sub", "a.txt"), []byte("after"), 0o644)
			waitFor(t, "sub/a.txt after the resync", 30*time.Second, remoteIs("sub/a.txt", "after"))
			if !strings.Contains(out.String(), "starting a full resync") {
				t.Error("no background resync")
			}
			os.WriteFile(filepath.Join(p.Dir, "sub", "b.txt"), []byte("b"), 0o644)
			waitFor(t, "sub/b.txt", 5*time.Second, remoteIs("sub/b.txt", "b"))

			// git checkout of 10k+ files: thousands of changes at once go in bulk.
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-c", "core.autocrlf=false", "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
				cmd.Dir = p.Dir
				if b, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, b)
				}
			}
			repo := t.TempDir() // build the branches outside, then clone into the synced directory
			files := map[string]string{}
			for i := range 10500 {
				files[fmt.Sprintf("src/d%03d/f%04d.txt", i%100, i)] = fmt.Sprintf("v1 %d", i)
			}
			writeLocal(t, repo, files)
			gitIn := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
				cmd.Dir = repo
				if b, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, b)
				}
			}
			gitIn("init", "-q", "-b", "one")
			gitIn("add", ".")
			gitIn("commit", "-q", "-m", "one")
			gitIn("checkout", "-q", "-b", "two")
			for i := range 10500 {
				f := filepath.Join(repo, "src", fmt.Sprintf("d%03d", i%100), fmt.Sprintf("f%04d.txt", i))
				switch i % 3 {
				case 0:
					os.Remove(f)
				case 1:
					os.WriteFile(f, []byte(fmt.Sprintf("v2 %d", i)), 0o644)
				}
			}
			writeLocal(t, repo, map[string]string{"new/n.txt": "new"})
			gitIn("add", "-A")
			gitIn("commit", "-q", "-m", "two")
			gitIn("checkout", "-q", "one")

			os.MkdirAll(filepath.Join(p.Dir, "repo"), 0o755)
			git("-C", "repo", "init", "-q")
			git("-C", "repo", "pull", "-q", repo, "one")
			git("-C", "repo", "fetch", "-q", repo, "two:two")
			converge := func(what string) {
				t.Helper()
				waitFor(t, what, 3*time.Minute, func() bool {
					time.Sleep(time.Second)
					return len(treeDiff(t, env, p.Dir, false)) == 0
				})
			}
			converge("the clone")
			git("-C", "repo", "checkout", "-q", "two")
			converge("the checkout")
			assertMatch(t, env, p.Dir)
		})
	}
}
