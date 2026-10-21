//go:build !linux

package airplay

import "os/exec"

// configureChildLifetime is a no-op on platforms without a parent-death signal.
// On Windows the capture child is reaped by cancelling the command context in
// startGStreamerCommand; a Job Object could later enforce hard-kill on crash.
func configureChildLifetime(cmd *exec.Cmd) {}
