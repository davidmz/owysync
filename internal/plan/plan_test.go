package plan

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"owysync/internal/tree"
)

var opts = Options{FileMode: 0o644, DirMode: 0o755}

func file(path string, size, mtime int64) tree.Entry {
	return tree.Entry{Path: path, Type: tree.File, Size: size, MTime: mtime, Mode: 0o600}
}

func dir(path string) tree.Entry { return tree.Entry{Path: path, Type: tree.Dir, Mode: 0o700} }

func typed(path string, t tree.Type) tree.Entry { return tree.Entry{Path: path, Type: t} }

func snap(es ...tree.Entry) tree.Snapshot {
	s := tree.Snapshot{}
	for _, e := range es {
		s[e.Path] = e
	}
	return s
}

// ops renders operations as "kind path" or "kind path mode" for upload and mkdir.
func ops(p Plan) []string {
	var out []string
	for _, o := range p.Ops {
		s := o.Kind.String() + " " + o.Path
		if o.Kind == Upload || o.Kind == Mkdir {
			s += fmt.Sprintf(" %o", o.Mode)
		}
		out = append(out, s)
	}
	return out
}

func TestDiff(t *testing.T) {
	tests := []struct {
		name          string
		local, remote tree.Snapshot
		unknown       []string
		scope         string
		window        int64
		ops           []string
		warnings      int
		errors        int
	}{
		{name: "equal trees", local: snap(dir("d"), file("d/f", 1, 10)), remote: snap(dir("d"), file("d/f", 1, 10))},
		{name: "new file and dir", local: snap(dir("d"), dir("d/e"), file("d/e/f", 1, 10)),
			ops: []string{"mkdir d 755", "mkdir d/e 755", "upload d/e/f 644"}},
		{name: "size changed keeps remote mode", local: snap(file("f", 2, 10)), remote: snap(file("f", 1, 10)),
			ops: []string{"upload f 600"}},
		{name: "mtime changed", local: snap(file("f", 1, 11)), remote: snap(file("f", 1, 10)),
			ops: []string{"upload f 600"}},
		{name: "mtime within window", local: snap(file("f", 1, 12)), remote: snap(file("f", 1, 10)), window: 2},
		{name: "mtime outside window", local: snap(file("f", 1, 13)), remote: snap(file("f", 1, 10)), window: 2,
			ops: []string{"upload f 600"}},
		{name: "mtimes below uint32 clamp equal", local: snap(file("f", 1, -100)), remote: snap(file("f", 1, 0))},
		{name: "mtimes above uint32 clamp equal", local: snap(file("f", 1, 1<<40)), remote: snap(file("f", 1, 1<<32-1))},
		{name: "dir mtime ignored", local: snap(tree.Entry{Path: "d", Type: tree.Dir, MTime: 5}), remote: snap(tree.Entry{Path: "d", Type: tree.Dir, MTime: 9})},
		{name: "file to dir", local: snap(dir("x"), file("x/f", 1, 1)), remote: snap(file("x", 1, 1)),
			ops: []string{"delete-for-type-change x", "mkdir x 755", "upload x/f 644"}},
		{name: "dir to file deletes contents bottom-up first", local: snap(file("x", 1, 1)),
			remote: snap(dir("x"), dir("x/a"), file("x/a/f", 1, 1), file("x/g", 1, 1)),
			ops: []string{"delete-for-type-change x/g", "delete-for-type-change x/a/f", "delete-for-type-change x/a", "delete-for-type-change x", "upload x 644"}},
		{name: "symlink to file", local: snap(file("l", 1, 1)), remote: snap(typed("l", tree.Symlink)),
			ops: []string{"delete-for-type-change l", "upload l 644"}},
		{name: "remote-only file, dir and symlink deleted bottom-up", local: snap(),
			remote: snap(dir("d"), file("d/f", 1, 1), typed("d/l", tree.Symlink), file("g", 1, 1)),
			ops: []string{"delete g", "delete d/l", "delete d/f", "delete d"}},
		{name: "remote-only other warns and pins its parent", local: snap(),
			remote: snap(dir("run"), typed("run/php.sock", tree.Other), file("run/pid", 1, 1)),
			ops: []string{"delete run/pid"}, warnings: 1},
		{name: "other against local object is an error", local: snap(file("s", 1, 1)), remote: snap(typed("s", tree.Other)), errors: 1},
		{name: "dir with other cannot become a file", local: snap(file("run", 1, 1)),
			remote: snap(dir("run"), typed("run/s", tree.Other)), warnings: 1, errors: 1},
		{name: "unknown subtree untouched", local: snap(dir("u"), file("u/new", 1, 1), file("x", 1, 1)),
			remote: snap(dir("u"), file("u/old", 1, 1)), unknown: []string{"u"},
			ops: []string{"upload x 644"}},
		{name: "unknown dir missing locally is kept with its ancestors", local: snap(),
			remote: snap(dir("a"), dir("a/u"), file("a/f", 1, 1)), unknown: []string{"a/u"},
			ops: []string{"delete a/f"}},
		{name: ".owysync never touched", local: snap(dir(".owysync"), file(".owysync/config", 1, 1)),
			remote: snap(dir(".owysync"), dir(".owysync/tmp"), file(".owysync/remote", 0, 0))},
		{name: "scope limits ops to the subtree", local: snap(dir("a"), file("a/f", 1, 1), file("b", 1, 1), file("ab", 1, 1)),
			remote: snap(dir("a"), file("a/g", 1, 1), file("c", 1, 1)), scope: "a",
			ops: []string{"upload a/f 644", "delete a/g"}},
		{name: "scope on a file", local: snap(file("a/f", 2, 1), file("a/g", 2, 1)),
			remote: snap(file("a/f", 1, 1), file("a/g", 1, 1)), scope: "a/f",
			ops: []string{"upload a/f 600"}},
		{name: "case-sensitive rename", local: snap(file("foo", 1, 1)), remote: snap(file("Foo", 1, 1)),
			ops: []string{"upload foo 644", "delete Foo"}},
		{name: "full order", local: snap(file("t", 1, 1), dir("n"), file("n/f", 1, 1)),
			remote: snap(dir("t"), file("t/x", 1, 1), file("old", 1, 1)),
			ops: []string{"delete-for-type-change t/x", "delete-for-type-change t", "mkdir n 755", "upload n/f 644", "upload t 644", "delete old"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := opts
			o.MTimeWindow = tt.window
			p := Diff(tt.local, tt.remote, tt.unknown, tt.scope, o)
			if got := ops(p); !slices.Equal(got, tt.ops) {
				t.Errorf("ops:\n got %q\nwant %q", got, tt.ops)
			}
			if len(p.Warnings) != tt.warnings || len(p.Errors) != tt.errors {
				t.Errorf("warnings %q, errors %q; want %d and %d", p.Warnings, p.Errors, tt.warnings, tt.errors)
			}
			if !slices.Equal(p.Unknown, tt.unknown) {
				t.Errorf("unknown %q, want %q", p.Unknown, tt.unknown)
			}
		})
	}
}

