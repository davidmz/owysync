package localfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLockTwice(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".owysync"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := Lock(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(root); err == nil {
		t.Fatal("second lock succeeded")
	}
	f.Close()
	f, err = Lock(root)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	f.Close()
}
