//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// startTrayDetached relaunches this executable as "ayrplay tray" in its own
// session. run only calls it on Windows today; it exists so the dispatcher
// builds everywhere.
func startTrayDetached(self string) error {
	cmd := exec.Command(self, "tray")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
