//go:build integration

package syncer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owysync/internal/localfs"
	"owysync/internal/standtest"
	"owysync/internal/tree"
)

// e2eSetup builds the remote side: stale objects, a fifo, a symlink to a directory outside
// the root where local has a directory, a directory with ignored content, a type change each way.
const e2eSetup = `r=%s; o=%s
[ -d "$r" ] && chmod -R u+rwx "$r" </dev/null; rm -rf "$r" "$o" </dev/null
mkdir -p "$r/.owysync" "$o" "$r/gone" "$r/typechg/sub" "$r/node_modules" </dev/null
touch "$r/.owysync/remote" "$o/keep" "$r/stale.txt" "$r/gone/keep.log" "$r/gone/x" "$r/Case.txt" "$r/typechg/sub/c" "$r/f2d" "$r/node_modules/keep" </dev/null
printf x > "$r/$(printf 'nl\nname')"
mkfifo "$r/fifo" </dev/null
ln -s "$o" "$r/linkdir" </dev/null
echo setup-done
`

var e2eMTime = time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)

func writeLocal(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, data := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(full, e2eMTime, e2eMTime); err != nil {
			t.Fatal(err)
		}
	}
}

// resync runs check and resync on a fresh connection, like `owysync sync --once -v`.
func resync(t *testing.T, ctx context.Context, dir string, o Options) (out, errOut string, err error) {
	t.Helper()
	var ob, eb bytes.Buffer
	env, err := Check(ctx, dir, io.Discard)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	defer env.Close()
	o.Verbose = true
	err = Resync(ctx, env, dir, o, &ob, &eb)
	t.Logf("resync:\n%s%s", ob.String(), eb.String())
	return ob.String(), eb.String(), err
}

// connect returns a checked environment for verification.
func connect(t *testing.T, dir string) *Env {
	t.Helper()
	env, err := Check(context.Background(), dir, io.Discard)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	t.Cleanup(func() { env.Close() })
	return env
}

// assertMatch compares the non-ignored remote tree with local by type, size, mtime and content.
// Directories in kept may stay on remote only: they hold ignored content.
func assertMatch(t *testing.T, env *Env, dir string, kept ...string) {
	t.Helper()
	for _, d := range treeDiff(t, env, dir, true, kept...) {
		t.Error(d)
	}
}

// treeDiff lists the differences between the non-ignored remote tree and local: type, size,
// mtime and, if content, the data of every file.
func treeDiff(t *testing.T, env *Env, dir string, content bool, kept ...string) []string {
	t.Helper()
	var diffs []string
	add := func(format string, a ...any) { diffs = append(diffs, fmt.Sprintf(format, a...)) }
	local, _, err := localfs.Walk(dir, env.Ignore)
	if err != nil {
		t.Fatal(err)
	}
	res, err := env.Client.Scan(context.Background(), env.Root, env.Stat, env.Ignore.PruneArgs(env.Root))
	if err != nil {
		t.Fatal(err)
	}
	s, _ := env.Client.SFTP()
	remote := tree.Snapshot{}
	for _, e := range res.Entries {
		if !env.Ignore.Excluded(e.Path) && e.Type != tree.Other {
			remote[e.Path] = e
		}
	}
	for p, l := range local {
		r, ok := remote[p]
		switch {
		case !ok:
			add("%q missing on remote", p)
			continue
		case r.Type != l.Type:
			add("%q: remote %s, local %s", p, r.Type, l.Type)
			continue
		case l.Type == tree.File && (r.Size != l.Size || r.MTime != l.MTime):
			add("%q: remote size %d mtime %d, local %d %d", p, r.Size, r.MTime, l.Size, l.MTime)
		}
		if content && l.Type == tree.File {
			want, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
			f, err := s.Open(path.Join(env.Root, p))
			if err != nil {
				add("%q: %v", p, err)
				continue
			}
			got, _ := io.ReadAll(f)
			f.Close()
			if !bytes.Equal(got, want) {
				add("%q: content differs", p)
			}
		}
		delete(remote, p)
	}
	for _, k := range kept {
		delete(remote, k)
	}
	for p := range remote {
		add("%q is on remote only", p)
	}
	return diffs
}

