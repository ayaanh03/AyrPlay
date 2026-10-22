package tray

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"doubletake/internal/airplay"
)

// This file holds the tray's UI-independent logic so it can be unit tested
// without a system tray or native dialogs.

const (
	quality1080p = "1080p"
	quality4K    = "4k"

	bufferLow    = "low"
	bufferSmooth = "smooth"
	bufferMovie  = "movie"
)

// settings is the persisted tray configuration.
type settings struct {
	Quality  string `json:"quality"` // "1080p" or "4k"
	Buffer   string `json:"buffer"`  // "low", "smooth", or "movie"
	AudioOff bool   `json:"audio_off"`
	Debug    bool   `json:"debug"`
	Monitor  int    `json:"monitor"` // 0-based display index
}

func defaultSettings() settings { return settings{Quality: quality1080p, Buffer: bufferSmooth} }

// normalized replaces unknown or out-of-range values (a hand-edited or stale
// tray.json, or a monitor that was unplugged) with defaults. displays is the
// number of connected monitors; 0 skips the upper bound while it is unknown.
func (s settings) normalized(displays int) settings {
	if s.Quality != quality1080p && s.Quality != quality4K {
		s.Quality = quality1080p
	}
	if s.Buffer != bufferLow && s.Buffer != bufferSmooth && s.Buffer != bufferMovie {
		s.Buffer = bufferSmooth
	}
	if s.Monitor < 0 || (displays > 0 && s.Monitor >= displays) {
		s.Monitor = 0
	}
	return s
}

func dataDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "doubletake")
}

func configPath() string { return filepath.Join(dataDir(), "tray.json") }
func logPath() string    { return filepath.Join(dataDir(), "doubletake.log") }

// loadSettings reads path, falling back to defaults for a missing or corrupt
// file and for any field it does not set.
func loadSettings(path string) settings {
	s := defaultSettings()
	if b, err := os.ReadFile(path); err == nil {
		loaded := s
		if json.Unmarshal(b, &loaded) == nil {
			s = loaded
		}
	}
	return s.normalized(0) // the display count is checked once the menu knows it
}

func (s settings) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// bufferMs maps a buffer preset to a -target-latency-ms value.
func bufferMs(preset string) int {
	switch preset {
	case bufferLow:
		return 200
	case bufferMovie:
		return 2000
	default: // smooth
		return 1000
	}
}

// senderArgs builds the sender command line for a click on a receiver. A
// HomePod (model prefix "AudioAccessory") gets the audio-only path; every other
// receiver gets screen mirroring with the current presets.
func senderArgs(s settings, ip, model string) []string {
	args := []string{
		"-target", ip,
		"-target-latency-ms", fmt.Sprint(bufferMs(s.Buffer)),
		"-machine-prompts",
	}
	if airplay.IsAudioOnlyModel(model) {
		// No -monitor/-video-codec/-bitrate/-no-audio: there is no video, and the
		// receiver always carries audio.
		args = append(args, "-audio-only")
	} else {
		args = append(args, "-monitor", fmt.Sprint(s.Monitor))
		if s.Quality == quality4K {
			args = append(args, "-video-codec", "hevc", "-bitrate", "25000")
		} else {
			args = append(args, "-video-codec", "h264")
		}
		if s.AudioOff {
			args = append(args, "-no-audio")
		}
	}
	if s.Debug {
		args = append(args, "-debug")
	}
	return args
}

// runningStatus returns the status line and tooltip for an active session.
func runningStatus(audioOnly bool, name string) (status, tooltip string) {
	if audioOnly {
		return "Streaming audio → " + name, "AyrPlay — streaming audio to " + name
	}
	return "Mirroring → " + name, "AyrPlay — mirroring to " + name
}

// childOutcome records what the sender's output stream revealed about a session.
type childOutcome struct {
	sawCred bool // a pairing PIN/password was requested
	paired  bool // pairing completed (so a later error is not a pairing failure)
}

// exitStatus is the status line after a session ends on its own. lastLog is
// the sender's final log line, which names the failure (log.Fatal output).
func exitStatus(waitErr error, outcome childOutcome, lastLog string) string {
	switch {
	case waitErr == nil:
		return "Idle"
	case outcome.sawCred && !outcome.paired:
		return "Pairing didn't complete — click the device to retry"
	case lastLog != "":
		return "Stopped: " + truncate(lastLog, 90)
	default:
		return "Stopped (error) — enable Debug logging"
	}
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// credentialPrompt returns the dialog text, the status label shown while the
// dialog is open, and whether the entry should be masked.
func credentialPrompt(kind, dispName string) (prompt, label string, hide bool) {
	if kind == "pin" {
		return fmt.Sprintf("Enter the PIN shown on %s:", dispName), "Enter PIN for " + dispName + "…", false
	}
	return fmt.Sprintf("Enter the code or password for %s:", dispName), "Enter password for " + dispName + "…", true
}

// pumpChildOutput scans the sender's stdout until it closes, teeing lines to
// tee (the debug log) when non-nil and answering each credential-request
// marker by writing ask's value and a newline to the sender's stdin. A
// cancelled or empty answer sends a blank line so the sender exits cleanly
// instead of hanging. The value is sent untrimmed: the sender preserves
// spaces in a password.
func pumpChildOutput(out io.Reader, in io.Writer, tee io.Writer, ask func(kind string) (string, bool)) childOutcome {
	var outcome childOutcome
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if tee != nil {
			fmt.Fprintln(tee, line)
		}
		if kind, ok := airplay.ParseCredentialRequest(line); ok {
			outcome.sawCred = true
			value, ok := ask(kind)
			if !ok {
				value = ""
			}
			_, _ = io.WriteString(in, value+"\n")
			continue
		}
		if state, ok := airplay.ParseStatus(line); ok && state == "paired" {
			outcome.paired = true
		}
	}
	return outcome
}

// lastLineWriter remembers the last non-empty line written to it, with any
// standard log timestamp prefix removed.
type lastLineWriter struct {
	mu      sync.Mutex
	partial []byte
	last    string
}

func (w *lastLineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.partial = append(w.partial, p...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		w.record(string(w.partial[:i]))
		w.partial = w.partial[i+1:]
	}
	if len(w.partial) > 64*1024 {
		w.partial = w.partial[len(w.partial)-64*1024:]
	}
	return len(p), nil
}

func (w *lastLineWriter) record(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	w.last = stripLogTimestamp(line)
}

// Last returns the final complete (or trailing partial) line.
func (w *lastLineWriter) Last() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if tail := strings.TrimSpace(string(w.partial)); tail != "" {
		return stripLogTimestamp(tail)
	}
	return w.last
}

// stripLogTimestamp removes the "2006/01/02 15:04:05 " prefix the standard
// logger adds.
func stripLogTimestamp(line string) string {
	const layout = "2006/01/02 15:04:05 "
	if len(line) > len(layout) && line[4] == '/' && line[7] == '/' && line[10] == ' ' && line[13] == ':' && line[16] == ':' && line[19] == ' ' {
		return line[len(layout):]
	}
	return line
}
