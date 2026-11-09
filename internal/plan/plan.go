// Package plan turns a local and a remote snapshot into an ordered list of remote
// operations (spec §6.4–6.5). It is a pure function shared by resync and live.
package plan

import (
	"fmt"
	"slices"
	"strings"

	"owysync/internal/tree"
)

// OpKind is the kind of a remote operation. The values follow the execution order.
type OpKind uint8

// Operation kinds, in execution order (spec §6.5). Each delete removes exactly one
// object: a directory is preceded by deletes of its contents.
const (
	DeleteForTypeChange OpKind = iota + 1 // remove an object whose type differs from local
	Mkdir                                 // create a directory with Op.Mode
	Upload                                // upload a local file with Op.Mode
	Delete                                // remove a remote-only object
)

func (k OpKind) String() string {
	switch k {
	case DeleteForTypeChange:
		return "delete-for-type-change"
	case Mkdir:
		return "mkdir"
	case Upload:
		return "upload"
	case Delete:
		return "delete"
	}
	return "?"
}

// Op is one remote operation.
type Op struct {
	Kind  OpKind
	Path  string
	Local tree.Entry // for Mkdir and Upload: what must end up on remote
	Mode  uint32     // for Upload: mode of the replaced file or file_mode; for Mkdir: dir_mode
}

// Plan is the result of Diff.
type Plan struct {
	Ops      []Op     // in execution order (spec §6.5)
	Warnings []string // remote objects of type other that are left in place
	Errors   []string // path conflicts to resolve by hand; they make resync unsuccessful
	Unknown  []string // non-ignored unreadable remote directories (spec §8.4.1)
}

// Options are the config values Diff depends on.
type Options struct {
	FileMode    uint32
	DirMode     uint32
	MTimeWindow int64 // seconds
}

// Diff compares local and remote and returns the operations that make remote match
// local. Both snapshots must already be free of ignored paths. unknown lists unreadable
// remote directories: nothing at, under or above them is created or deleted. scope
// limits the operations to a path and its subtree; "" means the whole tree.
func Diff(local, remote tree.Snapshot, unknown []string, scope string, opts Options) Plan {
	p := Plan{Unknown: slices.Sorted(slices.Values(unknown))}

	// Remote objects that must survive (other and unknown subtrees) pin their ancestors:
	// a directory holding them cannot be removed.
	pinned := map[string]bool{}
	pin := func(path string) {
		for d := parent(path); d != "" && !pinned[d]; d = parent(d) {
			pinned[d] = true
		}
	}
	for _, u := range unknown {
		pin(u)
	}
	for path, r := range remote {
		if r.Type == tree.Other {
			pin(path)
		}
	}

	skip := func(path string) bool {
		return !under(path, scope) || under(path, ".owysync") || slices.ContainsFunc(unknown, func(u string) bool { return under(path, u) })
	}

	// Remote directories replaced by a non-directory: their contents go with them.
	replaced := map[string]bool{}
	var typeDel, mkdirs, uploads, dels []Op
	create := func(l tree.Entry, mode uint32) {
		if l.Type == tree.Dir {
			mkdirs = append(mkdirs, Op{Kind: Mkdir, Path: l.Path, Local: l, Mode: opts.DirMode})
		} else {
			uploads = append(uploads, Op{Kind: Upload, Path: l.Path, Local: l, Mode: mode})
		}
	}

	for path, l := range local {
		if skip(path) {
			continue
		}
		r, ok := remote[path]
		switch {
		case !ok:
			create(l, opts.FileMode)
		case r.Type == tree.Other:
			p.Errors = append(p.Errors, fmt.Sprintf("%s: remote has a %s here (socket, fifo or device) and it is never deleted; remove it on remote by hand or add the path to .owysync/ignore", path, r.Type))
		case r.Type != l.Type:
			if pinned[path] {
				p.Errors = append(p.Errors, fmt.Sprintf("%s: cannot replace the remote directory with a %s: it contains objects that are never deleted (special files or unreadable directories); clean it up on remote by hand", path, l.Type))
				continue
			}
			typeDel = append(typeDel, Op{Kind: DeleteForTypeChange, Path: path})
			if r.Type == tree.Dir {
				replaced[path] = true
			}
			create(l, opts.FileMode)
		case l.Type == tree.File && (l.Size != r.Size || !sameMTime(l.MTime, r.MTime, opts.MTimeWindow)):
			create(l, r.Mode)
		}
	}

	for path, r := range remote {
		if _, ok := local[path]; ok || skip(path) {
			continue
		}
		switch {
		case r.Type == tree.Other:
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s: remote %s (socket, fifo or device) is not in local and is never deleted; add the path to .owysync/ignore to silence this", path, r.Type))
		case pinned[path]:
			// Holds an other or an unknown subtree; reported there.
		case insideReplaced(path, replaced):
			typeDel = append(typeDel, Op{Kind: DeleteForTypeChange, Path: path})
		default:
			dels = append(dels, Op{Kind: Delete, Path: path})
		}
	}

	byPath := func(a, b Op) int { return strings.Compare(a.Path, b.Path) }
	bottomUp := func(a, b Op) int { return -byPath(a, b) } // a child sorts after its parent
	slices.SortFunc(typeDel, bottomUp)
	slices.SortFunc(mkdirs, byPath)
	slices.SortFunc(uploads, byPath)
	slices.SortFunc(dels, bottomUp)
	p.Ops = slices.Concat(typeDel, mkdirs, uploads, dels)
	slices.Sort(p.Warnings)
	slices.Sort(p.Errors)
	return p
}

// sameMTime compares mtimes for equality within window after clamping (spec §5.1).
func sameMTime(a, b, window int64) bool {
	d := tree.ClampMTime(a) - tree.ClampMTime(b)
	return max(d, -d) <= window
}

// under reports whether path is dir or lies inside it. Every path is under "".
func under(path, dir string) bool {
	return dir == "" || path == dir || strings.HasPrefix(path, dir+"/")
}

// parent returns the parent of a relative path, "" for a top-level one.
func parent(path string) string {
	i := strings.LastIndexByte(path, '/')
	if i < 0 {
		return ""
	}
	return path[:i]
}

func insideReplaced(path string, replaced map[string]bool) bool {
	for d := parent(path); d != ""; d = parent(d) {
		if replaced[d] {
			return true
		}
	}
	return false
}
