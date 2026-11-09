//go:build integration

package syncer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"owysync/internal/standtest"
)

// TestBulkStand covers bulk mode on the stand: criteria 5 (10k+ files), 19 (dir_mode of new
// directories), 20 (cut in the middle of an extraction) and the rescan after a failed bulk operation.
func TestBulkStand(t *testing.T) {
	for _, target := range standtest.Targets(t) {
		for _, user := range standtest.Users {
			t.Run(target.Name+"/"+user, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				dirs := 2 // dirs × 10 × 50 files
				if user == "bashuser" {
					dirs = 20
				}
				root := "/home/" + user + "/bulk"
				p := standtest.Setup(t, target, user, "/home/"+user+"/proj")
				setup := fmt.Sprintf(`r=%s; [ -d "$r" ] && chmod -R u+rwx "$r" </dev/null; rm -rf "$r" </dev/null
mkdir -p "$r/.owysync" </dev/null && touch "$r/.owysync/remote" </dev/null && echo setup-done
`, shq(root))
				if out, _, err := connect(t, p.Dir).Client.RunScript(ctx, setup); err != nil || !bytes.Contains(out, []byte("setup-done")) {
					t.Fatalf("setup: %v %s", err, out)
				}
				p.WriteConfig(t, target.Port, root)
				// 0775/0664 differ from what both the usual umask and an implicit mkdir (0777) give.
				cfg := filepath.Join(p.Dir, ".owysync", "config")
				data, _ := os.ReadFile(cfg)
				os.WriteFile(cfg, append(data, "dir_mode = 0775\nfile_mode = 0664\nbulk_threshold = 10\n"...), 0o644)
				env := connect(t, p.Dir)
				s, _ := env.Client.SFTP()
				sh := func(script string) {
					t.Helper()
					if out, _, err := env.Client.RunScript(ctx, "cd "+shq(root)+" </dev/null && "+script+" </dev/null && echo sh-done\n"); err != nil || !bytes.Contains(out, []byte("sh-done")) {
						t.Fatalf("%s: %v %s", script, err, out)
					}
				}

				files := map[string]string{}
				for d := range dirs {
					for e := range 10 {
						for f := range 50 {
							files[fmt.Sprintf("t/d%02d/e%02d/f%03d.txt", d, e, f)] = fmt.Sprintf("file %d %d %d", d, e, f)
						}
					}
				}
				writeLocal(t, p.Dir, files)

				// 1. Initial upload, cut while the first part is being extracted (criterion 20), then a rerun.
				names := slices.Sorted(func(yield func(string) bool) {
					for k := range files {
						if !yield(k) {
							return
						}
					}
				})
				mid := path.Join(root, names[len(names)/10])
				cctx, ccancel := context.WithCancel(ctx)
				go func() {
					for cctx.Err() == nil {
						if _, err := s.Lstat(mid); err == nil {
							ccancel()
							return
						}
						time.Sleep(5 * time.Millisecond)
					}
				}()
				cut, _, err := resync(t, cctx, p.Dir, Options{})
				ccancel()
				if !errors.Is(err, context.Canceled) || !strings.Contains(cut, "bulk upload:") {
					t.Errorf("interrupted run returned %v", err)
				}
				time.Sleep(2 * time.Second) // let an orphaned tar on the remote finish
				out, _, err := resync(t, ctx, p.Dir, Options{})
				if err != nil {
					t.Fatalf("rerun: %v\n%s", err, out)
				}
				assertMatch(t, env, p.Dir)
				for name, want := range map[string]os.FileMode{"t": 0o775, "t/d00/e00": 0o775, "t/d00/e00/f000.txt": 0o664} {
					if fi, err := s.Lstat(path.Join(root, name)); err != nil || fi.Mode().Perm() != want {
						t.Errorf("%s: mode %v (%v), want %v", name, fi.Mode().Perm(), err, want)
					}
				}

				// 2. A "git checkout": changed, deleted, new files and type changes, all in bulk (criterion 5).
				sh("chmod 0700 t/d00") // an existing directory keeps its mode: it is not in the archive
				later := e2eMTime.Add(time.Hour)
				for e := range 9 {
					for f := range 50 {
						full := filepath.Join(p.Dir, "t", "d00", fmt.Sprintf("e%02d", e), fmt.Sprintf("f%03d.txt", f))
						os.WriteFile(full, []byte("v2"), 0o644)
						os.Chtimes(full, later, later)
					}
				}
				os.RemoveAll(filepath.Join(p.Dir, "t", fmt.Sprintf("d%02d", dirs-1)))
				os.RemoveAll(filepath.Join(p.Dir, "t", "d00", "e09"))
				os.Remove(filepath.Join(p.Dir, "t", "d00", "e08", "f000.txt"))
				fresh := map[string]string{"t/d00/e09": "dir to file", "t/d00/e08/f000.txt/child": "file to dir"}
				for a := range 10 {
					for f := range 20 {
						fresh[fmt.Sprintf("n/a%02d/f%02d", a, f)] = "new"
					}
				}
				writeLocal(t, p.Dir, fresh)
				// More than the threshold of type changes: e09 had 50 files.
				out, _, err = resync(t, ctx, p.Dir, Options{})
				if err != nil {
					t.Fatalf("checkout: %v", err)
				}
				for _, phase := range []string{"bulk delete for type change:", "bulk upload:", "bulk delete:"} {
					if !strings.Contains(out, phase) {
						t.Errorf("no %q in the output", phase)
					}
				}
				assertMatch(t, env, p.Dir)
				for name, want := range map[string]os.FileMode{"t/d00": 0o700, "n/a00": 0o775, "t/d00/e08/f000.txt": 0o775, "n/a00/f00": 0o664} {
					if fi, err := s.Lstat(path.Join(root, name)); err != nil || fi.Mode().Perm() != want {
						t.Errorf("%s: mode %v (%v), want %v", name, fi.Mode().Perm(), err, want)
					}
				}

				// 3. A failed bulk operation leads to a full resync with a new remote scan.
				sh("chmod 0555 t/d00/e00")
				more := map[string]string{}
				for f := range 20 {
					more[fmt.Sprintf("t/d00/e00/g%02d", f)] = "more"
				}
				writeLocal(t, p.Dir, more)
				out, errOut, err := resync(t, ctx, p.Dir, Options{})
				if !errors.Is(err, ErrFailed) || strings.Count(out, "scanning the remote again") != 1 || !strings.Contains(errOut, "Permission denied") {
					t.Errorf("failed bulk: %v\n%s%s", err, out, errOut)
				}
				sh("chmod 0755 t/d00/e00")
				if _, _, err := resync(t, ctx, p.Dir, Options{}); err != nil {
					t.Fatalf("after the fix: %v", err)
				}
				assertMatch(t, env, p.Dir)

				// 4. Nothing left to do.
				out, _, err = resync(t, ctx, p.Dir, Options{})
				if err != nil || strings.Contains(out, "\n+ ") || strings.Contains(out, "\n~ ") || strings.Contains(out, "\n- ") || strings.Contains(out, "bulk") {
					t.Fatalf("last run: %v\n%s", err, out)
				}
				env.Client.RunScript(context.Background(), fmt.Sprintf(`chmod -R u+rwx %s </dev/null; rm -rf %s </dev/null`, shq(root), shq(root)))
			})
		}
	}
}
