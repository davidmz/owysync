package remote

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
)

// MkdirAll creates abs and its missing parents; every directory it creates gets mode explicitly,
// because the server applies its umask to mkdir (spec §4.1, step 5). An existing component
// that is not a directory is an error.
func (c *Client) MkdirAll(abs string, mode uint32) error {
	s, err := c.SFTP()
	if err != nil {
		return err
	}
	cur := "/"
	for _, name := range strings.Split(strings.Trim(abs, "/"), "/") {
		if name == "" {
			continue
		}
		cur = path.Join(cur, name)
		fi, err := s.Stat(cur)
		switch {
		case err == nil:
			if !fi.IsDir() {
				return fmt.Errorf("%s exists and is not a directory", cur)
			}
			continue
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("stat %s: %w", cur, err)
		}
		if err := s.Mkdir(cur); err != nil {
			return fmt.Errorf("mkdir %s: %w", cur, err)
		}
		if err := s.Chmod(cur, os.FileMode(mode)); err != nil {
			return fmt.Errorf("chmod %s: %w", cur, err)
		}
	}
	return nil
}

// TopLevel returns the names of the objects directly inside dir, sorted.
func (c *Client) TopLevel(dir string) ([]string, error) {
	s, err := c.SFTP()
	if err != nil {
		return nil, err
	}
	ents, err := s.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	names := make([]string, len(ents))
	for i, e := range ents {
		names[i] = e.Name()
	}
	slices.Sort(names) // the server does not sort
	return names, nil
}

// InitRoot creates .owysync, .owysync/tmp and the empty marker .owysync/remote in the canonical
// root (spec §4.1, step 5). Existing directories are kept: an earlier init may have been interrupted.
// The marker goes last, so its presence means the rest is in place.
func (c *Client) InitRoot(root string, dirMode, fileMode uint32) error {
	s, err := c.SFTP()
	if err != nil {
		return err
	}
	for _, d := range []string{".owysync", ".owysync/tmp"} {
		if err := c.MkdirAll(path.Join(root, d), dirMode); err != nil {
			return err
		}
	}
	marker := path.Join(root, ".owysync", "remote")
	f, err := s.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return fmt.Errorf("creating the marker %s: %w", marker, err)
	}
	if err := f.Chmod(os.FileMode(fileMode)); err != nil {
		f.Close()
		return fmt.Errorf("chmod %s: %w", marker, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("creating the marker %s: %w", marker, err)
	}
	return nil
}
