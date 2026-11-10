package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"owysync/internal/localfs"
	"owysync/internal/plan"
	"owysync/internal/remote"
	"owysync/internal/tray"
	"owysync/internal/tree"
)

// Options control a resync.
type Options struct {
	DryRun  bool             // print the plan, change nothing
	Verbose bool             // print every operation
	Chime   func()           // called after a sync that changed the remote without errors; may be nil
	State   func(tray.State) // called by Live when the sync state changes; may be nil
}

// ErrFailed means the resync ran to the end but was not successful; the reasons are already printed.
var ErrFailed = errors.New("resync failed")

// errStale means the remote snapshot does not match the remote: retrying will not help.
var errStale = errors.New("the remote snapshot is stale; run again")

// Resync runs one full resync (spec §6) over a checked environment. Progress goes to out,
// warnings and errors to errOut. It returns nil only when the resync was successful.
func Resync(ctx context.Context, env *Env, localDir string, o Options, out, errOut io.Writer) error {
	lock, err := localfs.Lock(localDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	// Ctrl+C: closing the connection aborts every SFTP call in flight (spec §4).
	defer context.AfterFunc(ctx, func() { env.Close() })()

	for {
		_, err = runResync(ctx, env, localDir, o, out, errOut)
		if err == nil || !lost(ctx, env) {
			return err
		}
		if err := reconnect(ctx, env, out, errOut); err != nil {
			return err
		}
	}
}

// synced is the remote state a resync leaves for live mode.
type synced struct {
	x       *executor // holds the remote snapshot and the paths whose operations failed
	unknown []string  // non-ignored unreadable remote directories
}

// runResync is Resync without the lock. On success and on ErrFailed it returns the remote state
// (with a nil executor on a dry run).
func runResync(ctx context.Context, env *Env, localDir string, o Options, out, errOut io.Writer) (*synced, error) {
	start := time.Now()
	fmt.Fprintln(out, "resync: started")
	s, err := pass(ctx, env, localDir, o, out, errOut, start, false)
	if errors.Is(err, errBulk) && !env.client().Closed() { // a lost connection is the caller's
		fmt.Fprintln(out, "resync: scanning the remote again after the failed bulk operation")
		s, err = pass(ctx, env, localDir, o, out, errOut, start, true)
	}
	return s, err
}

// pass is one scan-diff-apply round of Resync. Unless last, a bulk failure returns errBulk
// right away: the remote snapshot is no longer trusted (invariant 2). On the last pass it
// counts as a failed operation.
func pass(ctx context.Context, env *Env, localDir string, o Options, out, errOut io.Writer, start time.Time, last bool) (*synced, error) {
	client := env.client()
	if err := client.ResetTmp(ctx, env.Root, !o.DryRun); err != nil {
		return nil, err
	}

	var res remote.ScanResult
	var scanErr error
	var wg sync.WaitGroup
	wg.Go(func() { res, scanErr = client.Scan(ctx, env.Root, env.Stat, env.Ignore.PruneArgs(env.Root)) })
	local, skipped, err := localfs.Walk(localDir, env.Ignore)
	wg.Wait()
	if err != nil {
		return nil, err
	}
	if scanErr != nil {
		return nil, scanErr
	}

	// Skipped local paths count as ignored (spec §2, 5): hide them from the remote side too.
	var skippedPaths []string
	for _, s := range skipped {
		fmt.Fprintln(errOut, "warning:", s)
		skippedPaths = append(skippedPaths, s.Path)
	}
	hidden := func(p string) bool {
		return env.Ignore.Excluded(p) || slices.ContainsFunc(skippedPaths, func(s string) bool { return under(p, s) })
	}
	rsnap := tree.Snapshot{}
	for _, e := range res.Entries {
		if !hidden(e.Path) {
			rsnap[e.Path] = e
		}
	}
	var unknown []string
	for _, u := range res.Unreadable {
		if !hidden(u) {
			unknown = append(unknown, u)
		}
	}

	cfg := env.Cfg
	p := plan.Diff(local, rsnap, unknown, "", plan.Options{FileMode: cfg.FileMode, DirMode: cfg.DirMode, MTimeWindow: int64(cfg.MTimeWindow)})
	for _, w := range p.Warnings {
		fmt.Fprintln(errOut, "warning:", w)
	}
	for _, e := range p.Errors {
		fmt.Fprintln(errOut, "error:", e)
	}
	failed := len(p.Errors)

	s := &synced{unknown: unknown}
	if o.DryRun {
		var size int64
		for _, op := range p.Ops {
			fmt.Fprintln(out, opLine(op, rsnap))
			if op.Kind == plan.Upload {
				size += op.Local.Size
			}
		}
		fmt.Fprintf(out, "dry run: %d operations, %d bytes to upload; nothing changed\n", len(p.Ops), size)
	} else {
		w, err := client.Writer(env.Root)
		if err != nil {
			return nil, err
		}
		x := &executor{w: w, snap: rsnap, localDir: localDir, out: out, errOut: errOut, verbose: o.Verbose, concurrency: cfg.Concurrency}
		s.x = x
		n, err := x.runPlan(ctx, p.Ops, cfg.BulkThreshold)
		failed += n
		if err != nil && ctx.Err() == nil {
			fmt.Fprintln(errOut, "error:", err)
			if !last {
				return nil, err
			}
			failed++
		}
	}

	if len(p.Unknown) > 0 {
		fmt.Fprintf(errOut, "not synced (no access): %s\n", strings.Join(p.Unknown, ", "))
	}
	files := 0
	for _, e := range local {
		if e.Type == tree.File {
			files++
		}
	}
	took := time.Since(start).Round(time.Millisecond)
	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case failed > 0 || len(p.Unknown) > 0:
		fmt.Fprintf(out, "resync: failed in %s: %d local files, %d operations, %d errors, %d unreadable directories\n", took, files, len(p.Ops), failed, len(p.Unknown))
		return s, ErrFailed
	}
	fmt.Fprintf(out, "resync: done in %s: %d local files, %d operations\n", took, files, len(p.Ops))
	if len(p.Ops) > 0 && !o.DryRun && o.Chime != nil {
		o.Chime()
	}
	return s, nil
}

