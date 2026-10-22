//go:build windows

package main

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// startTrayDetached relaunches this executable as "ayrplay tray" with no
// console, so a double-click leaves only the tray icon: the console window
// Windows opened for the launch closes as soon as this process exits.
func startTrayDetached(self string) error {
	cmd := exec.Command(self, "tray")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
