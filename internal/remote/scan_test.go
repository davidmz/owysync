package remote

import (
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"owysync/internal/tree"
)

// scantestEntries is the tree of kb/fixtures/probes/scantest.sh as the scan must see it:
// rnox/child (in an r-without-x directory), locked/*, node_modules and .owysync are absent.
func scantestEntries(mtime int64) []tree.Entry {
	return []tree.Entry{
		{Path: "rnox", Type: tree.Dir, Size: 4096, MTime: mtime, Mode: 0o444},
		{Path: "nl\nname", Type: tree.File, Size: 1, MTime: mtime, Mode: 0o644},
		{Path: "fifo", Type: tree.Other, Size: 0, MTime: mtime, Mode: 0o644},
		{Path: "locked", Type: tree.Dir, Size: 4096, MTime: mtime, Mode: 0},
		{Path: "a b", Type: tree.File, Size: 3, MTime: mtime, Mode: 0o644},
		{Path: "sub", Type: tree.Dir, Size: 4096, MTime: mtime, Mode: 0o755},
		{Path: "sub/link", Type: tree.Symlink, Size: 4, MTime: mtime, Mode: 0o777},
		{Path: "sub/файл", Type: tree.File, Size: 3, MTime: mtime, Mode: 0o644},
	}
}

func TestParseScanGolden(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mtime int64
	}{{"debian12", 1794169612}, {"alpine320", 1794169612}, {"busybox128", 1794169613}} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, err := os.ReadFile("testdata/scan/" + tc.name + ".stdout")
			if err != nil {
				t.Fatal(err)
			}
			stderr, err := os.ReadFile("testdata/scan/" + tc.name + ".stderr")
			if err != nil {
				t.Fatal(err)
			}
			res, err := ParseScan(stdout, stderr, "/srv/proj", StatGNU)
			if err != nil {
				t.Fatal(err)
			}
			if want := scantestEntries(tc.mtime); !reflect.DeepEqual(res.Entries, want) {
				t.Errorf("entries:\n got %+v\nwant %+v", res.Entries, want)
			}
			if want := []string{"rnox", "locked"}; !reflect.DeepEqual(res.Unreadable, want) {
				t.Errorf("unreadable = %q, want %q", res.Unreadable, want)
			}
		})
	}
}

// scanOut builds scan script output: section 1, section 2, stat lines and the completion marker.
func scanOut(paths, unreadable []string, lines ...string) string {
	s := "owysync-scan-begin\n"
	for _, p := range paths {
		s += "/r/" + p + "\x00"
	}
	s += "\x00"
	for _, p := range unreadable {
		s += "/r/" + p + "\x00"
	}
	s += "\x00"
	for _, l := range lines {
		s += l + "\n"
	}
	return s + "owysync-scan-done\n"
}

