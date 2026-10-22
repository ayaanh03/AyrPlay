package gstreamer

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Mode selects which streaming path a check covers.
type Mode string

const (
	ModeMirror    Mode = "mirror"     // screen + audio to an Apple TV
	ModeAudioOnly Mode = "audio-only" // system audio to a HomePod/speaker
)

// Requirement is one GStreamer element (or set of interchangeable elements)
// a streaming mode uses.
type Requirement struct {
	// AnyOf lists interchangeable elements; one present satisfies the check.
	AnyOf   []string
	Purpose string
	// Optional requirements degrade quality or features instead of failing.
	Optional bool
	Modes    []Mode
}

// Requirements returns what each streaming mode needs on goos. The element
// names mirror the pipelines in internal/airplay (capture.go, capture_windows.go,
// audio.go and audio_source_windows.go).
func Requirements(goos string) []Requirement {
	both := []Mode{ModeMirror, ModeAudioOnly}
	mirror := []Mode{ModeMirror}
	var reqs []Requirement
	if goos == "windows" {
		reqs = append(reqs,
			Requirement{AnyOf: []string{"wasapi2src"}, Purpose: "system audio capture (WASAPI loopback)", Modes: both},
			Requirement{AnyOf: []string{"d3d11screencapturesrc"}, Purpose: "screen capture (Direct3D 11)", Modes: mirror},
			Requirement{AnyOf: []string{"d3d11download"}, Purpose: "GPU-to-CPU frame download", Modes: mirror},
		)
	} else {
		reqs = append(reqs,
			Requirement{AnyOf: []string{"pipewiresrc", "pulsesrc"}, Purpose: "system audio capture", Modes: both},
			Requirement{AnyOf: []string{"pipewiresrc", "ximagesrc"}, Purpose: "screen capture (Wayland portal or X11)", Modes: mirror},
		)
	}
	reqs = append(reqs,
		Requirement{AnyOf: []string{"audioconvert"}, Purpose: "audio format conversion", Modes: both},
		Requirement{AnyOf: []string{"audioresample"}, Purpose: "audio resampling to 44.1 kHz", Modes: both},
		Requirement{AnyOf: []string{"videoconvert"}, Purpose: "video color conversion", Modes: mirror},
		Requirement{AnyOf: []string{"videoscale"}, Purpose: "scaling to the receiver's canvas", Modes: mirror},
		Requirement{AnyOf: []string{"nvh264enc", "vulkanh264enc", "vah264enc", "openh264enc", "x264enc"}, Purpose: "H.264 video encoder (NVENC preferred)", Modes: mirror},
		Requirement{AnyOf: []string{"h264parse"}, Purpose: "H.264 stream parsing", Modes: mirror},
		Requirement{AnyOf: []string{"nvh265enc"}, Purpose: "HEVC hardware encoder for 4K", Optional: true, Modes: mirror},
		Requirement{AnyOf: []string{"h265parse"}, Purpose: "HEVC stream parsing for 4K", Optional: true, Modes: mirror},
		Requirement{AnyOf: []string{"rtph264pay"}, Purpose: "capture timestamps (smoother A/V sync)", Optional: true, Modes: mirror},
		Requirement{AnyOf: []string{"audiobuffersplit"}, Purpose: "audio capture timestamps", Optional: true, Modes: both},
		Requirement{AnyOf: []string{"rtpL16pay"}, Purpose: "audio capture timestamps", Optional: true, Modes: both},
		Requirement{AnyOf: []string{"rtponviftimestamp"}, Purpose: "capture timestamps (smoother A/V sync)", Optional: true, Modes: both},
		Requirement{AnyOf: []string{"rtpstreampay"}, Purpose: "capture timestamps (smoother A/V sync)", Optional: true, Modes: both},
	)
	return reqs
}

func (r Requirement) appliesTo(mode Mode) bool {
	for _, m := range r.Modes {
		if m == mode {
			return true
		}
	}
	return false
}

// Check is the outcome of one requirement.
type Check struct {
	Elements []string `json:"elements"`
	Found    string   `json:"found,omitempty"` // the element that satisfied it
	Purpose  string   `json:"purpose"`
	Optional bool     `json:"optional"`
	OK       bool     `json:"ok"`
}

// ModeReport summarizes one streaming mode.
type ModeReport struct {
	Mode   Mode    `json:"mode"`
	Ready  bool    `json:"ready"`
	Checks []Check `json:"checks"`
}

// Report is the full environment check printed by `ayrplay doctor`.
type Report struct {
	OS          string       `json:"os"`
	LaunchTool  string       `json:"gst_launch,omitempty"`  // resolved gst-launch-1.0 path
	InspectTool string       `json:"gst_inspect,omitempty"` // resolved gst-inspect-1.0 path
	AddedToPath string       `json:"added_to_path,omitempty"`
	Modes       []ModeReport `json:"modes"`
	Ready       bool         `json:"ready"` // every mode is ready
	Hint        string       `json:"hint,omitempty"`
}

// Env abstracts the system for Diagnose so it can be tested.
type Env struct {
	GOOS        string
	LookPath    func(string) (string, error)
	HasElement  func(string) bool
	AddedToPath string
}

