package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

type streamBuffer struct {
	mu        sync.Mutex
	w         io.Writer
	buf       strings.Builder
	lastFlush time.Time
	active    bool
}

func newStreamBuffer(w io.Writer) *streamBuffer {
	return &streamBuffer{w: w}
}

func (s *streamBuffer) start() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.buf.Reset()
	s.active = true
	s.lastFlush = time.Now()
	s.mu.Unlock()
}

func (s *streamBuffer) push(delta string) {
	if s == nil || delta == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		s.active = true
	}
	s.buf.WriteString(delta)
	now := time.Now()
	if strings.Contains(delta, "\n") || s.buf.Len() >= 96 || now.Sub(s.lastFlush) >= 45*time.Millisecond {
		s.flushLocked()
	}
}

func (s *streamBuffer) finish() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushLocked()
	s.active = false
}

func (s *streamBuffer) flushLocked() {
	if s.buf.Len() == 0 {
		return
	}
	fmt.Fprint(s.w, s.buf.String())
	flush(s.w)
	s.buf.Reset()
	s.lastFlush = time.Now()
}
