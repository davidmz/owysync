package localfs

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"owysync/internal/ignore"
	"owysync/internal/tree"
)

// mkTree creates files (with content) and directories (names ending in '/') under dir.
func mkTree(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		p := filepath.Join(dir, filepath.FromSlash(n))
		if n[len(n)-1] == '/' {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func walkPaths(t *testing.T, root, masks string) (tree.Snapshot, []string, []string) {
	t.Helper()
	ign, err := ignore.Parse(masks)
	if err != nil {
		t.Fatal(err)
	}
	snap, skipped, err := Walk(root, ign)
	if err != nil {
		t.Fatal(err)
	}
	var warnings []string
	for _, s := range skipped {
		warnings = append(warnings, s.String())
	}
	var paths []string
	for p := range snap {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	return snap, paths, warnings
}

func TestWalk(t *testing.T) {
	root := t.TempDir()
	mkTree(t, root, "a.txt", "sub/файл", "sub/deep/x.log", "node_modules/m/i.js", "web/node_modules/y",
		".owysync/config", ".owysync/ignore", "empty/", "var/cache/c", "var/keep")
	snap, paths, warnings := walkPaths(t, root, "node_modules\n*.log\nvar/cache")
	want := []string{"a.txt", "empty", "sub", "sub/deep", "sub/файл", "var", "var/keep", "web"}
	if !reflect.DeepEqual(paths, want) || len(warnings) != 0 {
		t.Fatalf("paths = %q, warnings = %q; want %q", paths, warnings, want)
	}
	if e := snap["a.txt"]; e.Type != tree.File || e.Size != 5 || e.Path != "a.txt" {
		t.Errorf("a.txt = %+v", e)
	}
	if e := snap["sub"]; e.Type != tree.Dir {
		t.Errorf("sub = %+v", e)
	}
}

func TestWalkMTime(t *testing.T) {
	root := t.TempDir()
	mkTree(t, root, "old", "new", "frac")
	for n, tm := range map[string]time.Time{
		"old":  time.Date(1960, 1, 1, 0, 0, 0, 0, time.UTC),
		"new":  time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC),
		"frac": time.Unix(1_700_000_000, 999_999_900),
	} {
		if err := os.Chtimes(filepath.Join(root, n), tm, tm); err != nil {
			t.Fatal(err)
		}
	}
	snap, _, _ := walkPaths(t, root, "")
	if snap["old"].MTime != 0 || snap["new"].MTime != 1<<32-1 || snap["frac"].MTime != 1_700_000_000 {
		t.Errorf("mtimes: old %d, new %d, frac %d", snap["old"].MTime, snap["new"].MTime, snap["frac"].MTime)
	}
}

func TestWalkMissingRoot(t *testing.T) {
	if _, _, err := Walk(filepath.Join(t.TempDir(), "none"), &ignore.Set{}); err == nil {
		t.Fatal("expected an error")
	}
}
