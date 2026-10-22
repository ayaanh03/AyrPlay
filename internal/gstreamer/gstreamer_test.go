package gstreamer

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func envMap(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestCandidateDirsWindows(t *testing.T) {
	dirs := candidateDirs("windows", envMap(map[string]string{
		"GSTREAMER_1_0_ROOT_MSVC_X86_64": `D:\gst\1.0\msvc_x86_64\`,
		"LOCALAPPDATA":                   `C:\Users\me\AppData\Local`,
		"ProgramFiles":                   `C:\Program Files`,
	}), `C:\Users\me\Downloads`)
	want := []string{
		filepath.Join(`C:\Users\me\Downloads`, "gstreamer", "bin"),
		filepath.Join(`D:\gst\1.0\msvc_x86_64\`, "bin"),
		filepath.Join(`C:\Users\me\AppData\Local`, "Programs", "gstreamer", "1.0", "msvc_x86_64", "bin"),
	}
	if len(dirs) < len(want) {
		t.Fatalf("candidateDirs = %q, want at least %q first", dirs, want)
	}
	for i, w := range want {
		if dirs[i] != w {
			t.Fatalf("candidateDirs[%d] = %q, want %q (all: %q)", i, dirs[i], w, dirs)
		}
	}
	for _, w := range []string{
		filepath.Join(`C:\Program Files`, "gstreamer", "1.0", "msvc_x86_64", "bin"),
		filepath.Join(`C:\gstreamer\1.0`, "msvc_x86_64", "bin"),
	} {
		if !contains(dirs, w) {
			t.Fatalf("install location %q missing from %q", w, dirs)
		}
	}
}

func TestCandidateDirsOtherPlatformsUsePath(t *testing.T) {
	if dirs := candidateDirs("linux", envMap(nil), "/opt/ayrplay"); dirs != nil {
		t.Fatalf("linux candidates = %q, want none (package managers use PATH)", dirs)
	}
}

func TestLocatePrefersBundledThenInstallerRoot(t *testing.T) {
	exeDir := `C:\AyrPlay`
	root := `D:\gst`
	env := envMap(map[string]string{"GSTREAMER_1_0_ROOT_MSVC_X86_64": root})
	bundled := filepath.Join(exeDir, "gstreamer", "bin", "gst-launch-1.0.exe")
	installed := filepath.Join(root, "bin", "gst-launch-1.0.exe")

	has := func(paths ...string) func(string) bool {
		return func(p string) bool { return contains(paths, p) }
	}
	if got := locate("windows", env, exeDir, has(bundled, installed)); got != filepath.Dir(bundled) {
		t.Fatalf("locate = %q, want bundled runtime first", got)
	}
	if got := locate("windows", env, exeDir, has(installed)); got != filepath.Dir(installed) {
		t.Fatalf("locate = %q, want installer root", got)
	}
	if got := locate("windows", env, exeDir, has()); got != "" {
		t.Fatalf("locate with nothing installed = %q, want empty", got)
	}
}

func TestPrependPath(t *testing.T) {
	sep := string(os.PathListSeparator)
	if got := prependPath("new", ""); got != "new" {
		t.Fatalf("prependPath to empty = %q", got)
	}
	if got := prependPath("new", "a"+sep+"b"); got != "new"+sep+"a"+sep+"b" {
		t.Fatalf("prependPath = %q", got)
	}
}

func TestEnsureOnPathLeavesWorkingPathAlone(t *testing.T) {
	dir := t.TempDir()
	name := "gst-launch-1.0"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if added := EnsureOnPath(); added != "" {
		t.Fatalf("EnsureOnPath added %q although gst-launch-1.0 is already on PATH", added)
	}
	if os.Getenv("PATH") != dir {
		t.Fatalf("PATH changed to %q", os.Getenv("PATH"))
	}
}

// fakeEnv reports the listed elements as installed.
func fakeEnv(goos string, tools bool, elements ...string) Env {
	return Env{
		GOOS: goos,
		LookPath: func(name string) (string, error) {
			if !tools {
				return "", errors.New("not found")
			}
			return "/fake/" + name, nil
		},
		HasElement: func(name string) bool { return contains(elements, name) },
	}
}

var windowsComplete = []string{
	"wasapi2src", "d3d11screencapturesrc", "d3d11download", "audioconvert", "audioresample",
	"videoconvert", "videoscale", "nvh264enc", "h264parse", "nvh265enc", "h265parse",
	"rtph264pay", "audiobuffersplit", "rtpL16pay", "rtponviftimestamp", "rtpstreampay",
}

func TestDiagnoseCompleteWindowsInstall(t *testing.T) {
	report := Diagnose(fakeEnv("windows", true, windowsComplete...))
	if !report.Ready || report.Hint != "" {
		t.Fatalf("complete install not ready: %+v", report)
	}
	for _, mode := range []Mode{ModeMirror, ModeAudioOnly} {
		if missing := report.Missing(mode); len(missing) != 0 {
			t.Fatalf("%s missing %v on a complete install", mode, missing)
		}
	}
}

func TestDiagnoseAudioOnlyNeedsNoVideoPlugins(t *testing.T) {
	// A Typical install with WASAPI but without d3d11/NVENC can still stream to
	// a HomePod.
	report := Diagnose(fakeEnv("windows", true, "wasapi2src", "audioconvert", "audioresample"))
	if report.Ready {
		t.Fatal("report ready without any video plugins")
	}
	modes := map[Mode]ModeReport{}
	for _, mr := range report.Modes {
		modes[mr.Mode] = mr
	}
	if !modes[ModeAudioOnly].Ready {
		t.Fatalf("audio-only not ready: %+v", modes[ModeAudioOnly])
	}
	if modes[ModeMirror].Ready {
		t.Fatal("mirror ready without screen capture or an encoder")
	}
	missing := strings.Join(report.Missing(ModeMirror), ",")
	for _, want := range []string{"d3d11screencapturesrc", "nvh264enc or"} {
		if !strings.Contains(missing, want) {
			t.Fatalf("mirror missing = %q, want it to name %q", missing, want)
		}
	}
	if !strings.Contains(report.Hint, "Reinstall") || !strings.Contains(report.Hint, DownloadURL) {
		t.Fatalf("hint = %q, want advice to reinstall the runtime", report.Hint)
	}
}

func TestDiagnoseSoftwareEncoderSatisfiesMirror(t *testing.T) {
	elements := []string{"wasapi2src", "d3d11screencapturesrc", "d3d11download", "audioconvert", "audioresample", "videoconvert", "videoscale", "openh264enc", "h264parse"}
	report := Diagnose(fakeEnv("windows", true, elements...))
	if !report.Ready {
		t.Fatalf("openh264 install without optional plugins not ready: %v", report.Missing(ModeMirror))
	}
	for _, mr := range report.Modes {
		for _, c := range mr.Checks {
			if strings.Contains(c.Purpose, "H.264 video encoder") && c.Found != "openh264enc" {
				t.Fatalf("encoder check found %q, want openh264enc", c.Found)
			}
		}
	}
}

func TestDiagnoseWithoutGStreamer(t *testing.T) {
	asked := false
	env := fakeEnv("windows", false)
	env.HasElement = func(string) bool { asked = true; return true }
	report := Diagnose(env)
	if report.Ready || asked {
		t.Fatalf("missing tools: ready=%t inspected=%t, want not ready without running gst-inspect", report.Ready, asked)
	}
	if !strings.Contains(report.Hint, DownloadURL) || !strings.Contains(report.Hint, "no PATH changes") {
		t.Fatalf("hint = %q, want the download page and no-PATH reassurance", report.Hint)
	}
	if missing := report.Missing(ModeAudioOnly); len(missing) != 1 || !strings.Contains(missing[0], "gst-launch-1.0") {
		t.Fatalf("missing = %v, want only the GStreamer tools", missing)
	}
}

func TestDiagnoseLinuxAlternatives(t *testing.T) {
	report := Diagnose(fakeEnv("linux", true, "pulsesrc", "ximagesrc", "audioconvert", "audioresample", "videoconvert", "videoscale", "x264enc", "h264parse"))
	if !report.Ready {
		t.Fatalf("PulseAudio + X11 + x264 not ready: %v", report.Missing(ModeMirror))
	}
	if strings.Contains(report.Hint, "MSVC") {
		t.Fatalf("Linux hint mentions the Windows installer: %q", report.Hint)
	}
}

func TestReportJSONAndText(t *testing.T) {
	report := Diagnose(fakeEnv("windows", true, "wasapi2src", "audioconvert", "audioresample"))
	report.AddedToPath = `C:\Program Files\gstreamer\1.0\msvc_x86_64\bin`

	var js bytes.Buffer
	if err := report.WriteJSON(&js); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(js.Bytes(), &decoded); err != nil {
		t.Fatalf("doctor JSON does not parse: %v\n%s", err, js.String())
	}
	for _, key := range []string{"os", "gst_launch", "gst_inspect", "added_to_path", "modes", "ready", "hint"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("doctor JSON lacks %q: %s", key, js.String())
		}
	}

	var text strings.Builder
	report.WriteText(&text)
	for _, want := range []string{"mirror: NOT READY", "audio-only: ready", "[missing]", "[optional]", "found outside PATH"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("doctor text lacks %q:\n%s", want, text.String())
		}
	}
}

func TestRequirementsMatchCaptureElements(t *testing.T) {
	// The Windows capture path is wired to these exact element names; keep
	// the doctor's list in step with them.
	have := map[string]bool{}
	for _, r := range Requirements("windows") {
		for _, e := range r.AnyOf {
			have[e] = true
		}
	}
	for _, e := range []string{"wasapi2src", "d3d11screencapturesrc", "d3d11download", "nvh264enc", "nvh265enc", "h264parse", "h265parse", "rtph264pay", "rtponviftimestamp", "rtpstreampay"} {
		if !have[e] {
			t.Errorf("Windows requirements omit %s", e)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
