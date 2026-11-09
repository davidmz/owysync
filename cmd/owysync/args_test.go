package main

import "testing"

func TestParseArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want cmdLine
	}{
		{nil, cmdLine{}},
		{[]string{"--version"}, cmdLine{Version: true}},
		{[]string{"check"}, cmdLine{Cmd: "check"}},
		{[]string{"check", `C:\proj`}, cmdLine{Cmd: "check", Path: `C:\proj`}},
		{[]string{"-v", "check", "p"}, cmdLine{Cmd: "check", Path: "p", Verbose: true}},
		{[]string{"check", "p", "-v"}, cmdLine{Cmd: "check", Path: "p", Verbose: true}},
		{[]string{"--once", "p", "--dry-run"}, cmdLine{Path: "p", Once: true, DryRun: true}},
		{[]string{"p", "--once"}, cmdLine{Path: "p", Once: true}},
		{[]string{`.\check`}, cmdLine{Path: `.\check`}},
		{[]string{"init", "--remote", "p"}, cmdLine{Cmd: "init", Path: "p", Remote: true}},
		{[]string{"--", "-weird"}, cmdLine{Path: "-weird"}},
		{[]string{"-v", "--", "check"}, cmdLine{Cmd: "check", Verbose: true}},
	} {
		got, err := parseArgs(tc.args)
		if err != nil || got != tc.want {
			t.Errorf("%q: got %+v, %v; want %+v", tc.args, got, err, tc.want)
		}
	}
}

func TestParseArgsErrors(t *testing.T) {
	for _, args := range [][]string{
		{"a", "b"},
		{"check", "a", "b"},
		{"--nosuch"},
		{"--remote"},
		{"check", "--remote"},
	} {
		if _, err := parseArgs(args); err == nil {
			t.Errorf("%q: expected an error", args)
		}
	}
}
