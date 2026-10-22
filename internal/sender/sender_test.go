package sender

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"testing"

	"doubletake/internal/airplay"
)

func TestParsePortRange(t *testing.T) {
	for _, test := range []struct {
		name       string
		value      string
		wantMin    int
		wantMax    int
		wantErrSub string
	}{
		{name: "ephemeral"},
		{name: "valid", value: "60000-60010", wantMin: 60000, wantMax: 60010},
		{name: "whitespace", value: " 60000 - 60002 ", wantMin: 60000, wantMax: 60002},
		{name: "malformed", value: "60000", wantErrSub: "expected MIN-MAX"},
		{name: "minimum invalid", value: "zero-60010", wantErrSub: "min:"},
		{name: "maximum invalid", value: "60000-many", wantErrSub: "max:"},
		{name: "zero", value: "0-2", wantErrSub: "out of bounds"},
		{name: "maximum too high", value: "65534-65536", wantErrSub: "out of bounds"},
		{name: "reversed", value: "60010-60000", wantErrSub: "out of bounds"},
		{name: "too small", value: "60000-60001", wantErrSub: "too small"},
	} {
		t.Run(test.name, func(t *testing.T) {
			gotMin, gotMax, err := parsePortRange(test.value)
			if test.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErrSub) {
					t.Fatalf("parsePortRange(%q) error = %v, want substring %q", test.value, err, test.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePortRange(%q): %v", test.value, err)
			}
			if gotMin != test.wantMin || gotMax != test.wantMax {
				t.Fatalf("parsePortRange(%q) = %d-%d, want %d-%d", test.value, gotMin, gotMax, test.wantMin, test.wantMax)
			}
		})
	}
}

func TestReadCredentialPreservesPasswordAndEmptyInput(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("password with spaces\n\n"))
	if got := readCredential(reader, "", "password"); got != "password with spaces" {
		t.Fatalf("password = %q, want %q", got, "password with spaces")
	}
	if got := readCredential(reader, "", "password"); got != "" {
		t.Fatalf("blank password choice = %q, want empty", got)
	}
}

func TestReadCredentialMachinePromptEmitsMarker(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	machinePromptMode = true
	t.Cleanup(func() {
		machinePromptMode = false
		os.Stdout = old
	})

	reader := bufio.NewReader(strings.NewReader("1234\n"))
	got := readCredential(reader, "human prompt that must be suppressed", "pin")
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)

	if got != "1234" {
		t.Fatalf("value = %q, want %q", got, "1234")
	}
	if want := airplay.CredentialRequestLine("pin"); strings.TrimSpace(string(out)) != want {
		t.Fatalf("stdout = %q, want marker %q", string(out), want)
	}
	if strings.Contains(string(out), "human prompt") {
		t.Fatalf("machine mode must not print the human prompt; got %q", string(out))
	}
}

