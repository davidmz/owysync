package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	"owysync/internal/tree"
)

// ScanResult is the result of the remote scan (spec §8).
type ScanResult struct {
	Entries    []tree.Entry
	Unreadable []string // directories without r or x (spec §8.4.1), relative paths
}

// shQuote quotes s for sh in single quotes. It is the only way a root or a pattern
// gets into a script; file names never do (invariant 3).
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// scanScript is the script of spec §8.3. Every command that could read stdin has it redirected:
// the rest of stdin is the script itself (spec §8.2).
const scanScript = `echo owysync-scan-begin; echo owysync-scan-begin >&2
LC_ALL=C; export LC_ALL
root=@ROOT@; tmp="$root/.owysync/tmp/scan.$$"
: > "$tmp" || { echo owysync-scan-error-tmp >&2; exit 1; }
find "$root" -mindepth 1 \( -path "$root/.owysync"@PRUNE@ \) -prune -o -print0 > "$tmp" </dev/null
cat "$tmp" </dev/null; printf '\0'
[ -s "$tmp" ] && xargs -0 sh -c 'for p; do
  [ -d "$p" ] && [ ! -L "$p" ] && { [ -r "$p" ] && [ -x "$p" ] || printf "%s\0" "$p"; }
done' sh < "$tmp"
printf '\0'
[ -s "$tmp" ] && xargs -0 stat @STAT@ < "$tmp"
rm -f "$tmp" </dev/null
echo owysync-scan-done
`

// ScanScript builds the scan script for the canonical root. prune holds extra find operands
// that go inside the prune group right after `-path "$root/.owysync"`, so each condition
// starts with "-o" (for example "-o", "-name", "node_modules"); every operand is quoted with shQuote.
func ScanScript(root string, v StatVariant, prune []string) string {
	var p strings.Builder
	for _, a := range prune {
		p.WriteString(" ")
		p.WriteString(shQuote(a))
	}
	format := `-c '%f %s %Y %n'`
	if v == StatBSD {
		format = `-f '%p %z %m %N'`
	}
	return strings.NewReplacer("@ROOT@", shQuote(root), "@PRUNE@", p.String(), "@STAT@", format).Replace(scanScript)
}

// Scan walks the remote tree under the canonical root (spec §8).
func (c *Client) Scan(ctx context.Context, root string, v StatVariant, prune []string) (ScanResult, error) {
	stdout, stderr, err := c.RunScript(ctx, ScanScript(root, v, prune))
	if err != nil {
		return ScanResult{}, err
	}
	return ParseScan(stdout, stderr, root, v)
}

const (
	scanBegin = "owysync-scan-begin"
	scanDone  = "owysync-scan-done\n"
)

// ParseScan parses the output of the scan script (spec §8.4, 8.6). It is a pure function.
// Entries under .owysync/ are dropped even if find did not prune them (a root with glob characters).
func ParseScan(stdout, stderr []byte, root string, v StatVariant) (ScanResult, error) {
	res, perr := parseScanStdout(stdout, root, v)
	if err := checkScanStderr(stderr, len(res.Unreadable) > 0); err != nil {
		return ScanResult{}, err
	}
	if perr != nil {
		return ScanResult{}, perr
	}
	return res, nil
}

