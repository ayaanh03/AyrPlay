// Command ayrplay is AyrPlay's single executable. Double-clicked (or run with
// no arguments) it starts the system-tray app; with a subcommand it is the
// command-line sender, a receiver scanner, or a setup checker, so one file
// covers both people and scripts/agents.
package main

//go:generate go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo -platform-specific -icon=../../internal/tray/icon.ico -manifest=ayrplay.exe.manifest versioninfo.json

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"doubletake/internal/airplay"
	"doubletake/internal/gstreamer"
	"doubletake/internal/sender"
	"doubletake/internal/tray"
)

// version is set at release build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

const usage = `AyrPlay — mirror your screen or stream audio to Apple TV / HomePod over AirPlay 2.

Usage:
  ayrplay                      Start the system-tray app (same as double-clicking)
  ayrplay tray                 Run the tray app in the foreground (logs to this console)
  ayrplay discover [-json]     List AirPlay receivers on the local network
  ayrplay doctor [-json]       Check that GStreamer and its plugins are installed
  ayrplay send [flags]         Stream to a receiver from the command line
  ayrplay version              Print version information
  ayrplay help                 Show this help (ayrplay send -h lists every send flag)

Examples:
  ayrplay discover -json
  ayrplay send -target 192.168.1.77                       # mirror screen + audio
  ayrplay send -target 192.168.1.77 -video-codec hevc     # 4K HEVC
  ayrplay send -target 192.168.1.50 -audio-only           # HomePod / speaker

Exit codes: 0 success, 1 failure (doctor: setup incomplete), 2 usage error.
Flags may also be given without "send" (ayrplay -target ...), like doubletake.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, defaultDeps()))
}

// deps holds the side effects run needs, so dispatch can be tested.
type deps struct {
	executable   func() (string, error)
	ensureGst    func() string
	discover     func(ctx context.Context) ([]airplay.AirPlayDevice, error)
	diagnose     func(addedToPath string) gstreamer.Report
	runTray      func(tray.Options)
	startTray    func(self string) error // launch the tray detached, then return
	detachTray   bool                    // no-arg launch detaches (Windows)
	runSender    func(prog string, args []string)
	printVersion func(w io.Writer)
}

func defaultDeps() deps {
	return deps{
		executable: os.Executable,
		ensureGst:  gstreamer.EnsureOnPath,
		discover:   airplay.DiscoverAirPlayDevices,
		diagnose: func(added string) gstreamer.Report {
			return gstreamer.Diagnose(gstreamer.SystemEnv(runtime.GOOS, added))
		},
		runTray:      tray.Run,
		startTray:    startTrayDetached,
		detachTray:   runtime.GOOS == "windows",
		runSender:    sender.Main,
		printVersion: printVersion,
	}
}

func run(args []string, stdout, stderr io.Writer, d deps) int {
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch {
	case cmd == "help" || cmd == "-h" || cmd == "-help" || cmd == "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case cmd == "version" || cmd == "-version" || cmd == "--version":
		d.printVersion(stdout)
		return 0
	}

	// Every remaining mode may run GStreamer. Find an installed runtime even
	// when it is not on PATH, so a fresh install works without a sign-out.
	added := d.ensureGst()

	switch {
	case cmd == "":
		self, err := d.executable()
		if err != nil {
			fmt.Fprintf(stderr, "ayrplay: %v\n", err)
			return 1
		}
		if d.detachTray {
			if err := d.startTray(self); err != nil {
				fmt.Fprintf(stderr, "ayrplay: start tray: %v\n", err)
				return 1
			}
			fmt.Fprintln(stdout, "AyrPlay is running in the system tray. Run \"ayrplay help\" for command-line use.")
			return 0
		}
		d.runTray(tray.Options{SenderPath: self, SenderArgs: []string{"send"}})
		return 0
	case cmd == "tray":
		self, err := d.executable()
		if err != nil {
			fmt.Fprintf(stderr, "ayrplay: %v\n", err)
			return 1
		}
		d.runTray(tray.Options{SenderPath: self, SenderArgs: []string{"send"}})
		return 0
	case cmd == "discover" || cmd == "list":
		return runDiscover(args[1:], stdout, stderr, d.discover)
	case cmd == "doctor":
		return runDoctor(args[1:], stdout, stderr, func() gstreamer.Report { return d.diagnose(added) })
	case cmd == "send":
		d.runSender("ayrplay send", args[1:])
		return 0
	case strings.HasPrefix(cmd, "-"):
		d.runSender("ayrplay", args)
		return 0
	default:
		fmt.Fprintf(stderr, "ayrplay: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}

// receiver is one discovered device in `discover -json` output.
type receiver struct {
	Name     string `json:"name"`
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	Model    string `json:"model"`
	DeviceID string `json:"device_id,omitempty"`
	// Mode is how the tray streams to it: "mirror" or "audio-only".
	Mode string `json:"mode"`
	// SendArgs are the arguments to pass to `ayrplay send` for this receiver.
	SendArgs []string `json:"send_args"`
}

func toReceivers(devices []airplay.AirPlayDevice) []receiver {
	out := make([]receiver, 0, len(devices))
	for _, d := range devices {
		r := receiver{Name: d.Name, IP: d.IP, Port: d.Port, Model: d.Model, DeviceID: d.DeviceID,
			Mode: string(gstreamer.ModeMirror), SendArgs: []string{"-target", d.IP}}
		if d.Port != 0 && d.Port != 7000 {
			r.SendArgs = append(r.SendArgs, "-port", fmt.Sprint(d.Port))
		}
		if airplay.IsAudioOnlyModel(d.Model) {
			r.Mode = string(gstreamer.ModeAudioOnly)
			r.SendArgs = append(r.SendArgs, "-audio-only")
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].IP < out[j].IP
	})
	return out
}

func runDiscover(args []string, stdout, stderr io.Writer, discover func(context.Context) ([]airplay.AirPlayDevice, error)) int {
	flags := flag.NewFlagSet("ayrplay discover", flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "Print receivers as a JSON array")
	timeout := flags.Duration("timeout", 5*time.Second, "How long to listen for mDNS announcements")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	devices, err := discover(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "ayrplay discover: %v\n", err)
		return 1
	}
	receivers := toReceivers(devices)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(receivers); err != nil {
			fmt.Fprintf(stderr, "ayrplay discover: %v\n", err)
			return 1
		}
		return 0
	}
	if len(receivers) == 0 {
		fmt.Fprintln(stdout, "No AirPlay receivers found. Check that the receiver is awake, on the same network/subnet, and that AyrPlay is allowed through the firewall (UDP 5353).")
		return 0
	}
	fmt.Fprintf(stdout, "Found %d AirPlay receiver(s):\n", len(receivers))
	for _, r := range receivers {
		fmt.Fprintf(stdout, "  %-24s %-15s %-10s %-18s ayrplay send %s\n", r.Name, r.IP, r.Mode, r.Model, strings.Join(r.SendArgs, " "))
	}
	return 0
}

func runDoctor(args []string, stdout, stderr io.Writer, diagnose func() gstreamer.Report) int {
	flags := flag.NewFlagSet("ayrplay doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "Print the report as JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	report := diagnose()
	if *asJSON {
		if err := report.WriteJSON(stdout); err != nil {
			fmt.Fprintf(stderr, "ayrplay doctor: %v\n", err)
			return 1
		}
	} else {
		report.WriteText(stdout)
	}
	if !report.Ready {
		return 1
	}
	return 0
}

func printVersion(w io.Writer) {
	fmt.Fprintf(w, "ayrplay %s (%s/%s, %s)\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				fmt.Fprintf(w, "commit %s\n", s.Value)
			}
		}
	}
}
