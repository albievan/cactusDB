package main

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
)

// The log and the crash log start a new file every day: cactusdb.log is
// today's, earlier days are renamed to cactusdb-YYYY-MM-DD.log (the day of
// the lines in it). Files older than logKeepDays are deleted. ☰ → Logs shows
// any day and downloads them all as a .zip.

const (
	logKeepDays = 30
	dayFormat   = "2006-01-02"
)

func today() string { return time.Now().Format(dayFormat) }

// datedName is the file path of day: /x/cactusdb.log → /x/cactusdb-2026-11-06.log.
func datedName(path, day string) string {
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext) + "-" + day + ext
}

// logFileFor is the file holding day's lines of the log at path.
func logFileFor(path, day string) string {
	if day == "" || day == today() {
		return path
	}
	return datedName(path, day)
}

// rotateIfOld renames path to its dated name when it was last written to
// before today; it is appended to a dated file that already exists.
func rotateIfOld(path string) {
	st, err := os.Stat(path)
	if err != nil || st.Size() == 0 {
		return
	}
	if day := st.ModTime().Format(dayFormat); day != today() {
		moveLog(path, datedName(path, day))
		pruneLogs(path)
	}
}

func moveLog(from, to string) {
	if !fileExists(to) {
		os.Rename(from, to)
		return
	}
	in, err := os.Open(from)
	if err != nil {
		return
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, err = io.Copy(out, in)
	if out.Close() == nil && err == nil {
		os.Remove(from)
	}
}

var datedLogRe = regexp.MustCompile(`-(\d{4}-\d\d-\d\d)$`)

// logDays lists the days there is a log file for, newest first: today when
// path exists, and every dated file.
func logDays(path string) []string {
	var days []string
	if fileExists(path) {
		days = append(days, today())
	}
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(filepath.Base(path), ext)
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ext)
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ext) || !strings.HasPrefix(name, stem+"-") {
			continue
		}
		if m := datedLogRe.FindStringSubmatch(name); m != nil && name == stem+"-"+m[1] && m[1] != today() {
			days = append(days, m[1])
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	return days
}

// pruneLogs deletes the dated files of path older than logKeepDays.
func pruneLogs(path string) {
	oldest := time.Now().AddDate(0, 0, -logKeepDays).Format(dayFormat)
	for _, day := range logDays(path) {
		if day < oldest {
			os.Remove(datedName(path, day))
		}
	}
}

// dailyLog is a log file that is renamed to its dated name when the day
// changes, and started again.
type dailyLog struct {
	mu   sync.Mutex
	path string
	f    *os.File
	day  string
}

func openDailyLog(path string) (*dailyLog, error) {
	rotateIfOld(path)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &dailyLog{path: path, f: f, day: today()}, nil
}

func (d *dailyLog) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t := today(); t != d.day {
		d.f.Close()
		moveLog(d.path, datedName(d.path, d.day))
		f, err := os.OpenFile(d.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return 0, err
		}
		d.f, d.day = f, t
		pruneLogs(d.path)
	}
	return d.f.Write(p)
}

func (d *dailyLog) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.f.Close()
}

// rotateCrashLogDaily starts a new crash log after every midnight when the
// current one has anything in it. The Go runtime writes to the file given to
// debug.SetCrashOutput, so that is set again to the new file.
func rotateCrashLogDaily(path string) {
	for {
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 1, 0, now.Location())
		time.Sleep(time.Until(next))
		st, err := os.Stat(path)
		if err != nil || st.Size() == 0 {
			continue
		}
		rotateIfOld(path)
		if f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600); err == nil {
			debug.SetCrashOutput(f, debug.CrashOptions{})
			f.Close()
		}
	}
}
