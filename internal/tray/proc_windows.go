//go:build windows

package tray

import (
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideConsole keeps the console-subsystem sender from flashing a window when
// launched by the tray. CREATE_NO_WINDOW (0x08000000) runs it windowless.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

// displayCount returns the number of display monitors (user32 GetSystemMetrics
// SM_CMONITORS). The sender's -monitor index is 0-based, so valid values are
// 0..displayCount()-1.
func displayCount() int {
	r, _, _ := syscall.NewLazyDLL("user32.dll").NewProc("GetSystemMetrics").Call(80) // SM_CMONITORS
	if int(r) < 1 {
		return 1
	}
	return int(r)
}

// singleInstanceMutex names the per-session mutex that marks a running tray.
const singleInstanceMutex = `Local\AyrPlayTray`

// acquireSingleInstance reports whether this is the only tray in the user's
// session. The returned release closes the mutex handle; Windows also drops it
// when the process exits.
func acquireSingleInstance() (release func(), ok bool) {
	name, err := windows.UTF16PtrFromString(singleInstanceMutex)
	if err != nil {
		return func() {}, true
	}
	h, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
		return func() {}, false
	}
	if err != nil {
		// Failing open is better than refusing to start.
		return func() {}, true
	}
	return func() { _ = windows.CloseHandle(h) }, true
}

// openInShell opens a folder in Explorer or a URL in the default browser.
func openInShell(target string) {
	if strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "http://") {
		_ = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", target).Start()
		return
	}
	_ = exec.Command("explorer.exe", target).Start()
}
