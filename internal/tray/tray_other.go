//go:build !windows

package tray

import "context"

// Icon does nothing outside Windows: the sync client targets Windows.
type Icon struct{}

// Start returns a no-op icon.
func Start(tip string, exit func()) (*Icon, error) { return nil, nil }

// Set does nothing.
func (i *Icon) Set(s State) {}

// Close does nothing.
func (i *Icon) Close() {}

// OwnsConsole reports false outside Windows.
func OwnsConsole() bool { return false }

// RelaunchInConhost does nothing outside Windows.
func RelaunchInConhost() bool { return false }

// Pauses returns nil outside Windows: there is no way to pause.
func Pauses(ctx context.Context) <-chan struct{} { return nil }
