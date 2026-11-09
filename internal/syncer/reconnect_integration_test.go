//go:build integration

package syncer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owysync/internal/standtest"
)

// standContainer is the docker compose container of a stand target (test/stand/compose.yaml).
func standContainer(target standtest.Target) string { return "owysync-stand-" + target.Name + "-1" }

func docker(t *testing.T, args ...string) {
	t.Helper()
	if b, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, b)
	}
}

// reconnectSetup creates an empty marked remote root and a local project for it.
func reconnectSetup(t *testing.T, ctx context.Context, target standtest.Target, user, name, extraConfig string) (*standtest.Project, string) {
	t.Helper()
	root := "/home/" + user + "/" + name
	p := standtest.Setup(t, target, user, "/home/"+user+"/proj")
	setup := fmt.Sprintf(`r=%s; rm -rf "$r" </dev/null
mkdir -p "$r/.owysync" </dev/null && touch "$r/.owysync/remote" </dev/null && echo setup-done
`, shq(root))
	if out, _, err := connect(t, p.Dir).Client.RunScript(ctx, setup); err != nil || !bytes.Contains(out, []byte("setup-done")) {
		t.Fatalf("setup: %v %s", err, out)
	}
	p.WriteConfig(t, target.Port, root)
	cfg := filepath.Join(p.Dir, ".owysync", "config")
	data, _ := os.ReadFile(cfg)
	os.WriteFile(cfg, append(data, extraConfig...), 0o644)
	return p, root
}

// TestReconnectDuringResync covers criterion 6: the connection breaks in the middle of a
// --once resync; owysync reconnects, resyncs again and the trees match.
func TestReconnectDuringResync(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found")
	}
	for _, target := range standtest.Targets(t) {
		user := "bashuser"
		t.Run(target.Name+"/"+user, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			p, _ := reconnectSetup(t, ctx, target, user, "reconnect", "bulk_threshold = 0\n")
			files := map[string]string{}
			for i := range 1500 {
				files[fmt.Sprintf("d%02d/f%04d.txt", i%30, i)] = strings.Repeat("x", i)
			}
			writeLocal(t, p.Dir, files)

			env, err := Check(ctx, p.Dir, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			defer env.Close()
			var out, errOut lockedBuffer
			done := make(chan error, 1)
			go func() { done <- Resync(ctx, env, p.Dir, Options{Verbose: true}, &out, &errOut) }()
			waitFor(t, "the first uploads", time.Minute, func() bool { return strings.Count(out.String(), "\n+ ") > 50 })
			// Kill the user's sshd session processes: the connection breaks like a network failure.
			docker(t, "exec", standContainer(target), "pkill", "-KILL", "-u", user)
			err = <-done
			t.Logf("output:\n%s\n%s", out.String(), errOut.String())
			if err != nil {
				t.Fatalf("Resync: %v", err)
			}
			if !strings.Contains(out.String(), "reconnected") || strings.Count(out.String(), "resync: started") != 2 {
				t.Error("expected a reconnect and a second resync")
			}
			if n := strings.Count(errOut.String(), "error:"); n > 70 {
				t.Errorf("%d error lines: operations went on after the connection was lost", n)
			}
			assertMatch(t, connect(t, p.Dir), p.Dir)
		})
	}
}

// TestReconnectAfterSleep covers criterion 17: the stand is paused longer than the keepalive
// window, like a sleeping laptop; live mode notices the dead connection, reconnects once the
// stand is back and resyncs, and later saves reach the remote. Debian only: it takes minutes.
func TestReconnectAfterSleep(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found")
	}
	target := standtest.Targets(t)[0]
	user := "bashuser"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	p, root := reconnectSetup(t, ctx, target, user, "sleep", "")
	writeLocal(t, p.Dir, map[string]string{"a.txt": "a"})

	env, err := Check(ctx, p.Dir, &bytes.Buffer{})
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
		t.Logf("live output:\n%s\n%s", out.String(), errOut.String())
	}()
	waitFor(t, "the first resync", time.Minute, func() bool { return strings.Contains(out.String(), "resync: done") })

	c := standContainer(target)
	docker(t, "pause", c)
	paused := true
	defer func() {
		if paused {
			exec.Command("docker", "unpause", c).Run()
		}
	}()
	// A save while asleep: the upload hangs until the keepalives give up.
	writeLocal(t, p.Dir, map[string]string{"while-asleep.txt": "zzz"})
	waitFor(t, "the dead connection", 2*time.Minute, func() bool {
		return strings.Contains(errOut.String(), "no reply to 3 keepalives")
	})
	docker(t, "unpause", c)
	paused = false
	waitFor(t, "the reconnect and resync", 2*time.Minute, func() bool {
		o := out.String()
		i := strings.Index(o, "reconnected")
		return i >= 0 && strings.Contains(o[i:], "resync: done")
	})

	writeLocal(t, p.Dir, map[string]string{"after.txt": "awake"})
	check := connect(t, p.Dir)
	s, _ := check.Client.SFTP()
	waitFor(t, "a save after the wake-up", 10*time.Second, func() bool {
		_, err := s.Lstat(root + "/after.txt")
		return err == nil
	})
	assertMatch(t, check, p.Dir)
}