// display quotes a name that contains control characters (a remote name with a newline).
func display(p string) string {
	if strings.ContainsFunc(p, unicode.IsControl) {
		return strconv.Quote(p)
	}
	return p
}

// opLine renders an operation in the live output format (spec §11): + new, ~ replaced, - deleted.
// A path with control characters (a remote name with a newline) is quoted.
func opLine(op plan.Op, rsnap tree.Snapshot) string {
	p := display(op.Path)
	switch {
	case op.Kind == plan.Delete || op.Kind == plan.DeleteForTypeChange:
		return "- " + p
	case op.Kind == plan.Upload && rsnap[op.Path].Type == tree.File:
		return "~ " + p
	}
	return "+ " + p
}

// executor applies plan operations and keeps the remote snapshot in step (invariant 2).
type executor struct {
	w           *remote.Writer
	localDir    string
	out         io.Writer
	errOut      io.Writer
	verbose     bool
	concurrency int // uploads at a time

	mu     sync.Mutex
	snap   tree.Snapshot
	kept   []string // directories left in place; their ancestors are not reported again
	failed []string // paths whose operations failed; live mode retries them (spec §7)
	stale  bool     // an operation found the remote snapshot out of date (spec §7)
}

// fail reports a failed operation on p. The caller holds x.mu.
func (x *executor) fail(p string, err error) {
	fmt.Fprintln(x.errOut, "error:", err)
	// A missing parent whose own operation failed in this run says nothing about the remote.
	if errors.Is(err, errStale) && !slices.ContainsFunc(x.failed, func(f string) bool { return under(p, f) }) {
		x.stale = true
	}
	x.failed = append(x.failed, p)
}

// run applies ops one by one over SFTP; consecutive uploads run up to concurrency at a time.
// It returns the number of failed operations.
func (x *executor) run(ctx context.Context, ops []plan.Op) int {
	failed := 0
	var wg sync.WaitGroup
	sem := make(chan struct{}, max(x.concurrency, 1))
	var fmu sync.Mutex
	for i, op := range ops {
		if ctx.Err() != nil || x.w.Closed() {
			break
		}
		if op.Kind != plan.Upload {
			if !x.do(ctx, op) {
				failed++
			}
			continue
		}
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			if !x.do(ctx, op) {
				fmu.Lock()
				failed++
				fmu.Unlock()
			}
		})
		if i+1 == len(ops) || ops[i+1].Kind != plan.Upload {
			wg.Wait()
		}
	}
	wg.Wait()
	return failed
}

