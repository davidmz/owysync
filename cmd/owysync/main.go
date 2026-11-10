package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"

	"owysync/internal/sound"
	"owysync/internal/syncer"
	"owysync/internal/tray"
)

// version is the main module version that go build stamps from the git tag
// (v0.1.0 on a tagged commit, a pseudo-version otherwise, +dirty for uncommitted changes).
// It is "dev" when the build has no VCS information.
var version = func() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}()

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM) // SIGTERM: the console window was closed
	code := run(ctx, os.Args[1:])
	interrupted := ctx.Err() != nil
	stop() // cancels ctx; from here Ctrl+C and the close button end the process at once
	if code != 0 && !interrupted && tray.OwnsConsole() {
		// The window closes with the process: keep the error on screen.
		fmt.Fprint(os.Stderr, "Press Enter to close this window")
		os.Stdin.Read(make([]byte, 1)) // a console reads a whole line
	}
	os.Exit(code)
}

func run(ctx context.Context, args []string) int {
	c, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "owysync:", err)
		return 2
	}
	switch c.Cmd {
	case "version":
		fmt.Println("owysync", version)
		return 0
	case "help":
		fmt.Println("owysync", version)
		fmt.Print(usage)
		return 0
	}
	dir, err := localDir(c.Path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "owysync:", err)
		return 1
	}
	live := c.Cmd == "sync" && !c.Once && !c.DryRun
	if live && tray.RelaunchInConhost() {
		return 0 // the sync goes on in a window that can hide in the tray
	}

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

	// A sync is a log: every line gets the time. The dry-run plan is not a log.
	var stdout, stderr io.Writer = os.Stdout, os.Stderr
	if !c.DryRun {
		stdout, stderr = newStampWriter(os.Stdout), newStampWriter(os.Stderr)
	}

	// The tray icon shows how a live sync goes; it is grey while the check connects.
	var icon *tray.Icon
	if live {
		if icon, err = tray.Start("owysync: " + filepath.Base(dir)); err != nil {
			fmt.Fprintln(stderr, "warning: no tray icon:", err)
		}
		defer icon.Close()
	}

	// The check lines are noise for a sync; show them only when a check fails.
	var checkOut bytes.Buffer
	env, err := syncer.Check(ctx, dir, &checkOut)
	if err != nil {
		stderr.Write(checkOut.Bytes())
		return 1
	}
	defer env.Close()
	o := syncer.Options{DryRun: c.DryRun, Verbose: c.Verbose, State: icon.Set}
	if env.Cfg.Sound {
		o.Chime = func() { sound.Play(c.Once) }
	}
	sync := syncer.Live
	if c.Once {
		sync = syncer.Resync
	}
	if err := sync(ctx, env, dir, o, stdout, stderr); err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(stderr, "owysync: interrupted")
		} else if !errors.Is(err, syncer.ErrFailed) {
			fmt.Fprintln(stderr, "owysync:", err)
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
