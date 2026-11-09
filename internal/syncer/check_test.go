package syncer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckBadIgnore: an invalid mask fails check 1 with the line number, before any connection.
func TestCheckBadIgnore(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".owysync"), 0o755)
	os.WriteFile(filepath.Join(dir, ".owysync", "config"), []byte("host = h\nuser = u\nremote_root = /r\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".owysync", "ignore"), []byte("# masks\n*.sw?\n"), 0o644)
	var out bytes.Buffer
	env, err := Check(context.Background(), dir, &out)
	if err == nil || env != nil {
		t.Fatalf("expected an error, got %v\n%s", err, out.String())
	}
	if s := out.String(); !strings.HasPrefix(s, "1. config and ignore: FAIL: ") || !strings.Contains(s, "line 2") || strings.Count(s, "\n") != 1 {
		t.Fatalf("unexpected output:\n%s", s)
	}
}
