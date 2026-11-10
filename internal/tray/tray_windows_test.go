package tray

import "testing"

func TestIconStates(t *testing.T) {
	i, err := Start("owysync test", func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	for s := range len(circleFiles) {
		i.Set(State(s))
	}
}
