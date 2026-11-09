package remote

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

// StatVariant is the format of the stat command on the remote (spec §8.2).
type StatVariant uint8

const (
	StatGNU StatVariant = iota + 1 // GNU/BusyBox: stat -c '%f %s %Y %n'
	StatBSD                        // BSD: stat -f '%p %z %m %N'
)

func (v StatVariant) String() string {
	switch v {
	case StatGNU:
		return "stat -c (GNU/BusyBox)"
	case StatBSD:
		return "stat -f (BSD)"
	}
	return "?"
}

// probeScript follows the rules of spec §8.2: markers in stdout and stderr, a completion marker,
// stdin of every command is redirected.
const probeScript = `echo owysync-probe-begin; echo owysync-probe-begin >&2
LC_ALL=C; export LC_ALL
if stat -c '%f %s %Y %n' / </dev/null >/dev/null 2>&1; then echo stat=gnu
elif stat -f '%p %z %m %N' / </dev/null >/dev/null 2>&1; then echo stat=bsd
else echo stat=none; fi
m=''
for c in find xargs tar rm rmdir; do command -v "$c" </dev/null >/dev/null 2>&1 || m="$m $c"; done
find / -mindepth 1 -prune -print0 </dev/null >/dev/null 2>&1 || m="$m find(-mindepth,-print0)"
printf 'a\0' | xargs -0 echo >/dev/null 2>&1 || m="$m xargs(-0)"
echo "missing=$m"
echo owysync-probe-done
`

// Probe probes the shell on the remote.
func (c *Client) Probe(ctx context.Context) (StatVariant, error) {
	stdout, stderr, err := c.RunScript(ctx, probeScript)
	if err != nil {
		return 0, err
	}
	return ParseProbe(stdout, stderr)
}

// ParseProbe parses the output of probeScript. It is a pure function.
func ParseProbe(stdout, stderr []byte) (StatVariant, error) {
	body, ok := afterMarker(stdout, "owysync-probe-begin")
	if !ok {
		return 0, fmt.Errorf("sh -s printed no start marker: the remote has a restricted shell or a non-standard login command%s", stderrTail(stderr))
	}
	var variant StatVariant
	missing, done := "", false
	for _, line := range strings.Split(string(body), "\n") {
		switch {
		case line == "stat=gnu":
			variant = StatGNU
		case line == "stat=bsd":
			variant = StatBSD
		case strings.HasPrefix(line, "missing="):
			missing = strings.TrimSpace(strings.TrimPrefix(line, "missing="))
		case line == "owysync-probe-done":
			done = true
		}
	}
	if !done {
		return 0, fmt.Errorf("the shell probe was cut off: no completion marker%s", stderrTail(stderr))
	}
	if variant == 0 {
		return 0, fmt.Errorf("neither `stat -c` nor `stat -f` works on the remote: GNU/BusyBox or BSD stat is required")
	}
	if missing != "" {
		return 0, fmt.Errorf("the remote lacks required utilities or their features: %s", missing)
	}
	return variant, nil
}

// afterMarker returns the data after the first occurrence of marker+"\n". Everything before it is the output
// of login shell rc files (spec §8.4, step 0); it may lack a trailing newline.
func afterMarker(data []byte, marker string) ([]byte, bool) {
	i := bytes.Index(data, []byte(marker+"\n"))
	if i < 0 {
		return nil, false
	}
	return data[i+len(marker)+1:], true
}

func stderrTail(stderr []byte) string {
	if body, ok := afterMarker(stderr, "owysync-probe-begin"); ok {
		stderr = body
	}
	s := strings.TrimSpace(string(stderr))
	if s == "" {
		return ""
	}
	return ": " + s
}
