package main

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The Logs tab shows cactusdb's log in the web UI: the log file of any day
// when there is one (--runas background, --logfile; see logrotate.go),
// otherwise the lines logged since this cactusdb started, kept in memory;
// and the crash log. It updates live and downloads every file as a .zip.

const (
	logMemLines  = 5000
	logMaxLines  = 20000
	logTailBytes = 8 << 20 // the part of a log file read for its last lines
)

// logMemory keeps the last logMemLines lines written to the log; total
// counts every line, so the live view can ask for what came after.
type logMemory struct {
	mu    sync.Mutex
	lines []string
	total int
}

var logMem = &logMemory{}

func (m *logMemory) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		m.lines = append(m.lines, l)
		m.total++
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

// since returns the lines after the first `after` ever written, and the new
// total; ok is false when some of them are no longer kept.
func (m *logMemory) since(after int) (lines []string, total int, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	first := m.total - len(m.lines) // the number of lines dropped
	if after < first || after > m.total {
		return nil, m.total, false
	}
	return append([]string(nil), m.lines[after-first:]...), m.total, true
}

// tailLines returns the last n lines of the file at path, and its size.
func tailLines(path string, n int) ([]string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	off := max(0, st.Size()-logTailBytes)
	b, err := readFrom(f, off)
	if err != nil {
		return nil, 0, err
	}
	if off > 0 { // the first line read is probably cut off
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	lines := splitLines(b)
	return lines[max(0, len(lines)-n):], st.Size(), nil
}

func readFrom(f *os.File, off int64) ([]byte, error) {
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

func splitLines(b []byte) []string {
	s := strings.TrimRight(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// linesAfter returns the whole lines added to the file at path after byte
// off, and the offset to ask from next time; reset when the file is shorter
// than off (a new day started a new file).
func linesAfter(path string, off int64) (lines []string, end int64, reset bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 0, false, err
	}
	if st.Size() < off {
		return nil, 0, true, nil
	}
	b, err := readFrom(f, off)
	if err != nil {
		return nil, 0, false, err
	}
	if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
		b = b[:i+1] // a line still being written comes next time
	} else {
		b = nil
	}
	return splitLines(b), off + int64(len(b)), false, nil
}

// handleLogs returns the log: kind=log (default) or crash, date=YYYY-MM-DD
// (default today), lines=N (the last N), or after=<end of the previous
// answer> for only the lines added since (the live view).
func (s *server) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	n, _ := strconv.Atoi(q.Get("lines"))
	if n <= 0 {
		n = 1000
	}
	n = min(n, logMaxLines)
	kind, day := q.Get("kind"), q.Get("date")
	if day != "" && !datedDayRe.MatchString(day) {
		writeErr(w, 400, errors.New("date must be YYYY-MM-DD"))
		return
	}
	if day == "" {
		day = today()
	}
	out := map[string]any{"kind": "log", "date": day, "max": logMaxLines, "today": today(),
		"days": []string{today()}, "crashDays": logDays(s.crashPath), "crashPath": s.crashPath}
	base := s.logPath
	if kind == "crash" {
		out["kind"], base = "crash", s.crashPath
	}
	after, hasAfter := int64(-1), q.Get("after") != ""
	if hasAfter {
		after, _ = strconv.ParseInt(q.Get("after"), 10, 64)
	}

	if base == "" { // no log file: the lines kept in memory, today only
		out["source"] = "memory"
		if hasAfter {
			if lines, total, ok := logMem.since(int(after)); ok {
				out["lines"], out["end"], out["append"] = lines, total, true
				writeJSON(w, 200, out)
				return
			}
		}
		logMem.mu.Lock()
		total := logMem.total
		logMem.mu.Unlock()
		out["lines"], out["end"] = logMem.last(n), total
		writeJSON(w, 200, out)
		return
	}

	out["source"] = "file"
	if kind != "crash" {
		out["days"] = logDays(base)
	}
	path := logFileFor(base, day)
	out["path"] = path
	if !fileExists(path) {
		out["lines"], out["end"] = []string{}, 0
		writeJSON(w, 200, out)
		return
	}
	if hasAfter && after >= 0 {
		lines, end, reset, err := linesAfter(path, after)
		if err == nil && !reset {
			out["lines"], out["end"], out["append"] = lines, end, true
			writeJSON(w, 200, out)
			return
		}
	}
	lines, size, err := tailLines(path, n)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	out["lines"], out["end"] = lines, size
	writeJSON(w, 200, out)
}

var datedDayRe = regexp.MustCompile(`^\d{4}-\d\d-\d\d$`)

// handleLogsZip downloads every log and crash log file, compressed as much
// as zip can.
func (s *server) handleLogsZip(w http.ResponseWriter, r *http.Request) {
	var files []string
	for _, base := range []string{s.logPath, s.crashPath} {
		if base == "" {
			continue
		}
		for _, day := range logDays(base) {
			files = append(files, logFileFor(base, day))
		}
	}
	name := "cactusdb-logs_" + time.Now().Format("20060102-150405") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	zw := zip.NewWriter(w)
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestCompression)
	})
	if s.logPath == "" { // no log file: what is in memory
		if out, err := zw.Create("cactusdb.log"); err == nil {
			out.Write([]byte(strings.Join(logMem.last(logMemLines), "\n") + "\n"))
		}
	}
	for _, p := range files {
		in, err := os.Open(p)
		if err != nil {
			continue
		}
		st, _ := in.Stat()
		h := &zip.FileHeader{Name: filepath.Base(p), Method: zip.Deflate}
		if st != nil {
			h.Modified = st.ModTime()
		}
		if out, err := zw.CreateHeader(h); err == nil {
			io.Copy(out, in)
		}
		in.Close()
	}
	if err := zw.Close(); err != nil {
		log.Printf("logs zip: %v", err)
		return
	}
	logAction(r, "downloaded the logs (%d file(s)) as %s", len(files), name)
	s.event(r, "export", false, "downloaded the logs (%d file(s)) as %s", len(files), name)
}
