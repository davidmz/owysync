package main

import (
	"bytes"
	"io"
	"sync"
	"time"
)

// stampWriter prefixes every line with the local time, "[15:04:05] ". The time is taken
// when the first byte of the line is written. It is safe for concurrent use.
type stampWriter struct {
	mu      sync.Mutex
	w       io.Writer
	now     func() time.Time
	midLine bool // the last write did not end with a newline
}

func newStampWriter(w io.Writer) *stampWriter { return &stampWriter{w: w, now: time.Now} }

// Write implements io.Writer.
func (s *stampWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var buf []byte
	for rest := p; len(rest) > 0; {
		if !s.midLine {
			buf = s.now().AppendFormat(append(buf, '['), "15:04:05")
			buf = append(buf, "] "...)
		}
		line := rest
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			line = rest[:i+1]
		}
		buf, rest = append(buf, line...), rest[len(line):]
		s.midLine = line[len(line)-1] != '\n'
	}
	if _, err := s.w.Write(buf); err != nil {
		return 0, err
	}
	return len(p), nil
}
