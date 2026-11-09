package syncer

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owysync/internal/ignore"
	"owysync/internal/localfs"
	"owysync/internal/plan"
	"owysync/internal/tree"
)

// TestWritePart checks that the archive built for a plan unpacks back into the same entries,
// and that every directory comes before its contents (invariant 7).
func TestWritePart(t *testing.T) {
	dir := t.TempDir()
	mtime := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	files := map[string]string{
		"a.txt": "a", "a-b/x": "dash sorts before slash", "a/b/c.txt": "deep", "a/z": "z",
		"кириллица/файл.txt": "ru", "old/keep.txt": "replaced", "empty.txt": "",
	}
	for name, data := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(data), 0o644)
		os.Chtimes(full, mtime, mtime)
	}
	os.Mkdir(filepath.Join(dir, "emptydir"), 0o755)
	set, _ := ignore.Parse("")
	local, _, err := localfs.Walk(dir, set)
	if err != nil {
		t.Fatal(err)
	}
	// "old" exists on remote and must not be in the archive; old/keep.txt keeps its remote mode.
	remote := tree.Snapshot{
		"old":          {Path: "old", Type: tree.Dir, Mode: 0o700},
		"old/keep.txt": {Path: "old/keep.txt", Type: tree.File, Size: 1, Mode: 0o600},
	}
	p := plan.Diff(local, remote, nil, "", plan.Options{FileMode: 0o640, DirMode: 0o750})

	var ops []plan.Op
	for _, op := range p.Ops {
		if op.Kind == plan.Mkdir || op.Kind == plan.Upload {
			ops = append(ops, op)
		}
	}
	ops = archiveOrder(ops)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	n, part, err := writePart(tw, dir, ops, maps.Clone(remote))
	if err != nil || n != len(ops) || len(part.errs)+len(part.retry) > 0 {
		t.Fatalf("writePart: n=%d of %d, %+v, %v", n, len(ops), part, err)
	}
	tw.Close()

	seen := map[string]bool{}
	got := tree.Snapshot{}
	tr := tar.NewReader(&buf)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		e := tree.Entry{Path: h.Name, Type: tree.File, Size: h.Size, MTime: h.ModTime.Unix(), Mode: uint32(h.Mode)}
		if h.Typeflag == tar.TypeDir {
			e = tree.Entry{Path: h.Name[:len(h.Name)-1], Type: tree.Dir, Mode: uint32(h.Mode)}
		} else if data, _ := io.ReadAll(tr); string(data) != files[h.Name] {
			t.Errorf("%s: content %q", h.Name, data)
		}
		// bsdtar would turn non-ASCII names into '?' without this record (rawName).
		if ascii := !strings.ContainsFunc(e.Path, func(r rune) bool { return r > 127 }); (h.PAXRecords["hdrcharset"] == "BINARY") == ascii {
			t.Errorf("%s: hdrcharset record %q", e.Path, h.PAXRecords["hdrcharset"])
		}
		if d := path.Dir(e.Path); d != "." && !seen[d] && remote[d].Type != tree.Dir {
			t.Errorf("%s comes before its directory %s", e.Path, d)
		}
		seen[e.Path] = true
		got[e.Path] = e
	}
	want := tree.Snapshot{}
	for _, e := range part.entries {
		want[e.Path] = e
	}
	for p, l := range local {
		_, onRemote := remote[p]
		w, ok := want[p]
		switch {
		case l.Type == tree.Dir && onRemote:
			if _, in := got[p]; in {
				t.Errorf("existing directory %s is in the archive", p)
			}
			continue
		case !ok:
			t.Errorf("%s is not in the part", p)
			continue
		}
		mode := uint32(0o640)
		switch {
		case l.Type == tree.Dir:
			mode = 0o750
		case onRemote:
			mode = remote[p].Mode
		}
		if l.Type == tree.File {
			w.MTime = mtime.Unix()
			if w.Size != l.Size || w.MTime != l.MTime {
				t.Errorf("%s: part entry %+v, local %+v", p, w, l)
			}
		}
		if g := got[p]; g.Type != l.Type || g.Mode != mode || g.Type == tree.File && (g.Size != l.Size || g.MTime != l.MTime) {
			t.Errorf("%s: archive %+v, want type %s mode %o, local %+v", p, g, l.Type, mode, l)
		}
	}
	if len(got) != len(want) {
		t.Errorf("archive has %d entries, the part %d", len(got), len(want))
	}
}

// TestWritePartLimits checks the entry limit of a part, and that a directory written in an earlier
// part counts as present for the next one.
func TestWritePartLimits(t *testing.T) {
	var ops []plan.Op
	for i := range partMaxEntries + 1 {
		ops = append(ops, plan.Op{Kind: plan.Mkdir, Path: fmt.Sprintf("d/%05d", i), Mode: 0o755})
	}
	ops = append([]plan.Op{{Kind: plan.Mkdir, Path: "d", Mode: 0o755}}, ops...)
	view := tree.Snapshot{}
	n, part, err := writePart(tar.NewWriter(io.Discard), "", ops, view)
	if err != nil || n != partMaxEntries || len(part.entries) != partMaxEntries {
		t.Fatalf("first part: n=%d, %d entries, %v", n, len(part.entries), err)
	}
	n, part, err = writePart(tar.NewWriter(io.Discard), "", ops[n:], view)
	if err != nil || n != 2 || len(part.entries) != 2 || len(part.errs) != 0 {
		t.Fatalf("second part: n=%d, %+v, %v", n, part, err)
	}
	// Invariant 6: a parent that is a symlink on the remote stops the entry.
	view = tree.Snapshot{"l": {Path: "l", Type: tree.Symlink}}
	_, part, _ = writePart(tar.NewWriter(io.Discard), "", []plan.Op{{Kind: plan.Mkdir, Path: "l/x"}}, view)
	if len(part.errs) != 1 || len(part.entries) != 0 {
		t.Errorf("symlink parent: %+v", part)
	}
}

// TestWriteFileMissing checks that a file that cannot be read is left out and goes to SFTP.
func TestWriteFileMissing(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_, ok, err := writeFile(tw, t.TempDir(), plan.Op{Kind: plan.Upload, Path: "gone"})
	if ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	tw.Close()
	if h, err := tar.NewReader(&buf).Next(); err != io.EOF {
		t.Errorf("archive is not empty: %+v %v", h, err)
	}
}
