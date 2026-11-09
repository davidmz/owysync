package localfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// Lock opens .owysync/lock in root with no sharing, so a second instance on the same
// directory fails (spec §4). The OS releases the lock when the process exits.
func Lock(root string) (*os.File, error) {
	p := filepath.Join(root, ".owysync", "lock")
	name, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
		windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return nil, fmt.Errorf("%s is locked: another owysync is already running on this directory", p)
	}
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", p, err)
	}
	return os.NewFile(uintptr(h), p), nil
}
