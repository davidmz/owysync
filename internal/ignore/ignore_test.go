package ignore

import (
	"path"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func mustParse(t *testing.T, text string) *Set {
	t.Helper()
	s, err := Parse(text)
	if err != nil {
		t.Fatalf("Parse(%q): %v", text, err)
	}
	return s
}

// TestExamples is the example table of spec §4.4 plus extra cases.
func TestExamples(t *testing.T) {
	for _, tc := range []struct {
		mask     string
		excluded []string
		kept     []string
	}{
		{"node_modules", []string{"node_modules", "web/node_modules/x.js"}, []string{"node_modules2"}},
		{"*.log", []string{"app.log", "var/log/app.log", ".x.log"}, []string{"app.log.1"}},
		{"~*", []string{"~$doc.docx", "a/~lock"}, []string{"a~"}},
		{"/config.php", []string{"config.php"}, []string{"app/config.php"}},
		{"config.php", []string{"config.php", "app/config.php"}, []string{"config.php.bak"}},
		{"var/cache", []string{"var/cache", "var/cache/a/b"}, []string{"a/var/cache", "var/cache2"}},
		{"var/cache/*", []string{"var/cache/a", "var/cache/a/b"}, []string{"var/cache"}},
		{"/*.log", []string{"app.log"}, []string{"var/app.log"}},
		{"**/tmp/*.pid", []string{"tmp/a.pid", "x/y/tmp/a.pid"}, []string{"tmp/sub/a.pid"}},
		{"[id].tsx", []string{"[id].tsx", "pages/[id].tsx"}, []string{"i.tsx"}},

		{"**/x", []string{"x", "a/x", "a/b/x/c"}, []string{"xx", "a/xb"}},
		{"a/**/b", []string{"a/b", "a/x/y/b", "a/b/c"}, []string{"b", "x/a/b", "a/bb"}},
		{"a/**", []string{"a/x", "a/x/y"}, []string{"a", "ab"}},
		{"a/***", []string{"a/x", "a/x/y"}, []string{"a"}},
		{"node_modules/", []string{"node_modules", "a/node_modules/b"}, []string{"node_modules2"}},
		{`var\cache`, []string{"var/cache/x"}, []string{"a/var/cache"}},
		{"  *.tmp  ", []string{"a.tmp"}, []string{"a.tmpx"}},
		{"кэш", []string{"кэш", "a/кэш/b"}, []string{"кэши"}},
		{"Foo", []string{"Foo"}, []string{"foo"}},
		{"a.b", []string{"a.b"}, []string{"axb"}},
	} {
		s := mustParse(t, tc.mask)
		for _, p := range tc.excluded {
			if !s.Excluded(p) {
				t.Errorf("mask %q: %q must be excluded", tc.mask, p)
			}
		}
		for _, p := range tc.kept {
			if s.Excluded(p) {
				t.Errorf("mask %q: %q must not be excluded", tc.mask, p)
			}
		}
	}
}

func TestParseFile(t *testing.T) {
	s := mustParse(t, "# comment\r\n\r\n  # indented comment\r\n*.log\r\nvar/cache\r\n")
	if len(s.masks) != 2 || !s.Excluded("x.log") || !s.Excluded("var/cache/y") || s.Excluded("comment") {
		t.Fatalf("masks = %+v", s.masks)
	}
}

func TestParseErrors(t *testing.T) {
	for _, tc := range []struct{ text, errPart string }{
		{"!keep", "line 1"},
		{"ok\n!keep", "line 2: \"!keep\": negation"},
		{"*.sw?", "? is not supported"},
		{"/", "empty mask"},
		{"//", "empty mask"},
		{"../x", "not allowed"},
		{"a/./b", "not allowed"},
		{"..", "not allowed"},
	} {
		_, err := Parse(tc.text)
		if err == nil || !strings.Contains(err.Error(), tc.errPart) {
			t.Errorf("Parse(%q): err = %v, want substring %q", tc.text, err, tc.errPart)
		}
	}
}

func TestExcludedName(t *testing.T) {
	s := mustParse(t, "node_modules\nvar/cache")
	if !s.ExcludedName("a/node_modules") || !s.ExcludedName("var/cache") {
		t.Error("must match the path itself")
	}
	if s.ExcludedName("node_modules/x") || s.ExcludedName("var/cache/x") {
		t.Error("must not look at ancestors")
	}
}

func TestPruneArgs(t *testing.T) {
	for _, tc := range []struct {
		masks, root string
		want        []string
	}{
		{"node_modules\n*.log\n~*", "/r", []string{"-o", "-name", "node_modules", "-o", "-name", "*.log", "-o", "-name", "~*"}},
		{"/config.php\nvar/cache", "/r", []string{"-o", "-path", "/r/config.php", "-o", "-path", "/r/var/cache"}},
		{"var/cache/*\npublic/uploads/**", "/r", []string{"-o", "-path", "/r/var/cache/*", "-o", "-path", "/r/public/uploads/*"}},
		{"var/cache", "/", []string{"-o", "-path", "/var/cache"}},
		// Not translated: '[', '*' or '**' in the middle of a path mask, a lone "*" path mask.
		{"[id].tsx\npages/[id]\n/*.log\nvar/*/cache\n**/tmp\na/**/b\n/*\n**", "/r", nil},
		// A root with glob characters gives no -path conditions; -name is still fine.
		{"var/cache\nnode_modules", "/srv/a[1]", []string{"-o", "-name", "node_modules"}},
		{"var/cache", "/srv/*", nil},
		{"var/cache", `/srv/a\b`, nil},
	} {
		if got := mustParse(t, tc.masks).PruneArgs(tc.root); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("PruneArgs(%q, %q) = %q, want %q", tc.masks, tc.root, got, tc.want)
		}
	}
}

