// Package ignore implements the exclusion masks of .owysync/ignore (spec §4.4)
// and their conservative translation into find conditions (spec §8.5).
package ignore

import (
	"fmt"
	"regexp"
	"strings"
)

type mask struct {
	text string // normalized: '/' separators, no leading or trailing '/'
	name bool   // name mask: matched against one path component
	re   *regexp.Regexp
}

// Set is a parsed ignore file.
type Set struct {
	masks []mask
}

// Parse parses the text of an ignore file. Errors carry the line number.
func Parse(text string) (*Set, error) {
	s := &Set{}
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line) // also drops the CR of CRLF
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m, err := parseMask(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %q: %w", n+1, line, err)
		}
		s.masks = append(s.masks, m)
	}
	return s, nil
}

func parseMask(line string) (mask, error) {
	if strings.HasPrefix(line, "!") {
		return mask{}, fmt.Errorf("negation (!) is not supported")
	}
	if strings.Contains(line, "?") {
		return mask{}, fmt.Errorf("? is not supported, use *")
	}
	t := strings.TrimRight(strings.ReplaceAll(line, `\`, "/"), "/")
	name := !strings.Contains(t, "/") && !strings.Contains(t, "**")
	t = strings.TrimLeft(t, "/")
	if t == "" {
		return mask{}, fmt.Errorf("empty mask")
	}
	for _, c := range strings.Split(t, "/") {
		if c == "." || c == ".." {
			return mask{}, fmt.Errorf("components . and .. are not allowed")
		}
	}
	return mask{text: t, name: name, re: regexp.MustCompile("^" + toRegexp(t) + "$")}, nil
}

// toRegexp translates a mask: "**/" → zero or more directories, other "**" → anything,
// "*" → anything but '/', the rest is literal.
func toRegexp(t string) string {
	var b strings.Builder
	for t != "" {
		i := strings.IndexByte(t, '*')
		if i < 0 {
			b.WriteString(regexp.QuoteMeta(t))
			break
		}
		b.WriteString(regexp.QuoteMeta(t[:i]))
		t = t[i:]
		stars := len(t) - len(strings.TrimLeft(t, "*"))
		t = t[stars:]
		switch {
		case stars == 1:
			b.WriteString("[^/]*")
		case strings.HasPrefix(t, "/"):
			b.WriteString("(?:.*/)?")
			t = t[1:]
		default:
			b.WriteString(".*")
		}
	}
	return b.String()
}

// ExcludedName reports whether a mask matches p itself, without looking at its ancestors:
// a name mask is matched against the last component, a path mask against the whole path.
// It suits a walk that does not descend into excluded directories.
func (s *Set) ExcludedName(p string) bool {
	last := p[strings.LastIndexByte(p, '/')+1:]
	for _, m := range s.masks {
		if m.name && m.re.MatchString(last) || !m.name && m.re.MatchString(p) {
			return true
		}
	}
	return false
}

// Excluded reports whether p or any of its ancestors is excluded (spec §4.4).
// It suits a flat stream of entries, such as the remote scan.
func (s *Set) Excluded(p string) bool {
	for i := 0; i <= len(p); i++ {
		if (i == len(p) || p[i] == '/') && s.ExcludedName(p[:i]) {
			return true
		}
	}
	return false
}

// PruneArgs returns the find operands that prune excluded paths under root, following the
// table of spec §8.5. Each condition starts with "-o" (see remote.ScanScript). Masks that cannot
// be translated exactly are left out: pruning is only a speed-up and must never cut more than
// the masks (invariant 9).
func (s *Set) PruneArgs(root string) []string {
	var args []string
	pathOK := !strings.ContainsAny(root, `*?[\`)
	prefix := strings.TrimSuffix(root, "/") + "/"
	for _, m := range s.masks {
		if strings.Contains(m.text, "[") {
			continue
		}
		switch {
		case m.name:
			args = append(args, "-o", "-name", m.text)
		case !pathOK:
		case !strings.Contains(m.text, "*"):
			args = append(args, "-o", "-path", prefix+m.text)
		default:
			// Only a trailing "/*" or "/**": find's "*" also matches deeper paths,
			// but their parent is pruned already.
			dir := strings.TrimRight(m.text, "*")
			if strings.HasSuffix(dir, "/") && !strings.Contains(dir, "*") {
				args = append(args, "-o", "-path", prefix+dir+"*")
			}
		}
	}
	return args
}
