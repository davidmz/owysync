package syncer

import "testing"

func TestBatchScopes(t *testing.T) {
	dirty := map[string]bool{"a": true, "a/b/c": false, "x/y": false, "x/y/z": true}
	for p, want := range map[string]bool{"a": false, "a/b/c": true, "x/y": false, "x/y/z": false, "q": false} {
		if got := covered(p, dirty); got != want {
			t.Errorf("covered(%q) = %v, want %v", p, got, want)
		}
	}
	if !covered("a", map[string]bool{"": true}) {
		t.Error("an overflow must cover every path")
	}

	scopes := map[string]bool{"a": true, "x/y": true}
	for p, want := range map[string]bool{"a": true, "a/b": true, "ab": false, "x": false, "x/y/z": true, "": false} {
		if got := inScope(p, scopes); got != want {
			t.Errorf("inScope(%q) = %v, want %v", p, got, want)
		}
	}
	if !inScope("any/path", map[string]bool{"": true}) {
		t.Error(`scope "" must hold every path`)
	}
}