// do applies one operation with up to 3 retries (spec §10) and reports the outcome.
func (x *executor) do(ctx context.Context, op plan.Op) bool {
	x.mu.Lock()
	line := opLine(op, x.snap)
	x.mu.Unlock()
	var err error
	for attempt := 0; ; attempt++ {
		err = x.apply(op)
		if err == nil || errors.Is(err, errStale) || errors.Is(err, remote.ErrNotEmpty) || ctx.Err() != nil || x.w.Closed() || attempt == 3 {
			break
		}
		select {
		case <-time.After(200 * time.Millisecond << attempt):
		case <-ctx.Done():
		}
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	switch {
	case err == nil:
		if x.verbose {
			fmt.Fprintln(x.out, line)
		}
		return true
	case ctx.Err() != nil:
		return false
	case x.w.Closed(): // one connection message is enough; the reconnect resyncs everything
		x.failed = append(x.failed, op.Path)
		return false
	case errors.Is(err, remote.ErrNotEmpty) && op.Kind == plan.Delete:
		x.keep(op.Path)
		return true
	}
	x.kept = append(x.kept, op.Path) // a failed child makes its parent "not empty": not news
	x.fail(op.Path, err)
	return false
}

// keep reports a directory left in place because it holds ignored content (spec §9), once per
// subtree: the parents of a kept directory are not news. The caller holds x.mu.
func (x *executor) keep(p string) {
	if !slices.ContainsFunc(x.kept, func(k string) bool { return under(k, p) }) {
		fmt.Fprintf(x.out, "kept directory %s: it holds ignored files\n", p)
	}
	x.kept = append(x.kept, p)
}

func (x *executor) apply(op plan.Op) error {
	x.mu.Lock()
	err := checkParents(x.snap, op.Path)
	r := x.snap[op.Path]
	x.mu.Unlock()
	if err != nil {
		return err
	}
	switch op.Kind {
	case plan.DeleteForTypeChange, plan.Delete:
		if err := x.w.Remove(op.Path, r.Type == tree.Dir); err != nil {
			return err
		}
		x.set(op.Path, nil)
	case plan.Mkdir:
		if err := x.w.Mkdir(op.Path, op.Mode); err != nil {
			return remoteErr(err)
		}
		x.set(op.Path, &tree.Entry{Path: op.Path, Type: tree.Dir, Mode: op.Mode})
	case plan.Upload:
		e, err := x.upload(op)
		if err != nil {
			return err
		}
		x.set(op.Path, &e)
	}
	return nil
}

func (x *executor) set(p string, e *tree.Entry) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if e == nil {
		delete(x.snap, p)
	} else {
		x.snap[p] = *e
	}
}

// upload sends the current local file and checks it did not change meanwhile (spec §9).
func (x *executor) upload(op plan.Op) (tree.Entry, error) {
	full := filepath.Join(x.localDir, filepath.FromSlash(op.Path))
	before, err := lstatFile(full)
	if err != nil {
		return tree.Entry{}, err
	}
	f, err := os.Open(full)
	if err != nil {
		return tree.Entry{}, fmt.Errorf("reading %s: %w", full, err)
	}
	defer f.Close()
	if err := x.w.Upload(op.Path, f, op.Mode, before.MTime); err != nil {
		return tree.Entry{}, remoteErr(err)
	}
	after, err := lstatFile(full)
	if err != nil {
		return tree.Entry{}, err
	}
	if after != before {
		return tree.Entry{}, fmt.Errorf("%s changed during upload", full)
	}
	return tree.Entry{Path: op.Path, Type: tree.File, Size: before.Size, MTime: before.MTime, Mode: op.Mode}, nil
}

// remoteErr marks a remote "no such file" or "already exists" as errStale: the snapshot said
// otherwise (spec §7). Only errors of remote calls go through it, never local ones.
func remoteErr(err error) error {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%w (%w)", err, errStale)
	}
	return err
}

type fileStamp struct{ Size, MTime int64 }

func lstatFile(full string) (fileStamp, error) {
	fi, err := os.Lstat(full)
	if err != nil {
		return fileStamp{}, fmt.Errorf("reading %s: %w", full, err)
	}
	if !fi.Mode().IsRegular() {
		return fileStamp{}, fmt.Errorf("%s is no longer a regular file", full)
	}
	return fileStamp{fi.Size(), tree.ClampMTime(fi.ModTime().Unix())}, nil
}

// checkParents enforces invariant 6: every parent of p is a directory, not a symlink,
// in the remote snapshot, so a write never leaves the root.
func checkParents(snap tree.Snapshot, p string) error {
	for d := parentDir(p); d != ""; d = parentDir(d) {
		if e, ok := snap[d]; !ok || e.Type != tree.Dir {
			what := "missing"
			if ok {
				what = "a " + e.Type.String()
			}
			return fmt.Errorf("%s: parent %s is %s on the remote: %w", p, d, what, errStale)
		}
	}
	return nil
}

func parentDir(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return ""
	}
	return p[:i]
}

// under reports whether p is dir or lies inside it.
func under(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }
