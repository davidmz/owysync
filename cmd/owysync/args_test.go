package main

import "testing"

func TestParseArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want cmdLine
	}{
		{nil, cmdLine{Cmd: "help"}},
		{[]string{"help"}, cmdLine{Cmd: "help"}},
		{[]string{"--help"}, cmdLine{Cmd: "help"}},
		{[]string{"version"}, cmdLine{Cmd: "version"}},
		{[]string{"sync"}, cmdLine{Cmd: "sync"}},
		{[]string{"check"}, cmdLine{Cmd: "check"}},
		{[]string{"check", `C:\proj`}, cmdLine{Cmd: "check", Path: `C:\proj`}},
		{[]string{"sync", "-v", "p"}, cmdLine{Cmd: "sync", Path: "p", Verbose: true}},
		{[]string{"sync", "--once", "p", "--dry-run"}, cmdLine{Cmd: "sync", Path: "p", Once: true, DryRun: true}},
		{[]string{"sync", "p", "--once"}, cmdLine{Cmd: "sync", Path: "p", Once: true}},
		{[]string{"sync", "check"}, cmdLine{Cmd: "sync", Path: "check"}},
		{[]string{"init", "--remote", "p"}, cmdLine{Cmd: "init", Path: "p", Remote: true}},
		{[]string{"init", "p", "--remote"}, cmdLine{Cmd: "init", Path: "p", Remote: true}},
		{[]string{"sync", "--", "-weird"}, cmdLine{Cmd: "sync", Path: "-weird"}},
	} {
		got, err := parseArgs(tc.args)
		if err != nil || got != tc.want {
			t.Errorf("%q: got %+v, %v; want %+v", tc.args, got, err, tc.want)
		}
	}
}

func TestParseArgsErrors(t *testing.T) {
	for _, args := range [][]string{
		{"p"},
		{"--once"},
		{"--version"},
		{"version", "x"},
		{"help", "sync"},
		{"sync", "a", "b"},
		{"sync", "--nosuch"},
		{"sync", "--remote"},
		{"sync", "--dry-run"},
		{"init", "--once"},
		{"check", "-v"},
	} {
		if _, err := parseArgs(args); err == nil {
			t.Errorf("%q: expected an error", args)
		}
	}
}
