package main

import (
	"errors"
	"flag"
	"io"
)

// cmdLine is the parsed command line (spec §4).
type cmdLine struct {
	Cmd     string // "init", "check", or "" for sync
	Path    string // optional local directory; empty means the current one
	Remote  bool   // init --remote
	Once    bool
	DryRun  bool
	Verbose bool
	Version bool
}

// parseArgs: flags may appear before and after the path (FlagSet.Parse stops at the first
// positional argument, so parsing loops over the remainder). The first positional
// argument is the command if it is init or check; at most one more is allowed (the path).
func parseArgs(args []string) (cmdLine, error) {
	var c cmdLine
	fs := flag.NewFlagSet("owysync", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&c.Version, "version", false, "")
	fs.BoolVar(&c.Remote, "remote", false, "")
	fs.BoolVar(&c.Once, "once", false, "")
	fs.BoolVar(&c.DryRun, "dry-run", false, "")
	fs.BoolVar(&c.Verbose, "v", false, "")

	var pos []string
	for rest := args; len(rest) > 0; {
		if err := fs.Parse(rest); err != nil {
			return c, err
		}
		after := fs.Args()
		consumed := rest[:len(rest)-len(after)]
		rest = after
		if len(consumed) > 0 && consumed[len(consumed)-1] == "--" { // everything after "--" is positional
			pos = append(pos, rest...)
			break
		}
		if len(rest) > 0 {
			pos, rest = append(pos, rest[0]), rest[1:]
		}
	}

	if len(pos) > 0 && (pos[0] == "init" || pos[0] == "check") {
		c.Cmd, pos = pos[0], pos[1:]
	}
	if len(pos) > 1 {
		return c, errors.New("too many arguments: at most one path is expected")
	}
	if len(pos) == 1 {
		c.Path = pos[0]
	}
	if c.Remote && c.Cmd != "init" {
		return c, errors.New("the --remote flag applies only to the init command")
	}
	return c, nil
}
