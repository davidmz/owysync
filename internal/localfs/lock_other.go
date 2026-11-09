//go:build !windows

package localfs

import (
	"os"
	"path/filepath"
)

// Lock opens .owysync/lock in root. Outside Windows it does not lock: only tests run there.
func Lock(root string) (*os.File, error) {
	return os.OpenFile(filepath.Join(root, ".owysync", "lock"), os.O_RDWR|os.O_CREATE, 0o644)
}
