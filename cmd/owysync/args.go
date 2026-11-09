package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
)

// usage is printed by the help command, after the version line.
const usage = `Usage:
  owysync help                         show this help
  owysync version                      print the version
  owysync sync [--once [--dry-run]] [-v] [path]
                                       full resync, then live mode until Ctrl+C;
                                       --once: full resync only;
                                       --dry-run: print the plan, change nothing;
                                       -v: print every operation of a full resync
  owysync init [--remote] [path]       interactive setup; --remote: only the remote part
  owysync check [path]                 check the setup without writing anything

path is the local directory; the current one by default.
`

// cmdLine is the parsed command line (spec §4).
type cmdLine struct {
	Cmd     string // "help", "version", "sync", "init" or "check"
	Path    string // optional local directory; empty means the current one
	Remote  bool   // init --remote
	Once    bool   // sync --once
	DryRun  bool   // sync --dry-run
	Verbose bool   // sync -v
}

// parseArgs: the first argument is the command; no arguments, "--help" and "-h" mean help.
// The command's flags may appear before and after the path (FlagSet.Parse stops at the
// first positional argument, so parsing loops over the remainder). At most one path is allowed.
func parseArgs(args []string) (cmdLine, error) {
	var c cmdLine
	if len(args) == 0 {
		return cmdLine{Cmd: "help"}, nil
	}
	c.Cmd, args = args[0], args[1:]
	fs := flag.NewFlagSet("owysync "+c.Cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	maxPos := 1
	switch c.Cmd {
	case "--help", "-h", "-help":
		c.Cmd = "help"
		maxPos = 0
	case "help", "version":
		maxPos = 0
	case "sync":
		fs.BoolVar(&c.Once, "once", false, "")
		fs.BoolVar(&c.DryRun, "dry-run", false, "")
		fs.BoolVar(&c.Verbose, "v", false, "")
	case "init":
		fs.BoolVar(&c.Remote, "remote", false, "")
	case "check":
	default:
		return c, fmt.Errorf("unknown command %q; run owysync help", c.Cmd)
	}

	var pos []string
	for rest := args; len(rest) > 0; {
		if err := fs.Parse(rest); err != nil {
			return c, fmt.Errorf("%s: %w", c.Cmd, err)
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

	if len(pos) > maxPos {
		if maxPos == 0 {
			return c, fmt.Errorf("%s: unexpected arguments", c.Cmd)
		}
		return c, errors.New("too many arguments: at most one path is expected")
	}
	if len(pos) == 1 {
		c.Path = pos[0]
	}
	if c.DryRun && !c.Once {
		return c, errors.New("--dry-run works only with --once")
	}
	return c, nil
}
