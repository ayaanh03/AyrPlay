//go:build !windows

package main

import "os/exec"

func hideConsole(cmd *exec.Cmd) {}

func displayCount() int { return 1 }
