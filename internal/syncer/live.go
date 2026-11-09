package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"owysync/internal/localfs"
	"owysync/internal/plan"
	"owysync/internal/tree"
	"owysync/internal/watch"
)

// Live intervals (spec §7).
const (
	maxBatchDelay = time.Second      // a batch starts at most this long after its first event
	retryInterval = 30 * time.Second // paths whose operations failed are tried again
)

// Live runs a full resync and then keeps the remote in step with local changes until ctx is
// done (spec §6, 7). A failed resync does not stop it: failed paths are retried every 30 s.
// It returns ctx's error, or an error that stops the sync (a lost connection).
func Live(ctx context.Context, env *Env, localDir string, o Options, out, errOut io.Writer) error {
	lock, err := localfs.Lock(localDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer context.AfterFunc(ctx, func() { env.Close() })()

	// The watcher starts before the resync walks local: events meanwhile wait in the dirty set.
	w, err := watch.Start(localDir)
	if err != nil {
		return err
	}
	defer w.Close()
	l := &live{env: env, localDir: localDir, verbose: o.Verbose, out: out, errOut: errOut,
		debounce: time.Duration(env.Cfg.DebounceMS) * time.Millisecond,
		dirty:    map[string]bool{}, failed: map[string]bool{}, wake: make(chan struct{}, 1)}
	go l.collect(w.C)
	return l.loop(ctx)
}

type live struct {
	env      *Env
	localDir string
	verbose  bool
	out      io.Writer
	errOut   io.Writer
	debounce time.Duration

	s      *synced         // the remote snapshot; used by the loop goroutine only
	failed map[string]bool // paths to retry; used by the loop goroutine only

	mu          sync.Mutex
	dirty       map[string]bool // changed path -> its subtree must be read (not shallow)
	first, last time.Time       // first and last event of the pending batch
	wake        chan struct{}
	warned      map[string]bool // service files already reported as changed
}

// collect moves watcher events into the dirty set.
func (l *live) collect(c <-chan []watch.Event) {
	for evs := range c {
		now := time.Now()
		l.mu.Lock()
		for _, e := range evs {
			if e.Err != nil {
				fmt.Fprintf(l.errOut, "warning: %v; reading the whole local tree\n", e.Err)
			}
			if under(e.Path, ".owysync") {
				if (e.Path == ".owysync/config" || e.Path == ".owysync/ignore") && !l.warned[e.Path] {
					fmt.Fprintf(l.errOut, "warning: %s changed; the changes take effect after a restart\n", e.Path)
					if l.warned == nil {
						l.warned = map[string]bool{}
					}
					l.warned[e.Path] = true
				}
				continue
			}
			if e.Path != "" && l.env.Ignore.Excluded(e.Path) {
				continue
			}
			if len(l.dirty) == 0 {
				l.first = now
			}
			l.last = now
			l.dirty[e.Path] = l.dirty[e.Path] || !e.Shallow
		}
		l.mu.Unlock()
		select {
		case l.wake <- struct{}{}:
		default:
		}
	}
}

func (l *live) loop(ctx context.Context) error {
	retry := time.NewTicker(retryInterval)
	defer retry.Stop()
	needResync := true
	for {
		if needResync {
			if err := l.resync(ctx); err != nil {
				return err
			}
			needResync = false
		}
		var wait <-chan time.Time
		if due, ok := l.due(); ok {
			if d := time.Until(due); d > 0 {
				wait = time.After(d)
			} else {
				needResync = l.apply(ctx, l.take())
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if l.s.x.w.Closed() { // the batch saw the connection go
					if err := reconnect(ctx, l.env, l.out, l.errOut); err != nil {
						return err
					}
					needResync = true
				}
				continue
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-l.env.client().Done(): // lost while idle, e.g. the laptop slept
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := reconnect(ctx, l.env, l.out, l.errOut); err != nil {
				return err
			}
			needResync = true
		case <-l.wake:
		case <-wait:
		case <-retry.C:
			l.mu.Lock()
			for p := range l.failed {
				l.dirty[p] = true // first and last are old: the batch is due at once
			}
			l.mu.Unlock()
			clear(l.failed)
		}
	}
}

// resync runs a full resync, reconnecting while the connection is lost. The dirty set is
// cleared first: the resync reads local after that.
func (l *live) resync(ctx context.Context) error {
	l.mu.Lock()
	clear(l.dirty)
	l.mu.Unlock()
	clear(l.failed)
	s, err := runResync(ctx, l.env, l.localDir, Options{Verbose: l.verbose}, l.out, l.errOut)
	for err != nil && lost(ctx, l.env) {
		if err := reconnect(ctx, l.env, l.out, l.errOut); err != nil {
			return err
		}
		s, err = runResync(ctx, l.env, l.localDir, Options{Verbose: l.verbose}, l.out, l.errOut)
	}
	if err != nil && !errors.Is(err, ErrFailed) {
		return err
	}
	s.x.verbose = true // every live operation is printed (spec §11)
	for _, p := range s.x.failed {
		l.failed[p] = true
	}
	l.s = s
	return nil
}

// due returns when the pending batch must be processed: after debounce of quiet, but at most
// maxBatchDelay after its first event (spec §7).
func (l *live) due() (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.dirty) == 0 {
		return time.Time{}, false
	}
	due := l.last.Add(l.debounce)
	if limit := l.first.Add(maxBatchDelay); limit.Before(due) {
		due = limit
	}
	return due, true
}

func (l *live) take() map[string]bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	d := l.dirty
	l.dirty = map[string]bool{}
	return d
}

