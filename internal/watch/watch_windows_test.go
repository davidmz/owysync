package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcher(t *testing.T) {
	root := t.TempDir()
	w, err := Start(root)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // let the first call start
	os.MkdirAll(filepath.Join(root, "dir", "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "dir", "sub", "файл.txt"), []byte("x"), 0o644)
	os.Rename(filepath.Join(root, "dir"), filepath.Join(root, "moved"))

	got := map[string]bool{} // path -> seen as non-shallow
	timeout := time.After(5 * time.Second)
	for !got["moved"] {
		select {
		case evs := <-w.C:
			for _, e := range evs {
				got[e.Path] = got[e.Path] || !e.Shallow
			}
		case <-timeout:
			t.Fatalf("no rename event; got %v", got)
		}
	}
	for _, p := range []string{"dir", "dir/sub", "dir/sub/файл.txt"} {
		if !got[p] {
			t.Errorf("no create event for %q; got %v", p, got)
		}
	}

	done := make(chan error)
	go func() { done <- w.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close hangs")
	}
	for range w.C { // closed
	}
}
