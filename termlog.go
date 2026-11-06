package main

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// The terminal cactusdb runs in can stop reading its output: an SSH session
// whose network dropped, a paused or scrolled-back terminal, a pipe nobody
// reads. A write to it then blocks, and as every request logs a line, every
// request would hang while the page still loads ("Loading …" forever). Lines
// for the terminal are therefore queued and written by one goroutine; when
// the queue is full they're dropped and counted, never waited for.

const termQueueLines = 4096

type termWriter struct {
	out     io.Writer
	queue   chan []byte
	pending atomic.Int64 // queued, not yet written
	dropped atomic.Int64
}

func newTermWriter(out io.Writer) *termWriter {
	t := &termWriter{out: out, queue: make(chan []byte, termQueueLines)}
	go t.run()
	return t
}

func (t *termWriter) Write(p []byte) (int, error) {
	t.pending.Add(1)
	select {
	case t.queue <- append([]byte(nil), p...):
	default:
		t.pending.Add(-1)
		t.dropped.Add(1)
	}
	return len(p), nil
}

func (t *termWriter) run() {
	for b := range t.queue {
		if n := t.dropped.Swap(0); n > 0 {
			fmt.Fprintf(t.out, "(%d log lines were dropped: the terminal didn't read cactusdb's output)\n", n)
		}
		t.out.Write(b) // a closed terminal fails, and that's fine
		t.pending.Add(-1)
	}
}

// flush waits at most d for the queued lines to be written, e.g. before exiting.
func (t *termWriter) flush(d time.Duration) {
	for end := time.Now().Add(d); t.pending.Load() > 0 && time.Now().Before(end); {
		time.Sleep(10 * time.Millisecond)
	}
}
