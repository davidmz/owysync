package localfs

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestWalkSpecial checks that a junction, a symlink and a name with an unpaired surrogate
// are skipped with a warning and never entered.
func TestWalkSpecial(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	mkTree(t, root, "a.txt", "sub/")
	mkTree(t, target, "outside.txt")

	if out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(root, "junction"), target).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v %s", err, out)
	}
	wantWarn := []string{"junction"}
	// Symlinks need Developer Mode or admin rights.
	if err := os.Symlink(target, filepath.Join(root, "sub", "dirlink")); err == nil {
		os.Symlink(filepath.Join(target, "outside.txt"), filepath.Join(root, "filelink"))
		wantWarn = append(wantWarn, "sub/dirlink", "filelink")
	} else {
		t.Logf("symlinks not checked: %v", err)
	}

	// "x\uD800y": an unpaired high surrogate.
	name := append(windows.StringToUTF16(root + `\x`)[:len(root)+2], 0xD800, 'y', 0)
	h, err := windows.CreateFile(&name[0], windows.GENERIC_WRITE, 0, nil, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	windows.CloseHandle(h)
	wantWarn = append(wantWarn, "surrogate")

	_, paths, warnings := walkPaths(t, root, "")
	if want := []string{"a.txt", "sub"}; !reflect.DeepEqual(paths, want) {
		t.Errorf("paths = %q, want %q", paths, want)
	}
	all := strings.Join(warnings, "\n")
	for _, w := range wantWarn {
		if !strings.Contains(all, w) {
			t.Errorf("no warning about %q in:\n%s", w, all)
		}
	}
	if len(warnings) != len(wantWarn) {
		t.Errorf("warnings = %q, want %d", warnings, len(wantWarn))
	}
}