func TestResyncStand(t *testing.T) {
	for _, target := range standtest.Targets(t) {
		for _, user := range standtest.Users {
			t.Run(target.Name+"/"+user, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				root, outside := "/home/"+user+"/e2e", "/home/"+user+"/e2e-outside"
				// Build the remote tree over a connection to the stand's ready root, then switch to it.
				p := standtest.Setup(t, target, user, "/home/"+user+"/proj")
				setup := fmt.Sprintf(e2eSetup, shq(root), shq(outside))
				if out, _, err := connect(t, p.Dir).Client.RunScript(ctx, setup); err != nil || !bytes.Contains(out, []byte("setup-done")) {
					t.Fatalf("setup: %v %s", err, out)
				}
				p.WriteConfig(t, target.Port, root)
				if err := os.WriteFile(filepath.Join(p.Dir, ".owysync", "ignore"), []byte("node_modules\n*.log\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				env := connect(t, p.Dir)
				writeLocal(t, p.Dir, map[string]string{
					"a b.txt": "spaces", "it's.txt": "quote", "кириллица/файл.txt": "ru", "case.txt": "lower",
					"deep/x/y/z.txt": "deep", "linkdir/in.txt": "in", "typechg": "now a file",
					"f2d/child.txt": "child", "empty/": "", "node_modules/local.js": "ignored", "x.log": "ignored",
				})

				// 1. First run: everything converges; criteria 7, 9, 10, 11, 12.
				out, errOut, err := resync(t, ctx, p.Dir, Options{})
				if err != nil {
					t.Fatalf("first run: %v", err)
				}
				if !strings.Contains(errOut, "warning: fifo") {
					t.Errorf("no warning about the fifo:\n%s", errOut)
				}
				if !strings.Contains(out, "kept directory gone") || strings.Count(out, "kept directory") != 1 {
					t.Errorf("expected one message about gone:\n%s", out)
				}
				assertMatch(t, env, p.Dir, "gone")
				s, _ := env.Client.SFTP()
				for _, keep := range []string{outside + "/keep", root + "/fifo", root + "/gone/keep.log", root + "/node_modules/keep"} {
					if _, err := s.Lstat(keep); err != nil {
						t.Errorf("%s must survive: %v", keep, err)
					}
				}
				for name, want := range map[string]os.FileMode{"a b.txt": 0o644, "deep/x": 0o755} {
					if fi, err := s.Lstat(path.Join(root, name)); err != nil || fi.Mode().Perm() != want {
						t.Errorf("%s: mode %v (%v), want %v", name, fi.Mode().Perm(), err, want)
					}
				}

				// 2. Second run writes nothing (criterion 2); only the rmdir of gone is retried.
				out, _, err = resync(t, ctx, p.Dir, Options{})
				if err != nil || strings.Contains(out, "\n+ ") || strings.Contains(out, "\n~ ") || strings.Contains(out, "\n- ") {
					t.Fatalf("second run: %v\n%s", err, out)
				}

				// 3. Dry run reports a change but does not apply it.
				writeLocal(t, p.Dir, map[string]string{"a b.txt": "changed"})
				out, _, err = resync(t, ctx, p.Dir, Options{DryRun: true})
				if err != nil || !strings.Contains(out, "~ a b.txt\n") || !strings.Contains(out, "dry run: 2 operations, 7 bytes") {
					t.Fatalf("dry run: %v\n%s", err, out)
				}
				if fi, err := s.Lstat(root + "/a b.txt"); err != nil || fi.Size() != int64(len("spaces")) {
					t.Fatalf("dry run changed the remote: %v %v", fi, err)
				}

				// 4. Ctrl+C in the middle of uploads, then a rerun converges (bashuser only: slow).
				if user == "bashuser" {
					big := map[string]string{}
					for i := range 300 {
						big[fmt.Sprintf("bulk/%03d.bin", i)] = strings.Repeat("z", 64<<10)
					}
					writeLocal(t, p.Dir, big)
					cctx, ccancel := context.WithTimeout(ctx, 300*time.Millisecond)
					_, _, err = resync(t, cctx, p.Dir, Options{})
					ccancel()
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Errorf("interrupted run returned %v; make the tree bigger if it finished in time", err)
					}
				}
				if _, _, err := resync(t, ctx, p.Dir, Options{}); err != nil {
					t.Fatalf("final run: %v", err)
				}
				assertMatch(t, connect(t, p.Dir), p.Dir, "gone")

				env.Client.RunScript(context.Background(), fmt.Sprintf(`chmod -R u+rwx %s </dev/null; rm -rf %s %s </dev/null`, shq(root), shq(root), shq(outside)))
			})
		}
	}
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
