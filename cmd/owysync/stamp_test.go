package main

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

func TestStampWriter(t *testing.T) {
	var out bytes.Buffer
	sw := newStampWriter(&out)
	sec := 0
	sw.now = func() time.Time { sec++; return time.Date(2026, 1, 2, 10, 25, sec, 0, time.Local) }

	fmt.Fprintln(sw, "one")
	fmt.Fprint(sw, "two\nthree ")
	fmt.Fprintln(sw, "continued")
	fmt.Fprint(sw, "")

	want := "[10:25:01] one\n[10:25:02] two\n[10:25:03] three continued\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}
