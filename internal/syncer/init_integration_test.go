//go:build integration

package syncer

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"owysync/internal/standtest"
)

// remoteDir makes a scratch directory in the user's home on the stand (removed at the end)
// and returns its path and the SFTP client of an existing initialized project (for setup and checks).
func remoteDir(t *testing.T, p *standtest.Project, user, name string) (string, *sftp.Client) {
	t.Helper()
	_, env, err := runCheck(t, p) // /home/<user>/proj has the marker
	if err != nil {
		t.Fatal(err)
	}
	s, err := env.Client.SFTP()
	if err != nil {
		t.Fatal(err)
	}
	dir := fmt.Sprintf("/home/%s/init-%s-%d", user, name, time.Now().UnixNano())
	t.Cleanup(func() { s.RemoveAll(dir) })
	return dir, s
}

// TestInitRemoteStates covers every row of the table in spec §4.1, step 5, on the stand.
func TestInitRemoteStates(t *testing.T) {
	const user = "bashuser"
	for _, target := range standtest.Targets(t) {
		setup := func(t *testing.T, name string) (*standtest.Project, string, *sftp.Client) {
			p := standtest.Setup(t, target, user, "/home/"+user+"/proj")
			dir, s := remoteDir(t, p, user, name)
			p.WriteConfig(t, target.Port, dir)
			return p, dir, s
		}
		// hasMarker: the directory is initialized: .owysync/tmp and an empty .owysync/remote.
		hasMarker := func(t *testing.T, s *sftp.Client, dir string) bool {
			t.Helper()
			fi, err := s.Stat(path.Join(dir, ".owysync", "remote"))
			if errors.Is(err, os.ErrNotExist) {
				return false
			}
			if err != nil {
				t.Fatal(err)
			}
			if tmp, err := s.Stat(path.Join(dir, ".owysync", "tmp")); err != nil || !tmp.IsDir() || fi.Size() != 0 {
				t.Fatalf("incomplete init: tmp=%v err=%v size=%d", tmp, err, fi.Size())
			}
			return true
		}

		t.Run(target.Name+"/missing, create", func(t *testing.T) {
			p, dir, s := setup(t, "new")
			dir += "/a/b" // intermediate directories too
			os.WriteFile(p.Dir+"/.owysync/config", []byte(fmt.Sprintf("host = %s\nport = %d\nuser = %s\nidentity_file = %s\nremote_root = %s\ndir_mode = 0750\n", target.Host, target.Port, user, standtest.KeyPath(), dir)), 0o644)
			out, err := runInit(t, p.Dir, true, "y\n")
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if !hasMarker(t, s, dir) {
				t.Fatal("no marker")
			}
			for _, d := range []string{dir, path.Dir(dir)} {
				if fi, _ := s.Stat(d); fi == nil || fi.Mode().Perm() != 0o750 {
					t.Fatalf("%s: mode %v, want 0750 (dir_mode)", d, fi)
				}
			}
			// A following check passes entirely.
			if out, _, err := runCheck(t, p); err != nil || strings.Count(out, ": ok") != 8 {
				t.Fatalf("%v\n%s", err, out)
			}
		})

		t.Run(target.Name+"/missing, default answer is no", func(t *testing.T) {
			p, dir, s := setup(t, "new")
			out, err := runInit(t, p.Dir, true, "\n")
			if !errors.Is(err, ErrFailed) || !strings.Contains(out, "Nothing was changed") {
				t.Fatalf("%v\n%s", err, out)
			}
			if _, err := s.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the directory must not be created: %v", err)
			}
		})

		t.Run(target.Name+"/no marker, initialize", func(t *testing.T) {
			p, dir, s := setup(t, "nomarker")
			for i := 0; i < 12; i++ {
				if err := s.MkdirAll(fmt.Sprintf("%s/item%02d", dir, i)); err != nil {
					t.Fatal(err)
				}
			}
			out, err := runInit(t, p.Dir, true, "y\n")
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			// The listing: the number of objects, the first 10 names, then an ellipsis.
			if !strings.Contains(out, "12 objects") || !strings.Contains(out, "item09, ...") || strings.Contains(out, "item10") || !strings.Contains(out, "will delete") {
				t.Fatalf("unexpected listing:\n%s", out)
			}
			if !hasMarker(t, s, dir) {
				t.Fatal("no marker")
			}
			if _, err := s.Stat(dir + "/item00"); err != nil {
				t.Fatal("existing content must stay:", err)
			}
		})

		t.Run(target.Name+"/no marker, default answer is no", func(t *testing.T) {
			p, dir, s := setup(t, "nomarker")
			if err := s.MkdirAll(dir); err != nil {
				t.Fatal(err)
			}
			out, err := runInit(t, p.Dir, true, "\n")
			if !errors.Is(err, ErrFailed) || hasMarker(t, s, dir) {
				t.Fatalf("%v\n%s", err, out)
			}
		})

		t.Run(target.Name+"/marker exists, connect writes nothing", func(t *testing.T) {
			p, dir, s := setup(t, "marked")
			if _, err := runInit(t, p.Dir, true, "y\n"); err != nil { // creates the directory with the marker
				t.Fatal(err)
			}
			before, _ := s.Stat(dir + "/.owysync/remote")
			out, err := runInit(t, p.Dir, true, "y\n")
			if err != nil || !strings.Contains(out, "overwrite each other") || strings.Contains(out, "Directory") {
				t.Fatalf("%v\n%s", err, out)
			}
			if after, _ := s.Stat(dir + "/.owysync/remote"); after == nil || !after.ModTime().Equal(before.ModTime()) {
				t.Fatal("the marker was rewritten")
			}
			if _, err := runInit(t, p.Dir, true, "\n"); !errors.Is(err, ErrFailed) { // the default answer is no
				t.Fatalf("expected ErrFailed, got %v", err)
			}
		})

		t.Run(target.Name+"/not a directory", func(t *testing.T) {
			p, dir, s := setup(t, "file")
			f, err := s.Create(dir)
			if err != nil {
				t.Fatal(err)
			}
			f.Close()
			out, err := runInit(t, p.Dir, true, "y\n")
			if err == nil || !strings.Contains(err.Error(), "not a directory") {
				t.Fatalf("%v\n%s", err, out)
			}
		})
	}
}

