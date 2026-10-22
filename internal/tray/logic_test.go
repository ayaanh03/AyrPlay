package tray

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"doubletake/internal/airplay"
	"doubletake/internal/gstreamer"
)

func TestSettingsRoundTripAndDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "tray.json")
	if got := loadSettings(path); got != defaultSettings() {
		t.Fatalf("missing file loaded %+v, want defaults", got)
	}
	want := settings{Quality: quality4K, Buffer: bufferMovie, AudioOff: true, Debug: true, Monitor: 2}
	if err := want.save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := loadSettings(path); got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestLoadSettingsRepairsBadFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if got := loadSettings(write("corrupt.json", "{not json")); got != defaultSettings() {
		t.Fatalf("corrupt file loaded %+v, want defaults", got)
	}
	got := loadSettings(write("stale.json", `{"quality":"8k","buffer":"huge","monitor":-3,"debug":true}`))
	if want := (settings{Quality: quality1080p, Buffer: bufferSmooth, Debug: true}); got != want {
		t.Fatalf("stale file loaded %+v, want %+v", got, want)
	}
	// Fields missing from an older tray.json keep their defaults.
	got = loadSettings(write("partial.json", `{"audio_off":true}`))
	if want := (settings{Quality: quality1080p, Buffer: bufferSmooth, AudioOff: true}); got != want {
		t.Fatalf("partial file loaded %+v, want %+v", got, want)
	}
}

func TestSettingsNormalizedClampsUnpluggedMonitor(t *testing.T) {
	s := settings{Quality: quality4K, Buffer: bufferLow, Monitor: 2}
	if got := s.normalized(3).Monitor; got != 2 {
		t.Fatalf("monitor 2 of 3 = %d, want kept", got)
	}
	if got := s.normalized(2).Monitor; got != 0 {
		t.Fatalf("monitor 2 of 2 = %d, want reset to 0", got)
	}
	if got := s.normalized(0).Monitor; got != 2 {
		t.Fatalf("monitor with unknown display count = %d, want kept", got)
	}
}

func TestBufferMs(t *testing.T) {
	for preset, want := range map[string]int{bufferLow: 200, bufferSmooth: 1000, bufferMovie: 2000, "": 1000, "bogus": 1000} {
		if got := bufferMs(preset); got != want {
			t.Errorf("bufferMs(%q) = %d, want %d", preset, got, want)
		}
	}
}