// findMatch models find: -name is path.Match on the last component, -path is a shell pattern
// on the whole path where '*' also matches '/'.
func findMatch(t *testing.T, args []string, full string) bool {
	t.Helper()
	for i := 0; i < len(args); i += 3 {
		op, pat := args[i+1], args[i+2]
		switch op {
		case "-name":
			ok, err := path.Match(pat, path.Base(full))
			if err != nil {
				t.Fatalf("bad -name pattern %q: %v", pat, err)
			}
			if ok {
				return true
			}
		case "-path":
			if strings.ContainsAny(pat, `?[\`) {
				t.Fatalf("-path pattern %q has characters the model does not handle", pat)
			}
			re := regexp.MustCompile("^" + strings.ReplaceAll(regexp.QuoteMeta(pat), `\*`, ".*") + "$")
			if re.MatchString(full) {
				return true
			}
		default:
			t.Fatalf("unexpected operand %q", op)
		}
	}
	return false
}

// TestPruneConservative checks invariant 9: whatever find would prune is excluded by the masks too.
func TestPruneConservative(t *testing.T) {
	masks := strings.Join([]string{
		".git", ".idea", "~*", "*~", "*.tmp", "*___jb_tmp___", "Thumbs.db",
		"node_modules", "*.log", "/config.php", "var/cache", "var/cache/*", "public/uploads/**",
		"кэш", "/данные/архив", "[id].tsx", "/*.log", "var/*/cache", "**/tmp/*.pid", "a/**/b",
	}, "\n")
	paths := []string{
		"a", "a/b", "a/x/b", "app.log", "var/app.log", "var/log", "var/log/x.log.1",
		"config.php", "app/config.php", "var/cache", "var/cache/x", "var/cache2", "var/cache2/x",
		"var/x/cache", "public/uploads", "public/uploads/a/b", "public/uploads2",
		"кэш", "проект/кэш/файл", "кэши", "данные/архив", "данные/архив/1", "x/данные/архив",
		"[id].tsx", "pages/[id].tsx", "pages/i.tsx", "[a]", "[a]/b", "a[b]c.log",
		"tmp/a.pid", "tmp/sub/a.pid", "~lock", "dir~", "x.tmp", ".git/HEAD", "src/.idea/x",
	}
	for _, root := range []string{"/srv/proj", "/home/u/проект", "/", "/srv/a[1]"} {
		s := mustParse(t, masks)
		args := s.PruneArgs(root)
		if len(args)%3 != 0 {
			t.Fatalf("args not in triples: %q", args)
		}
		prefix := strings.TrimSuffix(root, "/") + "/"
		npruned := 0
		for _, p := range paths {
			// find prunes p if p or any ancestor matches a condition.
			pruned := false
			for i := 0; i <= len(p); i++ {
				if (i == len(p) || p[i] == '/') && findMatch(t, args, prefix+p[:i]) {
					pruned = true
				}
			}
			if pruned {
				npruned++
				if !s.Excluded(p) {
					t.Errorf("root %q: find prunes %q, the masks do not exclude it", root, p)
				}
			}
		}
		if npruned < 10 {
			t.Errorf("root %q: only %d paths pruned; the find model is broken", root, npruned)
		}
	}
}
