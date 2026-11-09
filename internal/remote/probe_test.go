package remote

import (
	"strings"
	"testing"
)

func TestParseProbe(t *testing.T) {
	const begin, done = "owysync-probe-begin\n", "owysync-probe-done\n"
	for _, tc := range []struct {
		name, stdout, stderr string
		want                 StatVariant
		errPart              string
	}{
		{"gnu", begin + "stat=gnu\nmissing=\n" + done, "owysync-probe-begin\n", StatGNU, ""},
		{"bsd", begin + "stat=bsd\nmissing=\n" + done, "owysync-probe-begin\n", StatBSD, ""},
		{"noise before marker, no trailing newline", "hello\nprompt " + begin + "stat=gnu\nmissing=\n" + done, "stty: not a tty\nowysync-probe-begin\n", StatGNU, ""},
		{"no start marker", "hello\n", "", 0, "start marker"},
		{"no start marker, stderr text", "", "sh: not found\n", 0, "sh: not found"},
		{"no completion marker", begin + "stat=gnu\n", "owysync-probe-begin\nboom\n", 0, "boom"},
		{"no stat", begin + "stat=none\nmissing=\n" + done, "", 0, "stat"},
		{"no tar", begin + "stat=gnu\nmissing= tar xargs(-0)\n" + done, "", 0, "tar xargs(-0)"},
	} {
		got, err := ParseProbe([]byte(tc.stdout), []byte(tc.stderr))
		if tc.errPart == "" {
			if err != nil || got != tc.want {
				t.Errorf("%s: got %v, %v; want %v", tc.name, got, err, tc.want)
			}
		} else if err == nil || !strings.Contains(err.Error(), tc.errPart) {
			t.Errorf("%s: err = %v, want substring %q", tc.name, err, tc.errPart)
		}
	}
}

func TestHarmlessTarWarning(t *testing.T) {
	for line, want := range map[string]bool{
		"tar: a.txt: time stamp 2030-01-01 00:00:00 is 1.5 s in the future": true,
		"tar: Ignoring unknown extended header keyword 'hdrcharset'":        true,
		"tar: Ignoring unknown extended header keyword `hdrcharset'":        true, // GNU tar before 1.28
		"tar: Ignoring unknown extended header keyword 'SCHILY.fflags'":     false,
		"tar: a.txt: Cannot open: Permission denied":                        false,
	} {
		if got := harmlessTarWarning(line); got != want {
			t.Errorf("%q: %v, want %v", line, got, want)
		}
	}
}