func TestPairingCredentialPrompt(t *testing.T) {
	for _, test := range []struct {
		name       string
		expectPIN  bool
		displayErr error
		want       string
	}{
		{name: "advertised PIN displayed", expectPIN: true, want: "Enter the PIN shown on the receiver: "},
		{name: "advertised PIN display failed", expectPIN: true, displayErr: errors.New("display failed"), want: "Enter the receiver's configured password or pairing PIN: "},
		{name: "unknown mode accepted display request", want: "Enter the receiver's configured password or pairing PIN: "},
		{name: "unknown mode rejected display request", displayErr: errors.New("not supported"), want: "Enter the receiver's configured password or pairing PIN: "},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := pairingCredentialPrompt(test.expectPIN, test.displayErr); got != test.want {
				t.Fatalf("prompt = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPasswordRequiresPairing(t *testing.T) {
	const (
		passwordFlag = uint64(1 << 7)
		pinFlag      = uint64(1 << 9)
	)
	modernFeatures := airplay.FeatureSystemPairing | airplay.FeatureTransientPairing | uint64(1<<38) | uint64(1<<46)
	legacyFeatures := modernFeatures | uint64(1<<51)

	for _, test := range []struct {
		name string
		info *airplay.ReceiverInfo
		want bool
	}{
		{name: "missing info"},
		{name: "legacy configured password", info: &airplay.ReceiverInfo{Features: legacyFeatures, StatusFlags: passwordFlag}, want: true},
		{name: "modern configured password can pair transiently", info: &airplay.ReceiverInfo{Features: modernFeatures, StatusFlags: passwordFlag}},
		{name: "modern password suppresses PIN but remains Digest only", info: &airplay.ReceiverInfo{Features: modernFeatures, StatusFlags: passwordFlag | pinFlag}},
		{name: "legacy password wins over PIN", info: &airplay.ReceiverInfo{Features: legacyFeatures, StatusFlags: passwordFlag | pinFlag}, want: true},
		{name: "legacy receiver without password", info: &airplay.ReceiverInfo{Features: legacyFeatures}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := passwordRequiresPairing(test.info); got != test.want {
				t.Fatalf("passwordRequiresPairing() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestForcePairDoesNotUseModernDigestPasswordAsSRPPIN(t *testing.T) {
	const passwordFlag = uint64(1 << 7)
	modernFeatures := airplay.FeatureSystemPairing | airplay.FeatureTransientPairing | uint64(1<<38) | uint64(1<<46)
	legacyFeatures := modernFeatures | uint64(1<<51)

	if !forcePairUsesTransient(&airplay.ReceiverInfo{Features: modernFeatures, StatusFlags: passwordFlag}) {
		t.Fatal("modern fixed-password receiver would reuse its Digest password as an SRP PIN")
	}
	if forcePairUsesTransient(&airplay.ReceiverInfo{Features: legacyFeatures, StatusFlags: passwordFlag}) {
		t.Fatal("legacy fixed-password receiver lost its authenticated SRP pairing path")
	}
	if forcePairUsesTransient(&airplay.ReceiverInfo{Features: modernFeatures}) {
		t.Fatal("unprotected modern receiver unexpectedly bypassed explicit full pairing")
	}
}

func TestRestoreSavedPairingRestoresRawProtocol(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	client := airplay.NewAirPlayClient("127.0.0.1", 7000)
	err = restoreSavedPairing(client, &airplay.SavedCredentials{
		PairingID:       "pair-id",
		Ed25519Public:   pub,
		Ed25519Seed:     priv.Seed(),
		PairingProtocol: airplay.PairingProtocolRaw,
	})
	if err != nil {
		t.Fatalf("restoreSavedPairing: %v", err)
	}
	if got := client.PairingProtocol(); got != airplay.PairingProtocolRaw {
		t.Fatalf("restored protocol = %q, want raw", got)
	}
}

func TestParseOptionsDefaults(t *testing.T) {
	opts, err := parseOptions("ayrplay", nil, func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatalf("parseOptions: %v", err)
	}
	if opts.port != 7000 || opts.fps != 30 || opts.hwaccel != "auto" || opts.videoCodec != "auto" ||
		opts.credBackend != "file" || opts.target != "" || opts.audioOnly || opts.noAudio || opts.monitorIndex != 0 {
		t.Fatalf("defaults = %+v", opts)
	}
}

func TestParseOptionsTrayCommandLine(t *testing.T) {
	// The exact flags the tray passes for a 4K Apple TV session and a HomePod.
	opts, err := parseOptions("ayrplay", []string{
		"-target", "192.168.1.77", "-target-latency-ms", "2000", "-machine-prompts",
		"-monitor", "1", "-video-codec", "hevc", "-bitrate", "25000", "-no-audio", "-debug",
	}, func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatalf("parseOptions: %v", err)
	}
	if opts.target != "192.168.1.77" || opts.targetLatencyMs != 2000 || !opts.machinePrompts ||
		opts.monitorIndex != 1 || opts.videoCodec != "hevc" || opts.bitrate != 25000 || !opts.noAudio || !opts.debug {
		t.Fatalf("parsed = %+v", opts)
	}

	opts, err = parseOptions("ayrplay", []string{"-target", "192.168.1.50", "-audio-only", "-port-range", "60000-60010"},
		func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatalf("parseOptions audio-only: %v", err)
	}
	if !opts.audioOnly || opts.portMin != 60000 || opts.portMax != 60010 {
		t.Fatalf("parsed = %+v", opts)
	}
}

func TestParseOptionsRejectsInvalidInput(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    []string
		wantSub string
	}{
		{name: "audio-only with no-audio", args: []string{"-audio-only", "-no-audio"}, wantSub: "mutually exclusive"},
		{name: "unknown hwaccel", args: []string{"-hwaccel", "quantum"}, wantSub: "invalid -hwaccel"},
		{name: "unknown codec", args: []string{"-video-codec", "av1"}, wantSub: "invalid -video-codec"},
		{name: "bad port range", args: []string{"-port-range", "1-2"}, wantSub: "invalid -port-range"},
		{name: "negative monitor", args: []string{"-monitor", "-1"}, wantSub: "invalid -monitor"},
		{name: "bad x11 window id", args: []string{"-x11-window-id", "window"}, wantSub: "invalid -x11-window-id"},
		{name: "positional argument", args: []string{"192.168.1.77"}, wantSub: "unexpected argument"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseOptions("ayrplay", test.args, func(string) string { return "" }, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.wantSub) {
				t.Fatalf("parseOptions(%v) error = %v, want substring %q", test.args, err, test.wantSub)
			}
		})
	}
}

func TestParseOptionsFlagErrorsAreUsageErrors(t *testing.T) {
	var out strings.Builder
	_, err := parseOptions("ayrplay", []string{"-no-such-flag"}, func(string) string { return "" }, &out)
	if !errors.Is(err, errUsage) {
		t.Fatalf("unknown flag error = %v, want errUsage", err)
	}
	if !strings.Contains(out.String(), "-no-such-flag") {
		t.Fatalf("flag error was not reported to the user: %q", out.String())
	}
	if _, err := parseOptions("ayrplay", []string{"-h"}, func(string) string { return "" }, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("-h error = %v, want flag.ErrHelp", err)
	}
}

func TestParseOptionsCodeFromEnvironmentWins(t *testing.T) {
	env := func(name string) string {
		if name == "DOUBLETAKE_CODE" {
			return "from env"
		}
		return ""
	}
	opts, err := parseOptions("ayrplay", []string{"-code", "from flag"}, env, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if opts.credential != "from env" {
		t.Fatalf("credential = %q, want $DOUBLETAKE_CODE to override -code", opts.credential)
	}
	opts, err = parseOptions("ayrplay", []string{"-code", "1234"}, func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if opts.credential != "1234" {
		t.Fatalf("credential = %q, want -code value", opts.credential)
	}
}

func TestParseXID(t *testing.T) {
	for in, want := range map[string]uint64{"": 0, "  ": 0, "42": 42, "0x2a": 42} {
		got, err := parseXID(in)
		if err != nil || got != want {
			t.Errorf("parseXID(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := parseXID("nope"); err == nil {
		t.Error("parseXID accepted a non-number")
	}
}

func TestPrintDefaultsListsEveryWindowsFlag(t *testing.T) {
	var out strings.Builder
	PrintDefaults(&out)
	for _, name := range []string{"-target", "-audio-only", "-monitor", "-video-codec", "-target-latency-ms", "-code", "-machine-prompts"} {
		if !strings.Contains(out.String(), name) {
			t.Errorf("PrintDefaults output is missing %s", name)
		}
	}
}

func TestCompareIPs(t *testing.T) {
	for _, test := range []struct {
		a, b string
		want int
	}{
		{"192.168.1.2", "192.168.1.10", -1},
		{"192.168.1.10", "192.168.1.2", 1},
		{"10.0.0.1", "10.0.0.1", 0},
		{"10.0.0.1", "not-an-ip", -1},
		{"host-b", "host-a", 1},
	} {
		if got := compareIPs(test.a, test.b); got != test.want {
			t.Errorf("compareIPs(%q, %q) = %d, want %d", test.a, test.b, got, test.want)
		}
	}
}
