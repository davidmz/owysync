// Package sound plays the chime that marks a finished sync.
package sound

import (
	_ "embed"
	"unsafe"

	"golang.org/x/sys/windows"
)

//go:embed chime.wav
var chime []byte

var playSound = windows.NewLazySystemDLL("winmm.dll").NewProc("PlaySoundW")

const (
	sndAsync     = 0x0001
	sndNoDefault = 0x0002
	sndMemory    = 0x0004
)

// Play plays the chime. With wait it returns when the chime ends (a process about to exit),
// otherwise at once, and a newer call cuts the previous chime short. It reports whether the
// chime started; there is nothing to do about a failure (no audio device).
func Play(wait bool) bool {
	flags := uintptr(sndNoDefault | sndMemory)
	if !wait {
		flags |= sndAsync
	}
	ok, _, _ := playSound.Call(uintptr(unsafe.Pointer(&chime[0])), 0, flags)
	return ok != 0
}
