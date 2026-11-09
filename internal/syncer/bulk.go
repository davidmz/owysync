package syncer

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"owysync/internal/plan"
	"owysync/internal/tree"
)

// Limits of one bulk part (spec §9.1).
const (
	partMaxBytes   = 64 << 20
	partMaxEntries = 5000
)

// errBulk marks a failed bulk operation: the remote snapshot can no longer be trusted
// (invariant 2), and a full resync with a remote scan follows.
var errBulk = errors.New("bulk operation failed")

// runPlan applies the plan phase by phase (spec §6.5). A phase with more than threshold
// operations goes in bulk (spec §9.1); mkdirs count with uploads, since the archive holds them.
// It returns the number of failed operations; an error wrapping errBulk stops the plan.
func (x *executor) runPlan(ctx context.Context, ops []plan.Op, threshold int) (int, error) {
	var typeDel, create, dels []plan.Op
	for _, op := range ops {
		switch op.Kind {
		case plan.DeleteForTypeChange:
			typeDel = append(typeDel, op)
		case plan.Delete:
			dels = append(dels, op)
		default:
			create = append(create, op)
		}
	}
	failed := 0
	for _, s := range []struct {
		name string
		ops  []plan.Op
		bulk func(context.Context, []plan.Op) (int, error)
	}{
		{"delete for type change", typeDel, func(ctx context.Context, ops []plan.Op) (int, error) { return x.bulkDelete(ctx, ops, true) }},
		{"upload", create, x.bulkUpload},
		{"delete", dels, func(ctx context.Context, ops []plan.Op) (int, error) { return x.bulkDelete(ctx, ops, false) }},
	} {
		if ctx.Err() != nil || x.w.Closed() {
			break
		}
		if threshold == 0 || len(s.ops) <= threshold {
			failed += x.run(ctx, s.ops)
			continue
		}
		if x.verbose {
			fmt.Fprintf(x.out, "bulk %s: %d operations\n", s.name, len(s.ops))
		}
		n, err := s.bulk(ctx, s.ops)
		failed += n
		if err != nil {
			return failed, fmt.Errorf("%w: %w", errBulk, err)
		}
	}
	return failed, nil
}

// bulkDelete removes objects by lists (spec §9.1). ops are ordered bottom-up.
func (x *executor) bulkDelete(ctx context.Context, ops []plan.Op, typeChange bool) (int, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	failed := 0
	var files, dirs []string
	var todo []plan.Op
	for _, op := range ops {
		if err := checkParents(x.snap, op.Path); err != nil {
			x.fail(op.Path, err)
			failed++
			continue
		}
		if x.snap[op.Path].Type == tree.Dir {
			dirs = append(dirs, op.Path)
		} else {
			files = append(files, op.Path)
		}
		todo = append(todo, op)
	}
	kept, err := x.w.RemoveList(ctx, files, dirs)
	if err != nil {
		return failed, err
	}
	for _, op := range todo {
		switch {
		case !slices.Contains(kept, op.Path):
			if x.verbose {
				fmt.Fprintln(x.out, opLine(op, x.snap))
			}
			delete(x.snap, op.Path)
		case typeChange:
			x.fail(op.Path, fmt.Errorf("cannot replace the remote directory %s: it holds ignored files", op.Path))
			failed++
		default:
			x.keep(op.Path)
		}
	}
	return failed, nil
}

// bulkUpload sends mkdirs and uploads as tar parts (spec §9.1): part N+1 is uploaded while part N
// is extracted. Files that were locked or changed while being read go by SFTP afterwards.
func (x *executor) bulkUpload(ctx context.Context, ops []plan.Op) (int, error) {
	ops = archiveOrder(ops)
	x.mu.Lock()
	view := maps.Clone(x.snap)
	x.mu.Unlock()

	failed := 0
	var retry []plan.Op
	var prev chan error // extraction of the previous part
	var prevPart tarPart
	wait := func() error {
		if prev == nil {
			return nil
		}
		err := <-prev
		prev = nil
		if err == nil {
			x.confirm(prevPart)
		}
		return err
	}
	for len(ops) > 0 {
		var n int
		var part tarPart
		rel, err := x.w.PutPart(func(w io.Writer) error {
			tw := tar.NewWriter(w)
			var err error
			if n, part, err = writePart(tw, x.localDir, ops, view); err != nil {
				return err
			}
			return tw.Close()
		})
		if werr := wait(); err == nil {
			err = werr
		}
		if err != nil {
			return failed, err
		}
		ops = ops[n:]
		x.mu.Lock()
		for _, e := range part.errs {
			x.fail(e.path, e.err)
		}
		x.mu.Unlock()
		failed += len(part.errs)
		retry = append(retry, part.retry...)
		ch := make(chan error, 1)
		go func() { ch <- x.w.Extract(ctx, rel) }()
		prev, prevPart = ch, part
	}
	if err := wait(); err != nil {
		return failed, err
	}
	return failed + x.run(ctx, retry), nil
}

