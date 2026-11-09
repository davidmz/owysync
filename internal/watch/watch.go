// Package watch reports changes under a local directory (spec §7).
package watch

// Event is a change under the watched directory.
type Event struct {
	// Path is relative to the watched directory, '/'-separated. "" means anything may have
	// changed (the buffer overflowed or the watcher failed): the whole tree must be read again.
	Path string
	// Shallow means only the object itself was modified (data or attributes): the subtree of
	// a directory need not be read. Created, deleted and renamed paths are never shallow.
	Shallow bool
	// Err is the watcher error behind a "" event, if any.
	Err error
}
