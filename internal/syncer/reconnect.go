package syncer

import (
	"context"
	"fmt"
	"io"
	"time"

	"owysync/internal/remote"
)

// Reconnect backoff (spec §10).
const (
	firstRetryDelay = time.Second
	maxRetryDelay   = 30 * time.Second
)

// lost reports whether err came from a lost connection rather than from the sync itself.
// A broken connection may fail an operation a moment before it is reported closed.
func lost(ctx context.Context, env *Env) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case <-env.client().Done():
		return true
	case <-time.After(500 * time.Millisecond):
		return false
	}
}

// reconnect replaces the lost connection, retrying with a delay that doubles from 1 s to 30 s
// (spec §10). Host key and authentication errors and a missing marker are final. The remote
// snapshot is not trusted after this: the caller runs a full resync.
func reconnect(ctx context.Context, env *Env, out, errOut io.Writer) error {
	old := env.client()
	why := "connection lost"
	if err := old.Err(); err != nil {
		why += ": " + err.Error()
	}
	fmt.Fprintf(errOut, "warning: %s; reconnecting\n", why)
	old.Close()
	cfg := env.Cfg
	for delay := firstRetryDelay; ; delay = min(2*delay, maxRetryDelay) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		c, err := remote.Connect(ctx, connOptions(cfg))
		if err != nil && !remote.Retryable(err) {
			return err
		}
		if err == nil {
			var has bool
			if has, err = c.HasMarker(env.Root); err == nil && !has {
				c.Close()
				return fmt.Errorf("no .owysync/remote marker in %s after reconnecting; check the remote directory", env.Root)
			}
			if err == nil {
				env.setClient(c)
				if ctx.Err() != nil { // Ctrl+C came while connecting
					c.Close()
					return ctx.Err()
				}
				fmt.Fprintln(out, "reconnected")
				return nil
			}
			c.Close()
		}
		fmt.Fprintf(errOut, "warning: reconnect failed: %v; next try in %s\n", err, min(2*delay, maxRetryDelay))
	}
}