func TestParseScan(t *testing.T) {
	const errBegin = "owysync-scan-begin\n"
	ab := []string{"a", "b"}
	lineA, lineB := "81a4 1 100 /r/a", "41ed 4096 200 /r/b"
	entA := tree.Entry{Path: "a", Type: tree.File, Size: 1, MTime: 100, Mode: 0o644}
	entB := tree.Entry{Path: "b", Type: tree.Dir, Size: 4096, MTime: 200, Mode: 0o755}
	for _, tc := range []struct {
		name, stdout, stderr string
		v                    StatVariant
		want                 []tree.Entry
		errPart              string
	}{
		{name: "empty tree", stdout: scanOut(nil, nil), stderr: errBegin},
		{name: "plain", stdout: scanOut(ab, nil, lineA, lineB), stderr: errBegin, want: []tree.Entry{entA, entB}},
		{name: "noise before markers", stdout: "Welcome!\n\x1b[0mprompt> " + scanOut(ab, nil, lineA, lineB), stderr: "stty: not a tty\n" + errBegin, want: []tree.Entry{entA, entB}},
		{name: "bsd octal mode", v: StatBSD, stdout: scanOut(ab, nil, "100644 1 100 /r/a", "40755 4096 200 /r/b"), stderr: errBegin, want: []tree.Entry{entA, entB}},
		{name: "symlink and other", stdout: scanOut(ab, nil, "a1ff 4 100 /r/a", "2190 0 200 /r/b"), stderr: errBegin,
			want: []tree.Entry{{Path: "a", Type: tree.Symlink, Size: 4, MTime: 100, Mode: 0o777}, {Path: "b", Type: tree.Other, MTime: 200, Mode: 0o620}}},
		{name: "mtime clamped", stdout: scanOut([]string{"a", "b"}, nil, "81a4 1 -5 /r/a", "81a4 1 5000000000 /r/b"), stderr: errBegin,
			want: []tree.Entry{{Path: "a", Type: tree.File, Size: 1, Mode: 0o644}, {Path: "b", Type: tree.File, Size: 1, MTime: 1<<32 - 1, Mode: 0o644}}},
		{name: "newline in name", stdout: scanOut([]string{"x\n81a4 1 1 /r/y", "z"}, nil, "81a4 1 100 /r/x\n81a4 1 1 /r/y", "81a4 2 100 /r/z"), stderr: errBegin,
			want: []tree.Entry{{Path: "x\n81a4 1 1 /r/y", Type: tree.File, Size: 1, MTime: 100, Mode: 0o644}, {Path: "z", Type: tree.File, Size: 2, MTime: 100, Mode: 0o644}}},
		{name: "vanished in the middle", stdout: scanOut([]string{"a", "gone", "b"}, nil, lineA, lineB),
			stderr: errBegin + "stat: cannot statx '/r/gone': No such file or directory\n", want: []tree.Entry{entA, entB}},
		{name: "vanished at the end", stdout: scanOut([]string{"a", "b", "gone"}, nil, lineA, lineB),
			stderr: errBegin + "stat: /r/gone: No such file or directory\n", want: []tree.Entry{entA, entB}},
		{name: "service directory dropped", stdout: scanOut([]string{".owysync", ".owysync/tmp", "a"}, []string{".owysync/tmp"}, "41ed 1 1 /r/.owysync", "41ed 1 1 /r/.owysync/tmp", lineA),
			stderr: errBegin, want: []tree.Entry{entA}},
		{name: "children of unreadable directory dropped", stdout: scanOut([]string{"a", "d", "d/x"}, []string{"d"}, lineA, "4124 4096 5 /r/d"),
			stderr: errBegin + "stat: cannot statx '/r/d/x': Permission denied\n",
			want:   []tree.Entry{entA, {Path: "d", Type: tree.Dir, Size: 4096, MTime: 5, Mode: 0o444}}},

		{name: "no start marker", stdout: "hello\n", stderr: errBegin, errPart: "start marker"},
		{name: "no start marker in stderr", stdout: scanOut(nil, nil), stderr: "", errPart: "start marker"},
		{name: "no completion marker", stdout: strings.TrimSuffix(scanOut(ab, nil, lineA, lineB), "owysync-scan-done\n"), stderr: errBegin, errPart: "cut off"},
		{name: "cut inside section 1", stdout: "owysync-scan-begin\n/r/a\x00/r/b", stderr: errBegin, errPart: "cut off"},
		{name: "cut after a name ending like the marker", stdout: "owysync-scan-begin\n/r/owysync-scan-done\x00\x00\x00", stderr: errBegin, errPart: "cut off"},
		{name: "line matches no path", stdout: scanOut(ab, nil, lineA, "81a4 1 1 /r/zzz"), stderr: errBegin, errPart: "matches no listed path"},
		{name: "malformed line", stdout: scanOut(ab, nil, "garbage"), stderr: errBegin, errPart: "malformed"},
		{name: "path outside root", stdout: "owysync-scan-begin\n/etc/passwd\x00\x00\x00owysync-scan-done\n", stderr: errBegin, errPart: "outside the root"},

		// The stderr table of spec §8.6.
		{name: "stderr: no such file is ignored", stdout: scanOut(ab, nil, lineA, lineB), stderr: errBegin + "find: '/r/c': No such file or directory\n", want: []tree.Entry{entA, entB}},
		{name: "stderr: permission denied without unreadable", stdout: scanOut(ab, nil, lineA, lineB), stderr: errBegin + "find: '/r/b': Permission denied\n", errPart: "/r/b': Permission denied"},
		{name: "stderr: temp file error", stdout: "owysync-scan-begin\n", stderr: errBegin + "sh: can't create /r/.owysync/tmp/scan.1: nonexistent directory\nowysync-scan-error-tmp\n", errPart: "temp file in .owysync/tmp: sh: can't create"},
		{name: "stderr: no space", stdout: scanOut(ab, nil, lineA, lineB), stderr: errBegin + "find: write error: No space left on device\n", errPart: "no space"},
		{name: "stderr: quota", stdout: scanOut(ab, nil, lineA, lineB), stderr: errBegin + "find: write error: Disk quota exceeded\n", errPart: "no space"},
		{name: "stderr: other error", stdout: scanOut(ab, nil, lineA, lineB), stderr: errBegin + "find: /r/deep: File name too long\n", errPart: "File name too long"},
		{name: "stderr: other error hints ignore", stdout: scanOut(ab, nil, lineA, lineB), stderr: errBegin + "find: /r/nfs: Stale NFS file handle\n", errPart: "add them to .owysync/ignore"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.v
			if v == 0 {
				v = StatGNU
			}
			res, err := ParseScan([]byte(tc.stdout), []byte(tc.stderr), "/r", v)
			if tc.errPart != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errPart) {
					t.Fatalf("err = %v, want substring %q", err, tc.errPart)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(res.Entries, tc.want) {
				t.Errorf("entries:\n got %+v\nwant %+v", res.Entries, tc.want)
			}
		})
	}
}

func TestParseScanRootSlash(t *testing.T) {
	res, err := ParseScan([]byte("owysync-scan-begin\n/a\x00\x00\x0081a4 1 1 /a\nowysync-scan-done\n"), []byte("owysync-scan-begin\n"), "/", StatGNU)
	if err != nil || len(res.Entries) != 1 || res.Entries[0].Path != "a" {
		t.Fatalf("got %+v, %v", res, err)
	}
}

// TestShQuote checks invariant 3: a quoted string reaches sh as exactly one word.
func TestShQuote(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	for _, s := range []string{"", "plain", "it's", "'", "a b", `$HOME`, "`id`", `"q"`, "x\ny", `\`, "*?[a]"} {
		out, err := exec.Command("sh", "-c", "printf %s "+shQuote(s)).Output()
		if err != nil || string(out) != s {
			t.Errorf("shQuote(%q): sh printed %q, %v", s, out, err)
		}
	}
}

// TestScanScriptQuoting checks that the root and the prune operands reach the script only quoted.
func TestScanScriptQuoting(t *testing.T) {
	s := ScanScript("/srv/it's", StatBSD, []string{"-o", "-name", "*.log"})
	for _, want := range []string{`root='/srv/it'\''s'`, `-path "$root/.owysync" '-o' '-name' '*.log' \)`, `stat -f '%p %z %m %N'`} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q:\n%s", want, s)
		}
	}
}
