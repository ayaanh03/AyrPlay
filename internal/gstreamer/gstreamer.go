// Package gstreamer finds the GStreamer runtime AyrPlay shells out to and checks
// that the elements each streaming mode needs are installed. It lets the single
// AyrPlay executable work right after the official Windows installer runs, with
// no PATH editing or sign-out, and backs the `ayrplay doctor` report.
package gstreamer

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// DownloadURL is the official GStreamer download page.
const DownloadURL = "https://gstreamer.freedesktop.org/download/"

// launchTool is the program every capture pipeline runs.
const launchTool = "gst-launch-1.0"

// candidateDirs lists directories that may hold the GStreamer tools, best
// first: a bundled runtime next to the executable, the install roots the
// official MSVC/MinGW installers record in the environment, and their default
// install locations. Only Windows has well-known locations; elsewhere the
// package manager puts GStreamer on PATH.
func candidateDirs(goos string, getenv func(string) string, exeDir string) []string {
	if goos != "windows" {
		return nil
	}
	var dirs []string
	if exeDir != "" {
		dirs = append(dirs, filepath.Join(exeDir, "gstreamer", "bin"))
	}
	for _, name := range []string{
		"GSTREAMER_1_0_ROOT_MSVC_X86_64",
		"GSTREAMER_1_0_ROOT_MSVC_ARM64",
		"GSTREAMER_1_0_ROOT_X86_64",
		"GSTREAMER_ROOT_X86_64",
	} {
		if root := strings.TrimSpace(getenv(name)); root != "" {
			dirs = append(dirs, filepath.Join(root, "bin"))
		}
	}
	// Since 1.28 the installer defaults to a per-user install under
	// %LOCALAPPDATA%\Programs (system-wide: %ProgramFiles%) and may not record
	// its root in the environment; older MSI installers used C:\gstreamer.
	var roots []string
	if local := getenv("LOCALAPPDATA"); local != "" {
		roots = append(roots, filepath.Join(local, "Programs", "gstreamer", "1.0"))
	}
	programFiles := getenv("ProgramFiles")
	if programFiles == "" {
		programFiles = `C:\Program Files`
	}
	roots = append(roots, filepath.Join(programFiles, "gstreamer", "1.0"), `C:\gstreamer\1.0`)
	for _, root := range roots {
		for _, abi := range []string{"msvc_x86_64", "msvc_arm64"} {
			dirs = append(dirs, filepath.Join(root, abi, "bin"))
		}
	}
	return dirs
}

// locate returns the first candidate directory holding gst-launch-1.0.
func locate(goos string, getenv func(string) string, exeDir string, exists func(string) bool) string {
	tool := launchTool
	if goos == "windows" {
		tool += ".exe"
	}
	for _, dir := range candidateDirs(goos, getenv, exeDir) {
		if exists(filepath.Join(dir, tool)) {
			return dir
		}
	}
	return ""
}

// EnsureOnPath makes the GStreamer tools resolvable for this process and every
// child it starts. When gst-launch-1.0 is not already on PATH it looks in the
// known install locations and prepends the first match to PATH. It returns the
// directory it added, or "" when nothing was needed or nothing was found.
func EnsureOnPath() string {
	if _, err := exec.LookPath(launchTool); err == nil {
		return ""
	}
	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	dir := locate(runtime.GOOS, os.Getenv, exeDir, fileExists)
	if dir == "" {
		return ""
	}
	_ = os.Setenv("PATH", prependPath(dir, os.Getenv("PATH")))
	return dir
}

func prependPath(dir, path string) string {
	if path == "" {
		return dir
	}
	return dir + string(os.PathListSeparator) + path
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
