// Package syncer orchestrates checks, resync and live mode (spec §4.2, 6, 7).
package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"owysync/internal/config"
	"owysync/internal/ignore"
	"owysync/internal/remote"
)

// Env is the result of a successful Check: the open connection and everything the checks found out.
type Env struct {
	Cfg    *config.Config
	Ignore *ignore.Set
	Client *remote.Client
	Root   string // canonical remote_root (realpath)
	Stat   remote.StatVariant
}

func (e *Env) Close() error { return e.Client.Close() }

// Check runs checks 1-8 from spec §4.2 in order, printing one line per check to out
// (`ok` or `FAIL: reason`). It stops at the first failure and returns the error; the
// FAIL line is already printed. On success it returns the open connection.
func Check(ctx context.Context, localDir string, out io.Writer) (*Env, error) {
	return check(ctx, localDir, out, 8)
}

// checkConnection runs checks 1-6 only (spec §4.1, step 4): init has not looked at remote_root yet,
// so Env.Root stays empty.
func checkConnection(ctx context.Context, localDir string, out io.Writer) (*Env, error) {
	return check(ctx, localDir, out, 6)
}

// check runs the checks up to and including check number last.
func check(ctx context.Context, localDir string, out io.Writer, last int) (*Env, error) {
	env := &Env{}
	ok := false
	defer func() {
		if !ok && env.Client != nil {
			env.Client.Close()
		}
	}()
	report := func(n int, name, detail string, err error) error {
		if err != nil {
			fmt.Fprintf(out, "%d. %s: FAIL: %v\n", n, name, err)
			return err
		}
		if detail != "" {
			detail = " (" + detail + ")"
		}
		fmt.Fprintf(out, "%d. %s: ok%s\n", n, name, detail)
		return nil
	}

	// 1. Config and ignore.
	cfg, err := config.Load(localDir)
	if err == nil {
		// A missing ignore file is an error: the first sync deletes everything on the remote
		// that is not in local and not ignored, so the user must have decided on the masks.
		file := filepath.Join(localDir, config.Dir, "ignore")
		data, e := os.ReadFile(file)
		switch {
		case errors.Is(e, os.ErrNotExist):
			err = fmt.Errorf("file %s not found; create it (an empty file is fine): the first sync deletes everything on the remote that is neither in local nor ignored", file)
		case e != nil:
			err = fmt.Errorf("reading %s: %w", file, e)
		default:
			env.Ignore, e = ignore.Parse(string(data))
			if e != nil {
				err = fmt.Errorf("%s: %w; fix the mask (see the comments at the top of the file)", file, e)
			}
		}
	}
	if report(1, "config and ignore", "", err) != nil {
		return nil, err
	}
	env.Cfg = cfg

	// 2-4. TCP, host key, authentication: one call, the phase is taken from the error.
	client, err := remote.Connect(ctx, remote.Options{Host: cfg.Host, Port: cfg.Port, User: cfg.User, IdentityFile: cfg.IdentityFile})
	steps := []struct {
		stage remote.Stage
		name  string
	}{{remote.StageDial, "TCP connection"}, {remote.StageHostKey, "host key"}, {remote.StageAuth, "authentication"}}
	var ce *remote.ConnError
	for _, s := range steps {
		if errors.As(err, &ce) && ce.Stage == s.stage {
			report(int(s.stage), s.name, "", err)
			return nil, err
		}
		report(int(s.stage), s.name, "", nil)
	}
	if err != nil {
		return nil, err // Connect returns only *ConnError; unreachable
	}
	env.Client = client

	// 5. SFTP.
	_, err = client.SFTP()
	if report(5, "SFTP", "", err) != nil {
		return nil, err
	}

	// 6. Shell, stat, tar.
	env.Stat, err = client.Probe(ctx)
	if report(6, "shell", env.Stat.String(), err) != nil {
		return nil, err
	}

	if last < 7 {
		ok = true
		return env, nil
	}

	// 7. remote_root.
	env.Root, err = client.RealRoot(cfg.RemoteRoot)
	if report(7, "remote_root", env.Root, err) != nil {
		return nil, err
	}

	// 8. Marker.
	has, err := client.HasMarker(env.Root)
	if err == nil && !has {
		err = fmt.Errorf("no .owysync/remote marker in %s; check remote_root, or run `owysync init --remote` for a new directory", env.Root)
	}
	if report(8, "marker .owysync/remote", "", err) != nil {
		return nil, err
	}
	ok = true
	return env, nil
}