func parseScanStdout(stdout []byte, root string, v StatVariant) (ScanResult, error) {
	var res ScanResult
	data, ok := afterMarker(stdout, scanBegin)
	if !ok {
		return res, errors.New("scan: sh -s printed no start marker: the remote has a restricted shell or a non-standard login command")
	}
	errCut := errors.New("scan output was cut off: no completion marker; the result is discarded")
	if !bytes.HasSuffix(data, []byte(scanDone)) {
		return res, errCut
	}
	prefix := strings.TrimSuffix(root, "/") + "/"

	// Sections 1 and 2: NUL-terminated absolute paths, each section ends with an empty record.
	section := func() ([]string, error) {
		var list []string
		for {
			i := bytes.IndexByte(data, 0)
			if i < 0 {
				return nil, errCut
			}
			rec := string(data[:i])
			data = data[i+1:]
			if rec == "" {
				return list, nil
			}
			list = append(list, rec)
		}
	}
	paths, err := section()
	if err != nil {
		return res, err
	}
	unreadable, err := section()
	if err != nil {
		return res, err
	}

	rel := func(p string) (string, error) {
		r, ok := strings.CutPrefix(p, prefix)
		if !ok || r == "" {
			return "", fmt.Errorf("scan: path %q is outside the root %s", p, root)
		}
		return r, nil
	}
	service := func(r string) bool { return r == ".owysync" || strings.HasPrefix(r, ".owysync/") }
	bad := map[string]bool{}
	for _, p := range unreadable {
		r, err := rel(p)
		if err != nil {
			return res, err
		}
		if !service(r) {
			bad[r] = true
			res.Unreadable = append(res.Unreadable, r)
		}
	}
	// Paths inside an unreadable directory are dropped: stat does not work on them (spec §8.4.2).
	under := func(r string) bool {
		for d := path.Dir(r); d != "."; d = path.Dir(d) {
			if bad[d] {
				return true
			}
		}
		return false
	}
	var keep []string // absolute paths expected in section 3, in order
	for _, p := range paths {
		r, err := rel(p)
		if err != nil {
			return res, err
		}
		if !under(r) {
			keep = append(keep, p)
		}
	}

	// Section 3: "<mode> <size> <mtime> <name>\n" per path, in order; lines of vanished
	// paths are missing (spec §8.4, step 3).
	base := 16
	if v == StatBSD {
		base = 8
	}
	for i := 0; ; {
		if bytes.HasPrefix(data, []byte(scanDone)) {
			return res, nil // remaining paths vanished during the scan
		}
		if len(data) == 0 {
			return res, errCut // the suffix belonged to a file name
		}
		mode, size, mtime, rest, err := statHead(data, base)
		if err != nil {
			return res, err
		}
		j := i
		for j < len(keep) && !(bytes.HasPrefix(rest, []byte(keep[j])) && len(rest) > len(keep[j]) && rest[len(keep[j])] == '\n') {
			j++
		}
		if j == len(keep) {
			line, _, _ := bytes.Cut(data, []byte("\n"))
			return res, fmt.Errorf("scan: stat line %q matches no listed path", line)
		}
		data = rest[len(keep[j])+1:]
		i = j + 1
		r, _ := rel(keep[j])
		if service(r) {
			continue
		}
		res.Entries = append(res.Entries, tree.Entry{
			Path:  r,
			Type:  modeType(mode),
			Size:  size,
			MTime: tree.ClampMTime(mtime),
			Mode:  uint32(mode & 0o7777),
		})
	}
}

// statHead parses "<mode> <size> <mtime> " and returns the rest of the data.
func statHead(data []byte, modeBase int) (mode uint64, size, mtime int64, rest []byte, err error) {
	line, _, _ := bytes.Cut(data, []byte("\n"))
	malformed := fmt.Errorf("scan: protocol error: malformed stat line %q", line)
	var f [3]string
	for k := range f {
		i := bytes.IndexByte(data, ' ')
		if i < 0 {
			return 0, 0, 0, nil, malformed
		}
		f[k], data = string(data[:i]), data[i+1:]
	}
	mode, err1 := strconv.ParseUint(f[0], modeBase, 32)
	size, err2 := strconv.ParseInt(f[1], 10, 64)
	mtime, err3 := strconv.ParseInt(f[2], 10, 64)
	if errors.Join(err1, err2, err3) != nil {
		return 0, 0, 0, nil, malformed
	}
	return mode, size, mtime, data, nil
}

// modeType maps the S_IFMT bits to a Type.
func modeType(mode uint64) tree.Type {
	switch mode & 0o170000 {
	case 0o100000:
		return tree.File
	case 0o040000:
		return tree.Dir
	case 0o120000:
		return tree.Symlink
	}
	return tree.Other
}

// checkScanStderr applies the table of spec §8.6 to stderr after the start marker.
func checkScanStderr(stderr []byte, haveUnreadable bool) error {
	body, ok := afterMarker(stderr, scanBegin)
	if !ok {
		return errors.New("scan: no start marker in stderr: the remote has a restricted shell or a non-standard login command")
	}
	var other []string
	prev := ""
	for _, line := range strings.Split(string(body), "\n") {
		switch {
		case line == "":
			continue
		case strings.Contains(line, "No such file or directory"):
			// The object vanished during the scan.
		case strings.Contains(line, "Permission denied") && haveUnreadable:
			// Explained by the unreadable directories (spec §8.4.1).
		case line == "owysync-scan-error-tmp":
			return fmt.Errorf("cannot create the scan temp file in .owysync/tmp: %s", prev)
		case strings.Contains(line, "No space left on device"), strings.Contains(line, "Disk quota exceeded"):
			return fmt.Errorf("no space for the scan temp file in .owysync/tmp (about 60 bytes per path): %s", line)
		default:
			other = append(other, line)
		}
		prev = line
	}
	if len(other) > 0 {
		return fmt.Errorf("remote scan failed:\n  %s\nif these paths need not be synced, add them to .owysync/ignore", strings.Join(other, "\n  "))
	}
	return nil
}
