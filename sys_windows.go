package main

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

// enableANSI turns on escape-sequence processing in a Windows console so the
// progress bars can redraw in place. It reports false on consoles without it.
func enableANSI(f *os.File) bool {
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}

// detachedProcess starts the background web server without a console window,
// in its own process group, so closing the terminal doesn't stop it.
func detachedProcess() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
}

const stopHint = "taskkill /PID %d /F"

// restartProcess starts exe (after an upgrade) as a new process: Windows
// can't replace a running one. A background cactusdb starts it detached,
// logging to the same file; a cactusdb in a console waits for it there, so
// the console stays in use until it stops. It only returns when exe can't be
// started.
func restartProcess(exe string, args, env []string, childLog string) error {
	cmd := exec.Command(exe, args...)
	cmd.Env = env
	if childLog != "" {
		f, err := os.OpenFile(childLog, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		cmd.Stdout, cmd.Stderr = f, f
		cmd.SysProcAttr = detachedProcess()
		if err := cmd.Start(); err != nil {
			return err
		}
		os.WriteFile(pidFile(childLog), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644)
		os.Exit(0)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	err := cmd.Wait()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		os.Exit(ee.ExitCode())
	}
	os.Exit(0)
	return nil
}
