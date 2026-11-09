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
	"unicode/utf8"

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
	ra := newReadAhead(x.localDir, ops)
	defer ra.stop()
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
			if n, part, err = writePart(tw, x.localDir, ops, view, ra); err != nil {
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
// before its contents (invariant 7), and every file header carries mode and mtime. Files come
// from ra (nil: read here); every upload op that writePart consumes takes its read from ra.
func writePart(tw *tar.Writer, localDir string, ops []plan.Op, view tree.Snapshot, ra *readAhead) (int, tarPart, error) {
	var p tarPart
	var size int64
	for i, op := range ops {
		if len(p.entries) == partMaxEntries || size >= partMaxBytes {
			return i, p, nil
		}
		var read fileRead
		if op.Kind == plan.Upload {
			read = ra.get(localDir, op.Path) // also for a skipped op: ra goes strictly in order
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
			h := &tar.Header{Typeflag: tar.TypeDir, Name: op.Path + "/", Mode: int64(op.Mode), ModTime: time.Unix(op.Local.MTime, 0), PAXRecords: rawName(op.Path)}
			if err := tw.WriteHeader(h); err != nil {
				return i, p, err
			}
			view[op.Path] = e
			p.entries, p.lines = append(p.entries, e), append(p.lines, line)
			size += 512
			continue
		}
		e, ok, err := writeFile(tw, localDir, op, read)
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

// Read-ahead of bulk uploads: the first open of a fresh file is slow on Windows (the antivirus
// scans it: ~1-5 ms), and in parallel it costs about 5 times less (measured with 8 readers).
const (
	readAheadFiles = 8       // files read ahead of the archive writer: open at most this many at once
	readAheadMax   = 1 << 20 // larger files are not held in memory: the writer streams them itself
)

// fileRead is a file read ahead for the archive.
type fileRead struct {
	before fileStamp
	data   []byte // the whole content of a small file
	large  bool   // not read: the writer streams it
	ok     bool   // read in full and unchanged meanwhile
	err    error  // missing or locked: left out of the archive and sent by SFTP
}

// readAhead reads the files of upload ops in order, up to readAheadFiles ahead of the consumer.
// Small files are read whole and closed at once, so few stay open while the archive is written.
type readAhead struct {
	res  map[string]chan fileRead
	sem  chan struct{}
	done chan struct{}
}

func newReadAhead(localDir string, ops []plan.Op) *readAhead {
	ra := &readAhead{res: map[string]chan fileRead{}, sem: make(chan struct{}, readAheadFiles), done: make(chan struct{})}
	var files []string
	for _, op := range ops {
		if op.Kind == plan.Upload {
			ra.res[op.Path] = make(chan fileRead, 1)
			files = append(files, op.Path)
		}
	}
	go func() {
		for _, p := range files {
			select {
			case ra.sem <- struct{}{}: // freed by get
			case <-ra.done:
				return
			}
			go func() { ra.res[p] <- readFile(localDir, p) }()
		}
	}()
	return ra
}

// get returns the read of the file at p. The upload ops must be got in the order given to
// newReadAhead, each once. A nil readAhead reads the file right here.
func (ra *readAhead) get(localDir, p string) fileRead {
	if ra == nil {
		return readFile(localDir, p)
	}
	r := <-ra.res[p]
	<-ra.sem
	return r
}

// stop ends the read-ahead early; reads in flight finish on their own.
func (ra *readAhead) stop() { close(ra.done) }

func readFile(localDir, rel string) fileRead {
	full := filepath.Join(localDir, filepath.FromSlash(rel))
	before, err := lstatFile(full)
	if err != nil {
		return fileRead{err: err}
	}
	if before.Size > readAheadMax {
		return fileRead{before: before, large: true}
	}
	data, err := os.ReadFile(full)
	if err != nil { // locked by another process
		return fileRead{err: err}
	}
	after, err := lstatFile(full)
	return fileRead{before: before, data: data, ok: err == nil && after == before && int64(len(data)) == before.Size}
}

// writeFile writes one local file into tw from its read r. ok is false when the file could not be
// opened (it is left out) or changed while being read (its content in the archive is not trusted).
// An error means the archive itself cannot be written.
func writeFile(tw *tar.Writer, localDir string, op plan.Op, r fileRead) (e tree.Entry, ok bool, err error) {
	if r.err != nil {
		return tree.Entry{}, false, nil
	}
	before := r.before
	h := &tar.Header{Typeflag: tar.TypeReg, Name: op.Path, Size: before.Size, Mode: int64(op.Mode), ModTime: time.Unix(before.MTime, 0), PAXRecords: rawName(op.Path)}
	e = tree.Entry{Path: op.Path, Type: tree.File, Size: before.Size, MTime: before.MTime, Mode: op.Mode}
	if !r.large {
		if err := tw.WriteHeader(h); err != nil {
			return tree.Entry{}, false, err
		}
		n, err := tw.Write(r.data[:min(int64(len(r.data)), before.Size)])
		if err != nil {
			return tree.Entry{}, false, err
		}
		if _, err := io.CopyN(tw, zeros{}, before.Size-int64(n)); err != nil { // shrank: keep the archive valid
			return tree.Entry{}, false, err
		}
		return e, r.ok, nil
	}

	full := filepath.Join(localDir, filepath.FromSlash(op.Path))
	f, err := os.Open(full)
	if err != nil { // locked by another process
		return tree.Entry{}, false, nil
	}
	defer f.Close()
	if err := tw.WriteHeader(h); err != nil {
		return tree.Entry{}, false, err
	}
	n, _ := io.Copy(tw, io.LimitReader(f, before.Size))
	if n < before.Size { // shrank or a read error: pad to keep the archive valid
		if _, err := io.CopyN(tw, zeros{}, before.Size-n); err != nil {
			return tree.Entry{}, false, err
		}
	}
	after, err := lstatFile(full)
	return e, n == before.Size && err == nil && after == before, nil
}

// rawName returns the PAX record hdrcharset=BINARY for a non-ASCII name. bsdtar (libarchive, FreeBSD)
// otherwise converts UTF-8 names to the locale charset, and under LC_ALL=C it silently turns every
// non-ASCII character into '?' (seen with libarchive 3.4.2 and 3.5.2): "кир" would land as "???".
// With BINARY it takes the bytes as they are. GNU tar and BusyBox ignore the record.
func rawName(name string) map[string]string {
	for i := 0; i < len(name); i++ {
		if name[i] >= utf8.RuneSelf {
			return map[string]string{"hdrcharset": "BINARY"}
		}
	}
	return nil
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
