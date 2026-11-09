// Package localfs reads the local tree (spec §5, 8.5).
package localfs

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"unicode/utf8"

	"owysync/internal/ignore"
	"owysync/internal/tree"
)

// Skipped is a local path that Walk left out and that must count as ignored (spec §2, 5):
// the remote object at this path, if any, must not be deleted.
type Skipped struct {
	Path   string
	Reason string
}

func (s Skipped) String() string { return fmt.Sprintf("skipped %q: %s", s.Path, s.Reason) }

// Walk scans the local tree under root. Excluded directories are not entered; .owysync/ is
// always skipped. Symlinks, junctions and other special objects, and names with unpaired UTF-16
// surrogates are reported in skipped and never entered. Any read error fails the walk:
// a missing part of local would turn into deletions on the remote.
func Walk(root string, ign *ignore.Set) (snap tree.Snapshot, skipped []Skipped, err error) {
	snap = tree.Snapshot{}
	err = filepath.WalkDir(root, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("reading %s: %w", full, err)
		}
		if full == root {
			return nil
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		skip := func() error {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Go keeps unpaired surrogates as WTF-8, which is not valid UTF-8.
		if !utf8.ValidString(rel) {
			skipped = append(skipped, Skipped{rel, "the name has an unpaired UTF-16 surrogate and cannot be converted to UTF-8; rename it"})
			return skip()
		}
		if rel == ".owysync" || ign.ExcludedName(rel) {
			return skip()
		}
		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil // deleted during the walk
		}
		if err != nil {
			return fmt.Errorf("reading %s: %w", full, err)
		}
		e := tree.Entry{Path: rel, MTime: tree.ClampMTime(info.ModTime().Unix())}
		switch m := info.Mode(); {
		case m.IsDir():
			e.Type = tree.Dir
		case m.IsRegular():
			e.Type, e.Size = tree.File, info.Size()
		default:
			skipped = append(skipped, Skipped{rel, "symlinks, junctions and special files are not synced; add the path to .owysync/ignore to silence this"})
			return skip() // a junction may look like a directory: never enter it
		}
		snap[rel] = e
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return snap, skipped, nil
}