// archiveOrder sorts mkdirs and uploads by path: a directory goes before its contents,
// since a path sorts before every path it prefixes.
func archiveOrder(ops []plan.Op) []plan.Op {
	return slices.SortedFunc(slices.Values(ops), func(a, b plan.Op) int { return strings.Compare(a.Path, b.Path) })
}

// confirm puts the entries of an extracted part into the snapshot.
func (x *executor) confirm(p tarPart) {
	x.mu.Lock()
	defer x.mu.Unlock()
	for i, e := range p.entries {
		x.snap[e.Path] = e
		if x.verbose {
			fmt.Fprintln(x.out, p.lines[i])
		}
	}
}

// tarPart is one written part of a bulk upload.
type tarPart struct {
	entries []tree.Entry // in the archive; they enter the snapshot once the part is extracted
	lines   []string     // output line of each entry
	retry   []plan.Op    // files locked or changed while being read: they go by SFTP
	errs    []opError    // operations the snapshot does not allow; left out
}

type opError struct {
	path string
	err  error
}

// writePart writes ops into tw until the part is full and returns how many ops it consumed.
// view is the remote snapshot as it will be once the earlier parts are extracted; writePart
// adds the directories it writes to it. A new directory is a separate entry with its mode,
// before its contents (invariant 7), and every file header carries mode and mtime.
func writePart(tw *tar.Writer, localDir string, ops []plan.Op, view tree.Snapshot) (int, tarPart, error) {
	var p tarPart
	var size int64
	for i, op := range ops {
		if len(p.entries) == partMaxEntries || size >= partMaxBytes {
			return i, p, nil
		}
		if err := checkParents(view, op.Path); err != nil {
			p.errs = append(p.errs, opError{op.Path, err})
			continue
		}
		r, exists := view[op.Path]
		if exists && (op.Kind == plan.Mkdir || r.Type != tree.File) {
			p.errs = append(p.errs, opError{op.Path, fmt.Errorf("%s: a %s is in the way on the remote: %w", op.Path, r.Type, errStale)})
			continue
		}
		line := opLine(op, view)
		if op.Kind == plan.Mkdir {
			e := tree.Entry{Path: op.Path, Type: tree.Dir, Mode: op.Mode}
			h := &tar.Header{Typeflag: tar.TypeDir, Name: op.Path + "/", Mode: int64(op.Mode), ModTime: time.Unix(op.Local.MTime, 0)}
			if err := tw.WriteHeader(h); err != nil {
				return i, p, err
			}
			view[op.Path] = e
			p.entries, p.lines = append(p.entries, e), append(p.lines, line)
			size += 512
			continue
		}
		e, ok, err := writeFile(tw, localDir, op)
		if err != nil {
			return i, p, err
		}
		if !ok {
			p.retry = append(p.retry, op)
		}
		if e.Path != "" && ok {
			p.entries, p.lines = append(p.entries, e), append(p.lines, line)
		}
		size += 512 + e.Size
	}
	return len(ops), p, nil
}

// writeFile writes one local file into tw. ok is false when the file could not be opened (it is
// left out) or changed while being read (its content in the archive is not trusted). An error
// means the archive itself cannot be written.
func writeFile(tw *tar.Writer, localDir string, op plan.Op) (e tree.Entry, ok bool, err error) {
	full := filepath.Join(localDir, filepath.FromSlash(op.Path))
	before, err := lstatFile(full)
	if err != nil {
		return tree.Entry{}, false, nil
	}
	f, err := os.Open(full)
	if err != nil { // locked by another process
		return tree.Entry{}, false, nil
	}
	defer f.Close()
	h := &tar.Header{Typeflag: tar.TypeReg, Name: op.Path, Size: before.Size, Mode: int64(op.Mode), ModTime: time.Unix(before.MTime, 0)}
	if err := tw.WriteHeader(h); err != nil {
		return tree.Entry{}, false, err
	}
	n, _ := io.Copy(tw, io.LimitReader(f, before.Size))
	if n < before.Size { // shrank or a read error: pad to keep the archive valid
		if _, err := io.CopyN(tw, zeros{}, before.Size-n); err != nil {
			return tree.Entry{}, false, err
		}
	}
	e = tree.Entry{Path: op.Path, Type: tree.File, Size: before.Size, MTime: before.MTime, Mode: op.Mode}
	after, err := lstatFile(full)
	return e, n == before.Size && err == nil && after == before, nil
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
