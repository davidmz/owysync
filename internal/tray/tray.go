// Package tray shows the sync state as an icon in the Windows notification area.
package tray

// State is what the tray icon shows.
type State int

// The states, each drawn as a circle of its own color inside the arrows.
const (
	Starting State = iota // gray: the state is not known yet
	Idle                  // green: connected, waiting for changes
	Syncing               // bright green: a sync is running
	Failed                // red: an error, or the connection is lost
)

func (s State) String() string {
	return [...]string{"starting", "idle", "syncing", "error"}[s]
}
