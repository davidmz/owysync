//go:build !windows

package watch

import "errors"

// Watcher is available on Windows only.
type Watcher struct {
	// C is never sent to.
	C <-chan []Event
}

// Start fails: live mode needs ReadDirectoryChangesW.
func Start(root string) (*Watcher, error) {
	return nil, errors.New("live mode is supported on Windows only")
}

// Close does nothing.
func (w *Watcher) Close() error { return nil }