func TestUploadCarriesLocalEntry(t *testing.T) {
	l := file("f", 7, 42)
	p := Diff(snap(l), snap(), nil, "", opts)
	if len(p.Ops) != 1 || p.Ops[0].Local != l {
		t.Fatalf("ops %+v, want upload of %+v", p.Ops, l)
	}
}

// TestInvariantDeleteOnlySeen checks invariant 1 on random snapshots: deletes target only
// remote paths that are not other, not in .owysync/ and not in or above an unknown subtree.
func TestInvariantDeleteOnlySeen(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	names := []string{"a", "b", ".owysync", "A"}
	types := []tree.Type{tree.File, tree.Dir, tree.Symlink, tree.Other}
	randTree := func(withOther bool) tree.Snapshot {
		s := tree.Snapshot{}
		var add func(prefix string, depth int)
		add = func(prefix string, depth int) {
			for _, n := range names {
				if rng.IntN(3) == 0 {
					continue
				}
				path := strings.TrimPrefix(prefix+"/"+n, "/")
				tp := types[rng.IntN(len(types))]
				if !withOther && (tp == tree.Symlink || tp == tree.Other) {
					tp = tree.File
				}
				if depth == 2 && tp == tree.Dir {
					tp = tree.File
				}
				s[path] = tree.Entry{Path: path, Type: tp, Size: rng.Int64N(2), MTime: rng.Int64N(2)}
				if tp == tree.Dir {
					add(path, depth+1)
				}
			}
		}
		add("", 0)
		return s
	}
	for i := range 2000 {
		local, remote := randTree(false), randTree(true)
		var unknown []string
		for p, e := range remote {
			if e.Type == tree.Dir && rng.IntN(4) == 0 {
				unknown = append(unknown, p)
			}
		}
		scope := ""
		if i%3 == 0 {
			scope = "a"
		}
		p := Diff(local, remote, unknown, scope, opts)
		for _, o := range p.Ops {
			if o.Kind != Delete && o.Kind != DeleteForTypeChange {
				continue
			}
			r, ok := remote[o.Path]
			switch {
			case !ok:
				t.Fatalf("delete of unseen path %q", o.Path)
			case r.Type == tree.Other:
				t.Fatalf("delete of other %q", o.Path)
			case under(o.Path, ".owysync"):
				t.Fatalf("delete in .owysync: %q", o.Path)
			case !under(o.Path, scope):
				t.Fatalf("delete %q outside scope %q", o.Path, scope)
			}
			for _, u := range unknown {
				if under(o.Path, u) || under(u, o.Path) {
					t.Fatalf("delete %q touches unknown subtree %q", o.Path, u)
				}
			}
			for q, e := range remote {
				if e.Type == tree.Other && under(q, o.Path) {
					t.Fatalf("delete %q would remove other %q", o.Path, q)
				}
			}
		}
	}
}
