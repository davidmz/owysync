// Package tree holds the data model shared by local and remote scans (spec §5).
package tree

import "math"

// Type is the kind of a file system object.
type Type uint8

// Object kinds. The zero value is not a valid Type.
const (
	File Type = iota + 1
	Dir
	Symlink
	Other // fifo, socket, device: never synced or deleted
)

func (t Type) String() string {
	switch t {
	case File:
		return "file"
	case Dir:
		return "dir"
	case Symlink:
		return "symlink"
	case Other:
		return "other"
	}
	return "?"
}

// Entry is one object of a tree.
type Entry struct {
	Path  string // relative, '/' separator, no "./" prefix and no trailing '/'
	Type  Type
	Size  int64
	MTime int64  // Unix seconds, already clamped to the uint32 range (spec §5.1)
	Mode  uint32 // permission bits: the low 12 bits
}

// Snapshot maps a relative path to its entry.
type Snapshot map[string]Entry

// ClampMTime clamps Unix seconds to the uint32 range that SFTP v3 can carry (spec §5.1).
func ClampMTime(sec int64) int64 {
	return min(max(sec, 0), math.MaxUint32)
}
