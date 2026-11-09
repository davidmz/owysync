package watch

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const filter = windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_DIR_NAME |
	windows.FILE_NOTIFY_CHANGE_SIZE | windows.FILE_NOTIFY_CHANGE_LAST_WRITE | windows.FILE_NOTIFY_CHANGE_ATTRIBUTES

// Watcher watches a directory tree with ReadDirectoryChangesW.
type Watcher struct {
	// C delivers the events of one ReadDirectoryChangesW call at a time. It is closed by Close.
	C      <-chan []Event
	h      windows.Handle
	closed atomic.Bool
	stop   chan struct{} // closed by Close: a pending send gives up
	done   chan struct{}
}

// Start watches root and its whole subtree. Changes are recorded from the first
// ReadDirectoryChangesW call, which the watcher goroutine makes right away.
func Start(root string) (*Watcher, error) {
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, fmt.Errorf("watching %s: %w", root, err)
	}
	c := make(chan []Event, 16)
	w := &Watcher{C: c, h: h, stop: make(chan struct{}), done: make(chan struct{})}
	go w.loop(c)
	return w, nil
}

// Close stops the watcher and closes C.
func (w *Watcher) Close() error {
	w.closed.Store(true)
	close(w.stop)
	windows.CancelIoEx(w.h, nil) // wakes the blocked synchronous call
	<-w.done
	return windows.CloseHandle(w.h)
}

func (w *Watcher) loop(c chan<- []Event) {
	defer close(w.done)
	defer close(c)
	buf := make([]byte, 1<<20) // the system buffer gets the same size; 1 MB is fine on local disks
	for {
		var n uint32
		err := windows.ReadDirectoryChanges(w.h, &buf[0], uint32(len(buf)), true, filter, &n, nil, 0)
		if w.closed.Load() {
			return
		}
		var evs []Event
		switch {
		case errors.Is(err, windows.ERROR_NOTIFY_ENUM_DIR) || err == nil && n == 0: // overflow
			evs = []Event{{}}
		case err != nil:
			evs = []Event{{Err: fmt.Errorf("ReadDirectoryChangesW: %w", err)}}
		default:
			evs = parse(buf[:n])
		}
		select {
		case c <- evs:
		case <-w.stop:
			return
		}
		if evs[0].Err != nil {
			time.Sleep(time.Second) // a persistent error must not spin
		}
	}
}

// parse decodes a FILE_NOTIFY_INFORMATION chain.
func parse(buf []byte) []Event {
	var evs []Event
	for off := 0; off+12 <= len(buf); {
		fi := (*windows.FileNotifyInformation)(unsafe.Pointer(&buf[off]))
		name := windows.UTF16ToString(unsafe.Slice(&fi.FileName, fi.FileNameLength/2))
		evs = append(evs, Event{Path: strings.ReplaceAll(name, `\`, "/"), Shallow: fi.Action == windows.FILE_ACTION_MODIFIED})
		if fi.NextEntryOffset == 0 {
			break
		}
		off += int(fi.NextEntryOffset)
	}
	return evs
}
