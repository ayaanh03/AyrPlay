package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"doubletake/internal/airplay"
	"doubletake/internal/gstreamer"
	"doubletake/internal/tray"
)

// recorder captures what run dispatched to.
type recorder struct {
	trayOpts     *tray.Options
	startedTray  string
	senderProg   string
	senderArgs   []string
	gstEnsured   int
	diagnosedFor string
}

func testDeps(r *recorder) deps {
	return deps{
		executable: func() (string, error) { return `C:\Tools\AyrPlay.exe`, nil },
		ensureGst: func() string {
			r.gstEnsured++
			return `C:\Program Files\gstreamer\1.0\msvc_x86_64\bin`
		},
		discover: func(context.Context) ([]airplay.AirPlayDevice, error) {
			return []airplay.AirPlayDevice{
				{Name: "Living Room", IP: "192.168.1.77", Port: 7000, Model: "AppleTV14,1", DeviceID: "AA:BB"},
				{Name: "Kitchen", IP: "192.168.1.50", Port: 7000, Model: "AudioAccessory5,1"},
				{Name: "Office", IP: "192.168.1.60", Port: 7100, Model: "AppleTV5,3"},
			}, nil
		},
		diagnose: func(added string) gstreamer.Report {
			r.diagnosedFor = added
			return gstreamer.Report{Ready: added != ""}
		},
		runTray:      func(o tray.Options) { r.trayOpts = &o },
		startTray:    func(self string) error { r.startedTray = self; return nil },
		runSender:    func(prog string, args []string) { r.senderProg, r.senderArgs = prog, args },
		printVersion: func(w io.Writer) { io.WriteString(w, "ayrplay test\n") },
	}
}

func runCapture(t *testing.T, d deps, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut strings.Builder
	code = run(args, &out, &errOut, d)
	return code, out.String(), errOut.String()
}

func TestNoArgsOnWindowsDetachesTray(t *testing.T) {
	r := &recorder{}
	d := testDeps(r)
	d.detachTray = true
	code, out, _ := runCapture(t, d)
	if code != 0 || r.startedTray != `C:\Tools\AyrPlay.exe` || r.trayOpts != nil {
		t.Fatalf("code=%d started=%q inProcess=%v, want a detached tray of this exe", code, r.startedTray, r.trayOpts)
	}
	if !strings.Contains(out, "system tray") {
		t.Fatalf("stdout = %q, want a pointer to the tray icon", out)
	}
	if r.gstEnsured != 1 {
		t.Fatal("GStreamer was not located before starting the tray")
	}
}

func TestNoArgsDetachFailureIsReported(t *testing.T) {
	r := &recorder{}
	d := testDeps(r)
	d.detachTray = true
	d.startTray = func(string) error { return errors.New("access denied") }
	if code, _, errOut := runCapture(t, d); code != 1 || !strings.Contains(errOut, "access denied") {
		t.Fatalf("code=%d stderr=%q, want the launch error", code, errOut)
	}
}

func TestNoArgsElsewhereRunsTrayInProcess(t *testing.T) {
	r := &recorder{}
	code, _, _ := runCapture(t, testDeps(r))
	want := tray.Options{SenderPath: `C:\Tools\AyrPlay.exe`, SenderArgs: []string{"send"}}
	if code != 0 || r.trayOpts == nil || !reflect.DeepEqual(*r.trayOpts, want) {
		t.Fatalf("code=%d tray=%+v, want %+v", code, r.trayOpts, want)
	}
}

func TestTrayCommandRunsInForeground(t *testing.T) {
	r := &recorder{}
	d := testDeps(r)
	d.detachTray = true // even on Windows: "ayrplay tray" is the detached child
	if code, _, _ := runCapture(t, d, "tray"); code != 0 || r.trayOpts == nil || r.startedTray != "" {
		t.Fatalf("code=%d tray=%v started=%q, want an in-process tray", code, r.trayOpts, r.startedTray)
	}
	if r.trayOpts.SenderPath != `C:\Tools\AyrPlay.exe` || !reflect.DeepEqual(r.trayOpts.SenderArgs, []string{"send"}) {
		t.Fatalf("tray sender = %q %q, want this exe with the send subcommand", r.trayOpts.SenderPath, r.trayOpts.SenderArgs)
	}
}

func TestSendDispatch(t *testing.T) {
	r := &recorder{}
	if code, _, _ := runCapture(t, testDeps(r), "send", "-target", "192.168.1.50", "-audio-only"); code != 0 {
		t.Fatalf("send exit %d", code)
	}
	if r.senderProg != "ayrplay send" || !reflect.DeepEqual(r.senderArgs, []string{"-target", "192.168.1.50", "-audio-only"}) {
		t.Fatalf("sender got %q %q", r.senderProg, r.senderArgs)
	}
	if r.gstEnsured != 1 {
		t.Fatal("send did not locate GStreamer first")
	}

	// doubletake-style bare flags keep working.
	r = &recorder{}
	runCapture(t, testDeps(r), "-target", "192.168.1.77", "-video-codec", "hevc")
	if r.senderProg != "ayrplay" || !reflect.DeepEqual(r.senderArgs, []string{"-target", "192.168.1.77", "-video-codec", "hevc"}) {
		t.Fatalf("bare flags sent %q %q", r.senderProg, r.senderArgs)
	}
}

