package syncer

import (
	"errors"
	"testing"

	"owysync/internal/plan"
	"owysync/internal/tree"
)

// TestCheckParents covers invariant 6: a write needs every parent to be a directory in the snapshot.
func TestCheckParents(t *testing.T) {
	snap := tree.Snapshot{
		"d":      {Path: "d", Type: tree.Dir},
		"d/e":    {Path: "d/e", Type: tree.Dir},
		"link":   {Path: "link", Type: tree.Symlink},
		"file":   {Path: "file", Type: tree.File},
		"d/link": {Path: "d/link", Type: tree.Symlink},
	}
	for _, tc := range []struct {
		path string
		ok   bool
	}{
		{"top", true},
		{"d/x", true},
		{"d/e/x", true},
		{"link/x", false},
		{"d/link/x", false},
		{"file/x", false},
		{"missing/x", false},
		{"d/missing/x", false},
	} {
		err := checkParents(snap, tc.path)
		if (err == nil) != tc.ok || err != nil && !errors.Is(err, errStale) {
			t.Errorf("%s: err = %v, want ok=%v", tc.path, err, tc.ok)
		}
	}
}

func TestOpLine(t *testing.T) {
	rsnap := tree.Snapshot{"f": {Type: tree.File}, "l": {Type: tree.Symlink}}
	for _, tc := range []struct {
		op   plan.Op
		want string
	}{
		{plan.Op{Kind: plan.Upload, Path: "f"}, "~ f"},
		{plan.Op{Kind: plan.Upload, Path: "l"}, "+ l"},
		{plan.Op{Kind: plan.Upload, Path: "n"}, "+ n"},
		{plan.Op{Kind: plan.Mkdir, Path: "d"}, "+ d"},
		{plan.Op{Kind: plan.Delete, Path: "f"}, "- f"},
		{plan.Op{Kind: plan.DeleteForTypeChange, Path: "l"}, "- l"},
		{plan.Op{Kind: plan.Delete, Path: "nl\nname"}, `- "nl\nname"`},
	} {
		if got := opLine(tc.op, rsnap); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.op, got, tc.want)
		}
	}
}
