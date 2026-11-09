//go:build !windows

// Package sound plays the chime that marks a finished sync.
package sound

// Play does nothing outside Windows: the sync client targets Windows.
func Play(wait bool) bool { return false }
