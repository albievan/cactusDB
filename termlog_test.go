package main

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

// blockedWriter is a terminal that doesn't read: every write waits for open.
type blockedWriter struct {
	open chan struct{}
	mu   sync.Mutex
	buf  bytes.Buffer
}

func (b *blockedWriter) Write(p []byte) (int, error) {
	<-b.open
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func TestTermWriterNeverBlocks(t *testing.T) {
	out := &blockedWriter{open: make(chan struct{})}
	tw := newTermWriter(out)
	start := time.Now()
	for i := 0; i < termQueueLines*3; i++ {
		tw.Write([]byte("a line\n"))
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("writes to a terminal that doesn't read took %s", d)
	}
	close(out.open) // the terminal reads again
	tw.flush(5 * time.Second)
	tw.Write([]byte("after\n"))
	tw.flush(5 * time.Second)
	out.mu.Lock()
	got := out.buf.String()
	out.mu.Unlock()
	if !strings.Contains(got, "log lines were dropped") || !strings.HasSuffix(got, "after\n") {
		t.Fatalf("want a dropped-lines note and the last line, got %d bytes ending %q", len(got), got[max(0, len(got)-120):])
	}
}
