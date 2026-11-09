package remote

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
)

// ErrRootMissing means remote_root does not exist.
var ErrRootMissing = errors.New("remote_root does not exist")

// RealRoot canonicalizes remote_root with SFTP realpath (spec §8.2, step 1)
// and checks that it is a directory. The returned path is used everywhere afterwards.
func (c *Client) RealRoot(root string) (string, error) {
	s, err := c.SFTP()
	if err != nil {
		return "", err
	}
	real, err := s.RealPath(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %s", ErrRootMissing, root)
		}
		return "", fmt.Errorf("realpath %s: %w", root, err)
	}
	fi, err := s.Stat(real)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %s", ErrRootMissing, root)
		}
		return "", fmt.Errorf("stat %s: %w", real, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("remote_root %s (realpath %s) is not a directory", root, real)
	}
	return real, nil
}

// HasMarker reports whether the root contains the .owysync/remote marker (spec §4).
func (c *Client) HasMarker(realRoot string) (bool, error) {
	s, err := c.SFTP()
	if err != nil {
		return false, err
	}
	fi, err := s.Lstat(path.Join(realRoot, ".owysync", "remote"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("marker check: %w", err)
	}
	return fi.Mode().IsRegular(), nil
}
