package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// A cactusdb that disappears leaves the page with only "no answer". These
// record why: a fatal Go error is written to cactusdb-crash.log next to the
// connection file, and a marker file shows on the next start that the
// previous run never shut down: killed, most often by the system for lack of
// memory, or the machine or container restarted.

const crashLogName = "cactusdb-crash.log"

// trackRun starts recording crashes of the web server listening on port, in
// dir. It returns what happened to the previous run ("" when it stopped
// normally) and the function to call on a normal stop.
func trackRun(dir string, port int) (previous string, stopped func()) {
	crashPath := filepath.Join(dir, crashLogName)
	if f, err := os.OpenFile(crashPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600); err != nil {
		log.Printf("warning: cannot open %s: %v", crashPath, err)
	} else if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		log.Printf("warning: cannot record crashes in %s: %v", crashPath, err)
	} else {
		f.Close() // SetCrashOutput keeps its own copy
	}

	marker := filepath.Join(dir, fmt.Sprintf("cactusdb-%d.running", port))
	if b, err := os.ReadFile(marker); err == nil {
		previous = previousRun(string(b), crashPath)
	}
	run := fmt.Sprintf("pid=%d\nstarted=%s\nversion=%s\n", os.Getpid(), processStart.Format(time.RFC3339Nano), version)
	if err := os.WriteFile(marker, []byte(run), 0o600); err != nil {
		log.Printf("warning: cannot write %s: %v", marker, err)
	}
	return previous, func() { os.Remove(marker) }
}

// previousRun explains a run whose marker was left behind.
func previousRun(marker, crashPath string) string {
	m := map[string]string{}
	for _, l := range strings.Split(marker, "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			m[k] = v
		}
	}
	pid, _ := strconv.Atoi(m["pid"])
	started, _ := time.Parse(time.RFC3339, m["started"])
	what := fmt.Sprintf("the previous cactusdb (process %d, v%s, started %s)", pid, m["version"], started.Local().Format("2006-01-02 15:04:05"))
	if st, err := os.Stat(crashPath); err == nil && !started.IsZero() && st.ModTime().After(started) {
		if last := lastCrashLine(crashPath); last != "" {
			return what + " crashed: " + last + " (the details are in " + crashPath + ")"
		}
	}
	return what + " stopped without shutting down: it was killed, most often by the system because the computer ran out of memory " +
		"(see `dmesg | grep -i -E 'killed process|out of memory'`; in a container, a memory limit), or the machine or container restarted"
}

// lastCrashLine returns the last "panic:" or "fatal error:" line in the crash
// log: the one of the most recent crash.
func lastCrashLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	last := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if l := sc.Text(); strings.HasPrefix(l, "panic: ") || strings.HasPrefix(l, "fatal error: ") {
			last = l
		}
	}
	if len(last) > 300 {
		last = last[:300] + "…"
	}
	return last
}
