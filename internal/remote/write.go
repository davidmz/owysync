package remote

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
)

// Writer performs SFTP writes under the canonical root (spec §9). Paths are relative to the root.
// It does not check the remote snapshot: the caller does (invariant 6).
type Writer struct {
	s     *sftp.Client
	root  string
	posix bool // posix-rename@openssh.com is supported

	mu       sync.Mutex
	fallback map[string]bool // directories where a rename from .owysync/tmp failed
}

// Writer returns a Writer for the canonical root.
func (c *Client) Writer(root string) (*Writer, error) {
	s, err := c.SFTP()
	if err != nil {
		return nil, err
	}
	_, posix := s.HasExtension("posix-rename@openssh.com")
	return &Writer{s: s, root: root, posix: posix, fallback: map[string]bool{}}, nil
}

func (w *Writer) abs(rel string) string { return path.Join(w.root, rel) }

func randName() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Upload writes src to rel atomically (spec §9): a temp file gets mode before the first byte
// (invariant 7), then the data and mtime, then it is renamed over the target. If the rename from
// .owysync/tmp fails (typically a mount point inside the tree), the temp file is created next to
// the target instead, and the directory goes that way for the rest of the session.
// src must be seekable: the fallback rewrites the data.
func (w *Writer) Upload(rel string, src io.ReadSeeker, mode uint32, mtime int64) error {
	dir := path.Dir(rel)
	w.mu.Lock()
	fb := w.fallback[dir]
	w.mu.Unlock()
	if !fb {
		err := w.upload(path.Join(".owysync/tmp", randName()), rel, src, mode, mtime)
		var re *renameError
		if !errors.As(err, &re) {
			return err
		}
		w.mu.Lock()
		w.fallback[dir] = true
		w.mu.Unlock()
		if _, err := src.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	return w.upload(path.Join(dir, "."+path.Base(rel)+".owysync-"+randName()), rel, src, mode, mtime)
}

type renameError struct{ err error }

func (e *renameError) Error() string { return e.err.Error() }
func (e *renameError) Unwrap() error { return e.err }

func (w *Writer) upload(tmpRel, rel string, src io.Reader, mode uint32, mtime int64) (err error) {
	tmp, target := w.abs(tmpRel), w.abs(rel)
	f, err := w.s.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return fmt.Errorf("upload %s: creating %s: %w", rel, tmpRel, err)
	}
	defer func() {
		if err != nil {
			w.s.Remove(tmp)
		}
	}()
	if err = f.Chmod(os.FileMode(mode)); err != nil {
		f.Close()
		return fmt.Errorf("upload %s: chmod: %w", rel, err)
	}
	if _, err = f.ReadFrom(src); err != nil {
		f.Close()
		return fmt.Errorf("upload %s: writing: %w", rel, err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("upload %s: closing: %w", rel, err)
	}
	t := time.Unix(mtime, 0)
	if err = w.s.Chtimes(tmp, t, t); err != nil {
		return fmt.Errorf("upload %s: setting mtime: %w", rel, err)
	}
	if w.posix {
		err = w.s.PosixRename(tmp, target)
	} else { // no atomic replace: the target is briefly missing (spec §3)
		if err = w.s.Remove(target); err == nil || errors.Is(err, fs.ErrNotExist) {
			err = w.s.Rename(tmp, target)
		}
	}
	if err != nil {
		return &renameError{fmt.Errorf("upload %s: rename from %s: %w", rel, tmpRel, err)}
	}
	return nil
}

// Mkdir creates rel and sets its mode explicitly: the server applies its umask to mkdir (spec §9).
func (w *Writer) Mkdir(rel string, mode uint32) error {
	if err := w.s.Mkdir(w.abs(rel)); err != nil {
		return fmt.Errorf("mkdir %s: %w", rel, err)
	}
	if err := w.s.Chmod(w.abs(rel), os.FileMode(mode)); err != nil {
		return fmt.Errorf("chmod %s: %w", rel, err)
	}
	return nil
}

// ErrNotEmpty means rmdir failed because the directory still has content, normally ignored
// files the scan did not see (spec §9). It is not an error of the sync.
var ErrNotEmpty = errors.New("directory not empty")

// Remove deletes one object: rmdir for a directory, remove for anything else (a symlink itself,
// never its target). A missing path is a success.
func (w *Writer) Remove(rel string, dir bool) error {
	p := w.abs(rel)
	var err error
	if dir {
		err = w.s.RemoveDirectory(p)
	} else {
		err = w.s.Remove(p)
	}
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if dir { // SFTP v3 has no ENOTEMPTY: look inside
		if ents, rerr := w.s.ReadDir(p); rerr == nil && len(ents) > 0 {
			return fmt.Errorf("rmdir %s: %w", rel, ErrNotEmpty)
		}
	}
	return fmt.Errorf("remove %s: %w", rel, err)
}

const tmpScript = `echo owysync-tmp-begin; echo owysync-tmp-begin >&2
LC_ALL=C; export LC_ALL
t=@ROOT@/.owysync/tmp
@WIPE@mkdir -p "$t" </dev/null && echo owysync-tmp-done
`

// ResetTmp empties .owysync/tmp (spec §6, step 2). With wipe false it only makes sure the
// directory exists (dry run: nothing is changed, but the scan needs it).
func (c *Client) ResetTmp(ctx context.Context, root string, wipe bool) error {
	w := ""
	if wipe {
		w = `rm -rf "$t" </dev/null && `
	}
	stdout, stderr, err := c.RunScript(ctx, strings.NewReplacer("@ROOT@", shQuote(root), "@WIPE@", w).Replace(tmpScript))
	if err != nil {
		return err
	}
	if body, ok := afterMarker(stdout, "owysync-tmp-begin"); !ok || !bytes.Contains(body, []byte("owysync-tmp-done\n")) {
		msg, _ := afterMarker(stderr, "owysync-tmp-begin")
		return fmt.Errorf("cannot recreate %s/.owysync/tmp: %s", root, bytes.TrimSpace(msg))
	}
	return nil
}
