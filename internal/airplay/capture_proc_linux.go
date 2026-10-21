//go:build linux

package airplay

import (
	"os/exec"
	"syscall"
)

// configureChildLifetime ensures a capture child (GStreamer) cannot outlive
// doubletake: Linux delivers SIGKILL to the child when its creating OS thread
// exits, which startGStreamerCommand keeps locked until Wait returns.
func configureChildLifetime(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