// SystemEnv probes the real system with gst-inspect-1.0.
func SystemEnv(goos, addedToPath string) Env {
	return Env{
		GOOS:        goos,
		LookPath:    exec.LookPath,
		HasElement:  func(name string) bool { return exec.Command("gst-inspect-1.0", name).Run() == nil },
		AddedToPath: addedToPath,
	}
}

// Diagnose checks the GStreamer tools and every mode's elements.
func Diagnose(env Env) Report {
	report := Report{OS: env.GOOS, AddedToPath: env.AddedToPath, Ready: true}
	report.LaunchTool, _ = env.LookPath("gst-launch-1.0")
	report.InspectTool, _ = env.LookPath("gst-inspect-1.0")
	toolsOK := report.LaunchTool != "" && report.InspectTool != ""

	cache := map[string]bool{}
	has := func(name string) bool {
		if !toolsOK {
			return false
		}
		ok, seen := cache[name]
		if !seen {
			ok = env.HasElement(name)
			cache[name] = ok
		}
		return ok
	}
	for _, mode := range []Mode{ModeMirror, ModeAudioOnly} {
		mr := ModeReport{Mode: mode, Ready: toolsOK}
		for _, req := range Requirements(env.GOOS) {
			if !req.appliesTo(mode) {
				continue
			}
			check := Check{Elements: req.AnyOf, Purpose: req.Purpose, Optional: req.Optional}
			for _, element := range req.AnyOf {
				if has(element) {
					check.OK, check.Found = true, element
					break
				}
			}
			if !check.OK && !req.Optional {
				mr.Ready = false
			}
			mr.Checks = append(mr.Checks, check)
		}
		if !mr.Ready {
			report.Ready = false
		}
		report.Modes = append(report.Modes, mr)
	}
	report.Hint = hint(env.GOOS, toolsOK, report.Ready)
	return report
}

func hint(goos string, toolsOK, ready bool) string {
	switch {
	case ready:
		return ""
	case !toolsOK && goos == "windows":
		return "GStreamer was not found. Install the MSVC 64-bit runtime from " + DownloadURL + " (with an older .msi installer, choose \"Complete\"), then run AyrPlay again; no PATH changes are needed."
	case !toolsOK:
		return "GStreamer was not found on PATH. Install gstreamer1.0-tools and the good/bad/ugly plugin sets with your package manager."
	case goos == "windows":
		return "Some GStreamer plugins are missing. Reinstall the latest GStreamer MSVC 64-bit runtime from " + DownloadURL + " (with an older .msi installer, choose \"Complete\")."
	default:
		return "Some GStreamer plugins are missing. Install the gst-plugins-good, -bad and -ugly packages."
	}
}

// Missing lists the required elements a mode lacks, for short status lines.
func (r Report) Missing(mode Mode) []string {
	var missing []string
	if r.LaunchTool == "" || r.InspectTool == "" {
		missing = append(missing, "gst-launch-1.0/gst-inspect-1.0")
	}
	for _, mr := range r.Modes {
		if mr.Mode != mode {
			continue
		}
		for _, c := range mr.Checks {
			if !c.OK && !c.Optional && (r.LaunchTool != "" && r.InspectTool != "") {
				missing = append(missing, strings.Join(c.Elements, " or "))
			}
		}
	}
	return missing
}

// WriteText prints a human-readable report.
func (r Report) WriteText(w io.Writer) {
	tool := func(name, path string) {
		if path == "" {
			fmt.Fprintf(w, "  [missing] %s\n", name)
		} else {
			fmt.Fprintf(w, "  [ok]      %s (%s)\n", name, path)
		}
	}
	fmt.Fprintln(w, "GStreamer:")
	tool("gst-launch-1.0", r.LaunchTool)
	tool("gst-inspect-1.0", r.InspectTool)
	if r.AddedToPath != "" {
		fmt.Fprintf(w, "  (found outside PATH; using %s)\n", r.AddedToPath)
	}
	width := 0
	for _, mr := range r.Modes {
		for _, c := range mr.Checks {
			width = max(width, len(checkName(c)))
		}
	}
	for _, mr := range r.Modes {
		state := "ready"
		if !mr.Ready {
			state = "NOT READY"
		}
		fmt.Fprintf(w, "\n%s: %s\n", mr.Mode, state)
		for _, c := range mr.Checks {
			mark := "[ok]     "
			switch {
			case c.OK:
			case c.Optional:
				mark = "[optional]"
			default:
				mark = "[missing]"
			}
			fmt.Fprintf(w, "  %-10s %-*s  %s\n", mark, width, checkName(c), c.Purpose)
		}
	}
	if r.Hint != "" {
		fmt.Fprintf(w, "\n%s\n", r.Hint)
	}
}

// checkName is the element that satisfied c, or every alternative when none did.
func checkName(c Check) string {
	if c.Found != "" {
		return c.Found
	}
	return strings.Join(c.Elements, " | ")
}

// WriteJSON prints the report as indented JSON for scripts and agents.
func (r Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
