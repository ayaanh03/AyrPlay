//go:build !windows

package tray

import (
	"os/exec"
	"runtime"
)

func hideConsole(cmd *exec.Cmd) {}

func displayCount() int { return 1 }

// acquireSingleInstance is a no-op off Windows, where the tray is a
// development convenience rather than the primary interface.
func acquireSingleInstance() (release func(), ok bool) { return func() {}, true }

// openInShell opens a folder or URL with the desktop's default handler.
func openInShell(target string) {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	_ = exec.Command(opener, target).Start()
}
