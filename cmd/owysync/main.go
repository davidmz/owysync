package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"owysync/internal/syncer"
)

// version is set at build time: go build -ldflags "-X main.version=0.1.0".
var version = "dev"

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	c, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "owysync:", err)
		return 2
	}
	if c.Version {
		fmt.Println("owysync", version)
		return 0
	}
	switch {
	case c.DryRun && !c.Once:
		fmt.Fprintln(os.Stderr, "owysync: --dry-run works only with --once")
		return 2
	}
	dir, err := localDir(c.Path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "owysync:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if c.Cmd == "init" {
		// The dialog needs a terminal; there is no non-interactive mode in the MVP (spec §4.1).
		if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			fmt.Fprintln(os.Stderr, "owysync: init is interactive and needs a terminal; run it in a console window")
			return 1
		}
		if err := syncer.Init(ctx, dir, c.Remote, os.Stdin, os.Stdout); err != nil {
			if ctx.Err() != nil {
				fmt.Fprintln(os.Stderr, "owysync: interrupted")
			} else if !errors.Is(err, syncer.ErrFailed) {
				fmt.Fprintln(os.Stderr, "owysync:", err)
			}
			return 1
		}
		return 0
	}

	if c.Cmd == "check" {
		env, err := syncer.Check(ctx, dir, os.Stdout) // the FAIL line already printed the reason
		if err != nil {
			return 1
		}
		env.Close()
		return 0
	}

	// The check lines are noise for a sync; show them only when a check fails.
	var checkOut bytes.Buffer
	env, err := syncer.Check(ctx, dir, &checkOut)
	if err != nil {
		os.Stderr.Write(checkOut.Bytes())
		return 1
	}
	defer env.Close()
	sync := syncer.Live
	if c.Once {
		sync = syncer.Resync
	}
	if err := sync(ctx, env, dir, syncer.Options{DryRun: c.DryRun, Verbose: c.Verbose}, os.Stdout, os.Stderr); err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "owysync: interrupted")
		} else if !errors.Is(err, syncer.ErrFailed) {
			fmt.Fprintln(os.Stderr, "owysync:", err)
		}
		return 1
	}
	return 0
}

// localDir returns the absolute path of the local directory; it must exist (spec §4).
func localDir(arg string) (string, error) {
	if arg == "" {
		arg = "."
	}
	dir, err := filepath.Abs(arg)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("directory %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	return dir, nil
}