func TestSenderArgs(t *testing.T) {
	for _, test := range []struct {
		name  string
		set   settings
		model string
		want  []string
	}{
		{
			name: "Apple TV 1080p default", set: defaultSettings(), model: "AppleTV14,1",
			want: []string{"-target", "10.0.0.5", "-target-latency-ms", "1000", "-machine-prompts", "-monitor", "0", "-video-codec", "h264"},
		},
		{
			name: "Apple TV 4K movie, second display, video only, debug", model: "AppleTV14,1",
			set:  settings{Quality: quality4K, Buffer: bufferMovie, Monitor: 1, AudioOff: true, Debug: true},
			want: []string{"-target", "10.0.0.5", "-target-latency-ms", "2000", "-machine-prompts", "-monitor", "1", "-video-codec", "hevc", "-bitrate", "25000", "-no-audio", "-debug"},
		},
		{
			name: "HomePod ignores video presets", model: "AudioAccessory5,1",
			set:  settings{Quality: quality4K, Buffer: bufferLow, Monitor: 1, AudioOff: true},
			want: []string{"-target", "10.0.0.5", "-target-latency-ms", "200", "-machine-prompts", "-audio-only"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := senderArgs(test.set, "10.0.0.5", test.model); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("senderArgs = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRunningAndScanStatus(t *testing.T) {
	if status, tip := runningStatus(true, "Kitchen"); status != "Streaming audio → Kitchen" || !strings.Contains(tip, "streaming audio to Kitchen") {
		t.Fatalf("audio status = %q / %q", status, tip)
	}
	if status, tip := runningStatus(false, "Living Room"); status != "Mirroring → Living Room" || !strings.Contains(tip, "mirroring to Living Room") {
		t.Fatalf("mirror status = %q / %q", status, tip)
	}
	if got := scanStatus(0); got != "No receivers found" {
		t.Fatalf("scanStatus(0) = %q", got)
	}
	if got := scanStatus(2); got != "Idle — 2 receiver(s)" {
		t.Fatalf("scanStatus(2) = %q", got)
	}
}

func TestSetupStatus(t *testing.T) {
	if got := setupStatus(gstreamer.Report{}); !strings.Contains(got, "GStreamer not found") {
		t.Fatalf("missing tools status = %q", got)
	}
	if got := setupStatus(gstreamer.Report{LaunchTool: "gst-launch-1.0", InspectTool: "gst-inspect-1.0"}); !strings.Contains(got, "plugins missing") {
		t.Fatalf("missing plugins status = %q", got)
	}
}

func TestExitStatus(t *testing.T) {
	failed := errors.New("exit status 1")
	for _, test := range []struct {
		name    string
		err     error
		outcome childOutcome
		lastLog string
		want    string
	}{
		{name: "clean exit", want: "Idle"},
		{name: "pairing abandoned", err: failed, outcome: childOutcome{sawCred: true}, lastLog: "pairing failed", want: "Pairing didn't complete — click the device to retry"},
		{name: "error after pairing shows the cause", err: failed, outcome: childOutcome{sawCred: true, paired: true}, lastLog: "audio capture failed: no wasapi2src", want: "Stopped: audio capture failed: no wasapi2src"},
		{name: "error without a log line", err: failed, want: "Stopped (error) — enable Debug logging"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := exitStatus(test.err, test.outcome, test.lastLog); got != test.want {
				t.Fatalf("exitStatus = %q, want %q", got, test.want)
			}
		})
	}
	long := strings.Repeat("x", 200)
	if got := exitStatus(failed, childOutcome{}, long); len([]rune(got)) > len("Stopped: ")+90 || !strings.HasSuffix(got, "…") {
		t.Fatalf("long error not truncated: %q", got)
	}
}

func TestCredentialPrompt(t *testing.T) {
	prompt, label, hide := credentialPrompt("pin", "Living Room")
	if prompt != "Enter the PIN shown on Living Room:" || label != "Enter PIN for Living Room…" || hide {
		t.Fatalf("pin prompt = %q / %q / hide=%t", prompt, label, hide)
	}
	prompt, label, hide = credentialPrompt("password", "Den")
	if !strings.Contains(prompt, "password for Den") || label != "Enter password for Den…" || !hide {
		t.Fatalf("password prompt = %q / %q / hide=%t (passwords must be masked)", prompt, label, hide)
	}
}

func TestPumpChildOutputAnswersCredentialRequests(t *testing.T) {
	out := strings.Join([]string{
		"selected: Living Room",
		airplay.CredentialRequestLine("pin"),
		airplay.CredentialRequestLine("password"),
		airplay.StatusLine("paired"),
		"streaming",
	}, "\n") + "\n"
	var asked []string
	answers := map[string]string{"pin": "1234", "password": "pass with spaces "}
	ask := func(kind string) (string, bool) {
		asked = append(asked, kind)
		return answers[kind], true
	}
	var stdin, tee strings.Builder
	outcome := pumpChildOutput(strings.NewReader(out), &stdin, &tee, ask)

	if !reflect.DeepEqual(asked, []string{"pin", "password"}) {
		t.Fatalf("asked = %v, want pin then password", asked)
	}
	if stdin.String() != "1234\npass with spaces \n" {
		t.Fatalf("stdin = %q, want each answer untrimmed with a newline", stdin.String())
	}
	if !outcome.sawCred || !outcome.paired {
		t.Fatalf("outcome = %+v, want sawCred and paired", outcome)
	}
	if tee.String() != out {
		t.Fatalf("debug log tee = %q, want every stdout line", tee.String())
	}
}

func TestPumpChildOutputCancelledDialogSendsBlankLine(t *testing.T) {
	var stdin strings.Builder
	outcome := pumpChildOutput(strings.NewReader(airplay.CredentialRequestLine("pin")+"\n"), &stdin, nil,
		func(string) (string, bool) { return "ignored", false })
	if stdin.String() != "\n" {
		t.Fatalf("stdin = %q, want a blank line so the sender exits instead of hanging", stdin.String())
	}
	if !outcome.sawCred || outcome.paired {
		t.Fatalf("outcome = %+v, want an unpaired credential request", outcome)
	}
}

func TestPumpChildOutputWithoutMarkers(t *testing.T) {
	var stdin strings.Builder
	outcome := pumpChildOutput(strings.NewReader("just logs\n"), &stdin, nil, func(string) (string, bool) {
		t.Fatal("asked for a credential without a request marker")
		return "", false
	})
	if stdin.Len() != 0 || outcome != (childOutcome{}) {
		t.Fatalf("stdin = %q outcome = %+v, want nothing", stdin.String(), outcome)
	}
}

func TestLastLineWriter(t *testing.T) {
	w := &lastLineWriter{}
	if w.Last() != "" {
		t.Fatal("new writer has a last line")
	}
	_, _ = io.WriteString(w, "2026/10/21 18:15:53 connected to: Apple TV\n")
	_, _ = io.WriteString(w, "2026/10/21 18:15:54 mirror setup failed: ")
	_, _ = io.WriteString(w, "HTTP 403\n\n   \n")
	if got := w.Last(); got != "mirror setup failed: HTTP 403" {
		t.Fatalf("Last() = %q, want the final non-empty line without its timestamp", got)
	}
	_, _ = io.WriteString(w, "panic: partial line without newline")
	if got := w.Last(); got != "panic: partial line without newline" {
		t.Fatalf("Last() = %q, want the trailing partial line", got)
	}
}

func TestStripLogTimestamp(t *testing.T) {
	for in, want := range map[string]string{
		"2026/10/21 18:15:53 hello":  "hello",
		"no timestamp here at all!!": "no timestamp here at all!!",
		"2026/10/21 18:15:53":        "2026/10/21 18:15:53",
	} {
		if got := stripLogTimestamp(in); got != want {
			t.Errorf("stripLogTimestamp(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDataDirIsUnderUserConfig(t *testing.T) {
	base, err := os.UserConfigDir()
	if err != nil {
		t.Skip("no user config dir:", err)
	}
	if want := filepath.Join(base, "doubletake"); dataDir() != want {
		t.Fatalf("dataDir() = %q, want %q (documented settings location)", dataDir(), want)
	}
	if filepath.Base(configPath()) != "tray.json" || filepath.Base(logPath()) != "doubletake.log" {
		t.Fatalf("config/log paths = %q / %q", configPath(), logPath())
	}
}