func TestHelpVersionAndUnknownCommand(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help", "-help"} {
		r := &recorder{}
		code, out, _ := runCapture(t, testDeps(r), arg)
		if code != 0 || !strings.Contains(out, "ayrplay discover [-json]") || r.gstEnsured != 0 {
			t.Fatalf("%s: code=%d gst=%d out=%q", arg, code, r.gstEnsured, out)
		}
	}
	for _, arg := range []string{"version", "--version"} {
		if code, out, _ := runCapture(t, testDeps(&recorder{}), arg); code != 0 || out != "ayrplay test\n" {
			t.Fatalf("%s: code=%d out=%q", arg, code, out)
		}
	}
	code, _, errOut := runCapture(t, testDeps(&recorder{}), "mirror")
	if code != 2 || !strings.Contains(errOut, `unknown command "mirror"`) {
		t.Fatalf("unknown command: code=%d stderr=%q", code, errOut)
	}
}

func TestDiscoverJSONIsAgentReady(t *testing.T) {
	code, out, _ := runCapture(t, testDeps(&recorder{}), "discover", "-json", "-timeout", "1ms")
	if code != 0 {
		t.Fatalf("discover exit %d", code)
	}
	var got []receiver
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("discover -json output does not parse: %v\n%s", err, out)
	}
	want := []receiver{
		{Name: "Kitchen", IP: "192.168.1.50", Port: 7000, Model: "AudioAccessory5,1", Mode: "audio-only", SendArgs: []string{"-target", "192.168.1.50", "-audio-only"}},
		{Name: "Living Room", IP: "192.168.1.77", Port: 7000, Model: "AppleTV14,1", DeviceID: "AA:BB", Mode: "mirror", SendArgs: []string{"-target", "192.168.1.77"}},
		{Name: "Office", IP: "192.168.1.60", Port: 7100, Model: "AppleTV5,3", Mode: "mirror", SendArgs: []string{"-target", "192.168.1.60", "-port", "7100"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("discover -json = %+v\nwant %+v", got, want)
	}
}

func TestDiscoverTextAndErrors(t *testing.T) {
	code, out, _ := runCapture(t, testDeps(&recorder{}), "discover")
	if code != 0 || !strings.Contains(out, "Found 3 AirPlay receiver(s)") || !strings.Contains(out, "ayrplay send -target 192.168.1.50 -audio-only") {
		t.Fatalf("discover text: code=%d\n%s", code, out)
	}

	d := testDeps(&recorder{})
	d.discover = func(context.Context) ([]airplay.AirPlayDevice, error) { return nil, nil }
	if code, out, _ := runCapture(t, d, "list"); code != 0 || !strings.Contains(out, "No AirPlay receivers found") {
		t.Fatalf("empty discover: code=%d out=%q", code, out)
	}
	d.discover = func(context.Context) ([]airplay.AirPlayDevice, error) {
		return nil, errors.New("no multicast interface")
	}
	if code, _, errOut := runCapture(t, d, "discover"); code != 1 || !strings.Contains(errOut, "no multicast interface") {
		t.Fatalf("failed discover: code=%d stderr=%q", code, errOut)
	}
	if code, _, _ := runCapture(t, d, "discover", "-bogus"); code != 2 {
		t.Fatalf("bad discover flag exit = %d, want 2", code)
	}
}

func TestDoctorExitCodesAndJSON(t *testing.T) {
	r := &recorder{}
	code, out, _ := runCapture(t, testDeps(r), "doctor", "-json")
	if code != 0 || !strings.Contains(out, `"ready": true`) {
		t.Fatalf("ready doctor: code=%d out=%s", code, out)
	}
	if r.diagnosedFor != `C:\Program Files\gstreamer\1.0\msvc_x86_64\bin` {
		t.Fatalf("doctor diagnosed with PATH addition %q, want the located GStreamer dir", r.diagnosedFor)
	}

	d := testDeps(&recorder{})
	d.ensureGst = func() string { return "" } // fake diagnose reports not ready
	if code, _, _ := runCapture(t, d, "doctor"); code != 1 {
		t.Fatalf("incomplete setup doctor exit = %d, want 1", code)
	}
}

func TestToReceiversSortsByNameThenIP(t *testing.T) {
	got := toReceivers([]airplay.AirPlayDevice{
		{Name: "B", IP: "10.0.0.2"}, {Name: "A", IP: "10.0.0.9"}, {Name: "A", IP: "10.0.0.1"},
	})
	var order []string
	for _, r := range got {
		order = append(order, r.Name+"@"+r.IP)
	}
	if want := []string{"A@10.0.0.1", "A@10.0.0.9", "B@10.0.0.2"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestPrintVersion(t *testing.T) {
	var out strings.Builder
	printVersion(&out)
	if !strings.HasPrefix(out.String(), "ayrplay "+version+" (") {
		t.Fatalf("version output = %q", out.String())
	}
}