// apply syncs a batch of dirty paths: each path not inside another deep one is read from local
// and compared with the snapshot under it (spec §7). It reports whether the snapshot turned out
// to be stale, so that a full resync must follow.
func (l *live) apply(ctx context.Context, dirty map[string]bool) bool {
	x := l.s.x
	x.failed, x.stale, x.kept = nil, false, nil
	scopes := map[string]bool{}
	local := tree.Snapshot{}
	var hidden []string
	for p, deep := range dirty {
		if covered(p, dirty) {
			continue
		}
		// A directory only modified (a child came or went): the children report themselves.
		if !deep && x.snap[p].Type == tree.Dir {
			if fi, err := os.Lstat(filepath.Join(l.localDir, filepath.FromSlash(p))); err == nil && fi.IsDir() {
				continue
			}
		}
		sub, skipped, err := localfs.WalkUnder(l.localDir, p, l.env.Ignore)
		if err != nil {
			fmt.Fprintln(l.errOut, "error:", err)
			l.failed[p] = true
			continue
		}
		maps.Copy(local, sub)
		for _, s := range skipped { // they count as ignored (spec §2, 5)
			fmt.Fprintln(l.errOut, "warning:", s)
			hidden = append(hidden, s.Path)
		}
		scopes[p] = true
	}
	if len(scopes) == 0 {
		return false
	}

	// ponytail: scans the whole snapshot per batch (~10 ms at 200k paths); index by directory if it shows up.
	remote := tree.Snapshot{}
	for p, e := range x.snap {
		if inScope(p, scopes) && !slices.ContainsFunc(hidden, func(h string) bool { return under(p, h) }) {
			remote[p] = e
		}
	}
	cfg := l.env.Cfg
	pl := plan.Diff(local, remote, l.s.unknown, "", plan.Options{FileMode: cfg.FileMode, DirMode: cfg.DirMode, MTimeWindow: int64(cfg.MTimeWindow)})
	for _, w := range pl.Warnings {
		fmt.Fprintln(l.errOut, "warning:", w)
	}
	for _, e := range pl.Errors {
		fmt.Fprintln(l.errOut, "error:", e)
	}
	_, err := x.runPlan(ctx, pl.Ops, cfg.BulkThreshold)
	for _, p := range x.failed {
		l.failed[p] = true
	}
	if ctx.Err() != nil {
		return false
	}
	if err != nil { // a failed bulk operation: the snapshot is not trusted (invariant 2)
		fmt.Fprintln(l.errOut, "error:", err)
		return true
	}
	if x.stale {
		fmt.Fprintln(l.out, "resync: the remote changed behind our back; starting a full resync")
	}
	return x.stale
}

// covered reports whether an ancestor of p ("" is the root) is dirty with its whole subtree.
func covered(p string, dirty map[string]bool) bool {
	for p != "" {
		p = parentDir(p)
		if dirty[p] {
			return true
		}
	}
	return false
}

// inScope reports whether p is one of scopes or lies inside one ("" is the whole tree).
func inScope(p string, scopes map[string]bool) bool {
	for {
		if scopes[p] {
			return true
		}
		if p == "" {
			return false
		}
		p = parentDir(p)
	}
}