// TestInitFlow is the path of spec §4.1, step 4: init saves the config and fails at authentication
// (the stand key is not in ssh-agent); after the user adds identity_file, init --remote finishes the job.
func TestInitFlow(t *testing.T) {
	const user = "bashuser"
	target := standtest.Targets(t)[0]
	p := standtest.Setup(t, target, user, "/home/"+user+"/proj")
	dir, s := remoteDir(t, p, user, "flow")
	os.RemoveAll(p.Dir + "/.owysync") // Setup wrote a config; start from an empty directory

	out, err := runInit(t, p.Dir, false, fmt.Sprintf("%s:%d\n%s\n%s\n", target.Host, target.Port, user, dir))
	if !errors.Is(err, ErrFailed) || !strings.Contains(out, "4. authentication: FAIL") || !strings.Contains(out, "init --remote") {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := runInit(t, p.Dir, false, ""); err == nil || !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("a repeated init must fail: %v", err)
	}

	f, err := os.OpenFile(p.Dir+"/.owysync/config", os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(f, "identity_file = %s\n", standtest.KeyPath())
	f.Close()

	if out, err = runInit(t, p.Dir, true, "y\n"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "review") || !strings.Contains(out, "--dry-run") {
		t.Fatalf("no summary:\n%s", out)
	}
	if _, err := s.Stat(dir + "/.owysync/remote"); err != nil {
		t.Fatal(err)
	}
}
