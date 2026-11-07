//go:build !windows

package main

import (
	"os"
	"syscall"
)

func enableANSI(*os.File) bool { return true }

// detachedProcess starts the background web server in a new session, so it
// keeps running after the terminal is closed.
func detachedProcess() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

const stopHint = "kill %d"

// restartProcess replaces this process with exe (after an upgrade): the same
// process id, terminal or log file, and service manager. It only returns
// when that fails.
func restartProcess(exe string, args, env []string, childLog string) error {
	return syscall.Exec(exe, append([]string{exe}, args...), env)
}
