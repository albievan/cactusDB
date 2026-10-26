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
