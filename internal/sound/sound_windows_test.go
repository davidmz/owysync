package sound

import "testing"

func TestPlay(t *testing.T) {
	if !Play(true) {
		t.Skip("no audio device")
	}
}
