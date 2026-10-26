package main

import (
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// backgroundEnv is set, to the log file path, in the copy of cactusdb that
// --runas background starts. Its output already goes to that file.
const backgroundEnv = "CACTUSDB_BACKGROUND_LOG"

// pidFile is where a background server's process id is kept: next to the
// log file, with the extension .pid.
func pidFile(logFile string) string {
	return strings.TrimSuffix(logFile, filepath.Ext(logFile)) + ".pid"
}

// startBackground runs this program again, detached from the terminal, with
// the same options except --runas, and its output appended to logFile. It
// returns once the new server answers on probeURL, or with an error (and
// the end of the log) if it stopped instead.
func startBackground(logFile, probeURL string, urls []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("cannot open log file: %w", err)
	}
	defer f.Close()
	start, _ := f.Seek(0, 2)

	cmd := exec.Command(exe, withoutFlag(os.Args[1:], "runas")...)
	cmd.Env = append(os.Environ(), backgroundEnv+"="+logFile)
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = detachedProcess()
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	deadline := time.After(20 * time.Second)
	client := &http.Client{Timeout: time.Second}
	for {
		select {
		case <-exited:
			return fmt.Errorf("the background server stopped:\n%s", logTail(logFile, start))
		case <-deadline:
			cmd.Process.Kill()
			return fmt.Errorf("the background server did not start within 20 seconds:\n%s", logTail(logFile, start))
		case <-time.After(200 * time.Millisecond):
		}
		resp, err := client.Get(probeURL)
		if err != nil {
			continue
		}
		resp.Body.Close()
		break
	}

	pid := cmd.Process.Pid
	os.WriteFile(pidFile(logFile), []byte(strconv.Itoa(pid)+"\n"), 0o644)
	fmt.Printf("cactusdb %s is running in the background (process id %d)\n", version, pid)
	for _, u := range urls {
		fmt.Printf("  open %s\n", u)
	}
	fmt.Printf("  log file: %s\n  stop it with: "+stopHint+"\n", logFile, pid)
	return nil
}

// withoutFlag removes --name value, --name=value and their single-dash forms.
func withoutFlag(args []string, name string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := strings.TrimLeft(args[i], "-")
		if !strings.HasPrefix(args[i], "-") || args[i] == "--" {
			out = append(out, args[i])
			continue
		}
		if a == name {
			i++ // skip the value
			continue
		}
		if strings.HasPrefix(a, name+"=") {
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// logTail returns what was written to the log file after offset, at most the
// last 20 lines.
func logTail(logFile string, offset int64) string {
	f, err := os.Open(logFile)
	if err != nil {
		return ""
	}
	defer f.Close()
	f.Seek(offset, 0)
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, "  "+sc.Text())
		if len(lines) > 20 {
			lines = lines[1:]
		}
	}
	if len(lines) == 0 {
		return "  (nothing was logged)"
	}
	return strings.Join(lines, "\n")
}

var errBackgroundCLI = errors.New("--runas background is for the web UI; it can't be combined with --dbtype/--dbtable/--showtable/--exportddl")
