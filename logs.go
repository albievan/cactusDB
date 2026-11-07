package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
)

// ☰ → Logs shows cactusdb's log in the web UI: the log file when there is
// one (--runas background, --logfile), otherwise the lines logged since this
// cactusdb started, kept in memory; and the crash log.

const (
	logMemLines  = 5000
	logMaxLines  = 20000
	logTailBytes = 8 << 20 // the part of a log file read for its last lines
)

// logMemory keeps the last logMemLines lines written to the log.
type logMemory struct {
	mu    sync.Mutex
	lines []string
}

var logMem = &logMemory{}

func (m *logMemory) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		m.lines = append(m.lines, l)
	}
	if over := len(m.lines) - logMemLines; over > 0 {
		m.lines = append([]string(nil), m.lines[over:]...)
	}
	return len(p), nil
}

func (m *logMemory) last(n int) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.lines[max(0, len(m.lines)-n):]...)
}

// tailLines returns the last n lines of the file at path.
func tailLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	off := max(0, st.Size()-logTailBytes)
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if off > 0 { // the first line read is probably cut off
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	return lines[max(0, len(lines)-n):], nil
}

func (s *server) handleLogs(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if n <= 0 {
		n = 1000
	}
	n = min(n, logMaxLines)
	out := map[string]any{"source": "memory", "path": "", "max": logMaxLines}
	var lines []string
	if s.logPath != "" {
		l, err := tailLines(s.logPath, n)
		if err == nil {
			lines = l
			out["source"], out["path"] = "file", s.logPath
		} else {
			out["fileError"] = err.Error()
		}
	}
	if out["source"] == "memory" {
		lines = logMem.last(n)
	}
	out["lines"] = lines
	crash := map[string]any{"path": s.crashPath}
	if l, err := tailLines(s.crashPath, 300); err == nil {
		crash["lines"] = l
	}
	out["crash"] = crash
	writeJSON(w, 200, out)
}
