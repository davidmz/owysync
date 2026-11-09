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

// Walk scans the local tree under root. Excluded directories are not entered; .owysync/ is
// always skipped. Symlinks, junctions and other special objects, and names with unpaired UTF-16
// surrogates are skipped with a warning and count as ignored. Any read error fails the walk:
// a missing part of local would turn into deletions on the remote.
func Walk(root string, ign *ignore.Set) (snap tree.Snapshot, warnings []string, err error) {
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
			warnings = append(warnings, fmt.Sprintf("skipped %q: the name has an unpaired UTF-16 surrogate and cannot be converted to UTF-8; rename it", rel))
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
			warnings = append(warnings, fmt.Sprintf("skipped %s: symlinks, junctions and special files are not synced; add the path to .owysync/ignore to silence this", rel))
			return skip() // a junction may look like a directory: never enter it
		}
		snap[rel] = e
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return snap, warnings, nil
}
