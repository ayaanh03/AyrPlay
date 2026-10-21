//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideConsole keeps the doubletake.exe console subprocess from flashing a window
// when launched by the GUI tray. CREATE_NO_WINDOW (0x08000000) runs it windowless.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

// displayCount returns the number of display monitors (user32 GetSystemMetrics
// SM_CMONITORS). doubletake's -monitor index is 0-based, so valid values are
// 0..displayCount()-1.
func displayCount() int {
	r, _, _ := syscall.NewLazyDLL("user32.dll").NewProc("GetSystemMetrics").Call(80) // SM_CMONITORS
	if int(r) < 1 {
		return 1
	}
	return int(r)
}
