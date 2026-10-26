package main

import (
	"os"
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
