package main

import (
	"context"
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
	if c.Cmd != "check" {
		fmt.Fprintln(os.Stderr, "owysync: this command is not implemented yet")
		return 2
	}

	dir, err := localDir(c.Path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "owysync:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	env, err := syncer.Check(ctx, dir, os.Stdout) // the FAIL line already printed the reason
	if err != nil {
		return 1
	}
	env.Close()
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
