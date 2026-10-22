// Command doubletake-tray is a Windows system-tray front-end for doubletake. It
// discovers AirPlay receivers on the network, lists them in a click-to-mirror menu,
// exposes quality/buffer/audio/display/debug presets, and drives the proven
// doubletake.exe binary as a subprocess so there are no flags to remember.
package main

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
	"github.com/ncruces/zenity"

	"doubletake/internal/airplay"
)

//go:embed icon.ico
var iconData []byte

const deviceSlots = 12

// settings is the persisted tray configuration.
type settings struct {
	Quality  string `json:"quality"` // "1080p" or "4k"
	Buffer   string `json:"buffer"`  // "low", "smooth", or "movie"
	AudioOff bool   `json:"audio_off"`
	Debug    bool   `json:"debug"`
	Monitor  int    `json:"monitor"` // 0-based display index
}

func defaultSettings() settings { return settings{Quality: "1080p", Buffer: "smooth"} }

func dataDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "doubletake")
}

func configPath() string { return filepath.Join(dataDir(), "tray.json") }
func logPath() string     { return filepath.Join(dataDir(), "doubletake.log") }

func loadSettings() settings {
	s := defaultSettings()
	if b, err := os.ReadFile(configPath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func (s settings) save() {
	_ = os.MkdirAll(dataDir(), 0o755)
	if b, err := json.MarshalIndent(s, "", "  "); err == nil {
		_ = os.WriteFile(configPath(), b, 0o644)
	}
}

// bufferMs maps a buffer preset to a -target-latency-ms value.
func bufferMs(preset string) int {
	switch preset {
	case "low":
		return 200
	case "movie":
		return 2000
	default: // smooth
		return 1000
	}
}

// doubletakePath prefers doubletake.exe next to this binary, else relies on PATH.
func doubletakePath() string {
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "doubletake.exe")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return "doubletake.exe"
}

type deviceSlot struct {
	item  *systray.MenuItem
	ip    string
	name  string
	model string
}

type app struct {
	mu           sync.Mutex
	set          settings
	cmd          *exec.Cmd
	cancelDialog context.CancelFunc
	slots        []*deviceSlot
	displayItems []*systray.MenuItem
	status       *systray.MenuItem
	stop         *systray.MenuItem
	q1080        *systray.MenuItem
	q4k          *systray.MenuItem
	bLow         *systray.MenuItem
	bSmooth      *systray.MenuItem
	bMovie       *systray.MenuItem
	audio        *systray.MenuItem
	debug        *systray.MenuItem
}

func main() {
	a := &app{set: loadSettings()}
	systray.Run(a.onReady, a.onExit)
}

func (a *app) onReady() {
	systray.SetIcon(iconData)
	systray.SetTitle("")
	systray.SetTooltip("doubletake — AirPlay sender")

	a.status = systray.AddMenuItem("Idle", "Current status")
	a.status.Disable()
	systray.AddSeparator()

	header := systray.AddMenuItem("Receivers", "AirPlay devices found on the network")
	header.Disable()
	for i := 0; i < deviceSlots; i++ {
		it := systray.AddMenuItem("", "")
		it.Hide()
		slot := &deviceSlot{item: it}
		a.slots = append(a.slots, slot)
		go func(sl *deviceSlot) {
			for range sl.item.ClickedCh {
				a.mu.Lock()
				ip, name, model := sl.ip, sl.name, sl.model
				a.mu.Unlock()
				if ip != "" {
					a.startMirror(ip, name, model)
				}
			}
		}(slot)
	}
	rescan := systray.AddMenuItem("Rescan", "Scan the network again")
	go func() {
		for range rescan.ClickedCh {
			a.scan()
		}
	}()

	systray.AddSeparator()

	quality := systray.AddMenuItem("Quality", "Resolution / codec")
	a.q1080 = quality.AddSubMenuItemCheckbox("1080p (Wi-Fi friendly)", "H.264, smooth over Wi-Fi", a.set.Quality == "1080p")
	a.q4k = quality.AddSubMenuItemCheckbox("4K (wired recommended)", "HEVC at the receiver's max canvas", a.set.Quality == "4k")
	go func() {
		for range a.q1080.ClickedCh {
			a.setQuality("1080p")
		}
	}()
	go func() {
		for range a.q4k.ClickedCh {
			a.setQuality("4k")
		}
	}()

	buffer := systray.AddMenuItem("Buffer", "Playout buffer (bigger = smoother, more delay)")
	a.bLow = buffer.AddSubMenuItemCheckbox("Low latency (0.2s)", "", a.set.Buffer == "low")
	a.bSmooth = buffer.AddSubMenuItemCheckbox("Smooth (1s)", "", a.set.Buffer == "smooth")
	a.bMovie = buffer.AddSubMenuItemCheckbox("Movie (2s)", "", a.set.Buffer == "movie")
	go func() {
		for range a.bLow.ClickedCh {
			a.setBuffer("low")
		}
	}()
	go func() {
		for range a.bSmooth.ClickedCh {
			a.setBuffer("smooth")
		}
	}()
	go func() {
		for range a.bMovie.ClickedCh {
			a.setBuffer("movie")
		}
	}()

	display := systray.AddMenuItem("Display", "Which screen to mirror")
	n := displayCount()
	if a.set.Monitor >= n || a.set.Monitor < 0 {
		a.set.Monitor = 0
	}
	for i := 0; i < n; i++ {
		it := display.AddSubMenuItemCheckbox(fmt.Sprintf("Display %d", i+1), "", a.set.Monitor == i)
		a.displayItems = append(a.displayItems, it)
		idx := i
		go func() {
			for range it.ClickedCh {
				a.setMonitor(idx)
			}
		}()
	}

	a.audio = systray.AddMenuItemCheckbox("Stream audio", "Mirror system audio too", !a.set.AudioOff)
	go func() {
		for range a.audio.ClickedCh {
			a.mu.Lock()
			// systray does not auto-toggle a checkbox on click; flip it here.
			if a.audio.Checked() {
				a.audio.Uncheck()
				a.set.AudioOff = true
			} else {
				a.audio.Check()
				a.set.AudioOff = false
			}
			s := a.set
			a.mu.Unlock()
			s.save()
		}
	}()

	a.debug = systray.AddMenuItemCheckbox("Debug logging", "Write a verbose log you can share", a.set.Debug)
	go func() {
		for range a.debug.ClickedCh {
			a.mu.Lock()
			if a.debug.Checked() {
				a.debug.Uncheck()
				a.set.Debug = false
			} else {
				a.debug.Check()
				a.set.Debug = true
			}
			s := a.set
			a.mu.Unlock()
			s.save()
		}
	}()
	openLogs := systray.AddMenuItem("Open logs folder", "Show the debug log in Explorer")
	go func() {
		for range openLogs.ClickedCh {
			_ = os.MkdirAll(dataDir(), 0o755)
			_ = exec.Command("explorer.exe", dataDir()).Start()
		}
	}()

	systray.AddSeparator()
	a.stop = systray.AddMenuItem("Stop mirroring", "Disconnect")
	a.stop.Disable()
	go func() {
		for range a.stop.ClickedCh {
			a.stopMirror()
		}
	}()
	quit := systray.AddMenuItem("Quit", "Exit doubletake")
	go func() {
		<-quit.ClickedCh
		a.stopMirror()
		systray.Quit()
	}()

	go a.scan()
}

func (a *app) onExit() {
	a.stopMirror()
}

// scan discovers receivers and repopulates the device slots.
func (a *app) scan() {
	a.setStatusIfIdle("Scanning…")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	devices, err := airplay.DiscoverAirPlayDevices(ctx)
	if err != nil {
		a.setStatusIfIdle("Scan failed")
		return
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Name < devices[j].Name })

	a.mu.Lock()
	mirroring := a.cmd != nil
	for i, sl := range a.slots {
		if i < len(devices) {
			d := devices[i]
			sl.ip, sl.name, sl.model = d.IP, d.Name, d.Model
			sl.item.SetTitle(d.Name)
			sl.item.SetTooltip(fmt.Sprintf("%s:%d  (%s)", d.IP, d.Port, d.Model))
			sl.item.Show()
		} else {
			sl.ip, sl.name, sl.model = "", "", ""
			sl.item.Hide()
		}
	}
	a.mu.Unlock()

	if mirroring {
		return
	}
	if len(devices) == 0 {
		a.setStatusIfIdle("No receivers found")
	} else {
		a.setStatusIfIdle(fmt.Sprintf("Idle — %d receiver(s)", len(devices)))
	}
}

// startMirror launches doubletake.exe targeting ip with the current presets.
// A HomePod (model prefix "AudioAccessory") gets the audio-only path; every
// other receiver gets the screen-mirror flags exactly as before.
func (a *app) startMirror(ip, name, model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopLocked()

	audioOnly := strings.HasPrefix(model, "AudioAccessory")
	args := []string{
		"-target", ip,
		"-target-latency-ms", fmt.Sprint(bufferMs(a.set.Buffer)),
		"-machine-prompts",
	}
	if audioOnly {
		// No -monitor/-video-codec/-bitrate/-no-audio: there is no video, and the
		// receiver always carries audio.
		args = append(args, "-audio-only")
	} else {
		args = append(args, "-monitor", fmt.Sprint(a.set.Monitor))
		if a.set.Quality == "4k" {
			args = append(args, "-video-codec", "hevc", "-bitrate", "25000")
		} else {
			args = append(args, "-video-codec", "h264")
		}
		if a.set.AudioOff {
			args = append(args, "-no-audio")
		}
	}
	if a.set.Debug {
		args = append(args, "-debug")
	}

	cmd := exec.Command(doubletakePath(), args...)
	cmd.Env = os.Environ()
	hideConsole(cmd)

	// Wire stdin so we can answer a pairing PIN/password request, and capture
	// stdout, where the sender prints machine-readable credential markers.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		a.status.SetTitle("Error: " + err.Error())
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		a.status.SetTitle("Error: " + err.Error())
		return
	}

	var logf *os.File
	if a.set.Debug {
		_ = os.MkdirAll(dataDir(), 0o755)
		if f, err := os.OpenFile(logPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
			logf = f
			cmd.Stderr = f // verbose logs go to stderr; stdout is scanned below
		}
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		if logf != nil {
			logf.Close()
		}
		a.status.SetTitle("Error: " + err.Error())
		return
	}
	a.cmd = cmd

	// Per-session context so a pending PIN/password dialog is dismissed if this
	// session is stopped or replaced while the dialog is still open.
	dctx, dcancel := context.WithCancel(context.Background())
	a.cancelDialog = dcancel

	var runStatus, runTip string
	if audioOnly {
		runStatus, runTip = "Streaming audio → "+name, "doubletake — streaming audio to "+name
	} else {
		runStatus, runTip = "Mirroring → "+name, "doubletake — mirroring to "+name
	}
	a.status.SetTitle(runStatus)
	systray.SetTooltip(runTip)
	a.stop.Enable()

	go func(c *exec.Cmd, in io.WriteCloser, out io.ReadCloser, lf *os.File) {
		outcome := a.pumpChildOutput(c, out, in, lf, name, runStatus, dctx)
		err := c.Wait()
		in.Close()
		dcancel()
		if lf != nil {
			lf.Close()
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.cmd != c {
			return // superseded or stopped; that path already set the status
		}
		a.cmd = nil
		a.cancelDialog = nil
		a.stop.Disable()
		systray.SetTooltip("doubletake — AirPlay sender")
		switch {
		case err == nil:
			a.status.SetTitle("Idle")
		case outcome.sawCred && !outcome.paired:
			a.status.SetTitle("Pairing didn't complete — click the device to retry")
		default:
			a.status.SetTitle("Stopped (error) — enable Debug logging")
		}
	}(cmd, stdin, stdout, logf)
}

// childOutcome records what the sender's output stream revealed about a session.
type childOutcome struct {
	sawCred bool // a pairing PIN/password was requested
	paired  bool // pairing completed (so a later error is not a pairing failure)
}

// pumpChildOutput scans the sender's stdout until it closes, teeing lines to the
// debug log when enabled and answering any credential-request marker with a
// PIN/password dialog whose value is written back to the sender's stdin.
func (a *app) pumpChildOutput(c *exec.Cmd, out io.Reader, in io.Writer, lf *os.File, dispName, runStatus string, dctx context.Context) childOutcome {
	var outcome childOutcome
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if lf != nil {
			fmt.Fprintln(lf, line)
		}
		if kind, ok := airplay.ParseCredentialRequest(line); ok {
			outcome.sawCred = true
			a.answerCredentialRequest(c, kind, in, dispName, runStatus, dctx)
			continue
		}
		if state, ok := airplay.ParseStatus(line); ok && state == "paired" {
			outcome.paired = true
		}
	}
	return outcome
}

// answerCredentialRequest pops a native dialog for a pairing PIN or password and
// writes the entered value (and a newline) to the sender's stdin. It touches the
// tray UI only while this session is still the active one, so a stopped or
// superseded session can never clobber the current status. A cancelled or empty
// entry sends a blank line so the sender exits cleanly instead of hanging.
func (a *app) answerCredentialRequest(c *exec.Cmd, kind string, in io.Writer, dispName, runStatus string, dctx context.Context) {
	if !a.isCurrent(c) {
		_, _ = io.WriteString(in, "\n")
		return
	}

	opts := []zenity.Option{zenity.Title("AyrPlay — pairing"), zenity.Context(dctx)}
	var prompt, label string
	if kind == "pin" {
		prompt = fmt.Sprintf("Enter the PIN shown on %s:", dispName)
		label = "Enter PIN for " + dispName + "…"
	} else {
		prompt = fmt.Sprintf("Enter the code or password for %s:", dispName)
		label = "Enter password for " + dispName + "…"
		opts = append(opts, zenity.HideText())
	}
	a.setStatusIfCurrent(c, label)

	value, err := zenity.Entry(prompt, opts...)
	// The CLI preserves spaces in a credential, so send the value untrimmed;
	// only use a trimmed copy to detect an empty or cancelled entry.
	if err != nil || strings.TrimSpace(value) == "" {
		_, _ = io.WriteString(in, "\n")
		return
	}
	_, _ = io.WriteString(in, value+"\n")
	a.setStatusIfCurrent(c, runStatus)
}

// isCurrent reports whether c is still the active session.
func (a *app) isCurrent(c *exec.Cmd) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cmd == c
}

// setStatusIfCurrent updates the status line only while c is the active session.
func (a *app) setStatusIfCurrent(c *exec.Cmd, title string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd == c {
		a.status.SetTitle(title)
	}
}

// setStatusIfIdle updates the status line only when no mirror/stream is running,
// so a background rescan never overwrites a live session's status.
func (a *app) setStatusIfIdle(title string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd == nil {
		a.status.SetTitle(title)
	}
}

func (a *app) stopMirror() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopLocked()
}

// stopLocked terminates any running mirror. Caller must hold a.mu.
func (a *app) stopLocked() {
	if a.cancelDialog != nil {
		a.cancelDialog()
		a.cancelDialog = nil
	}
	if a.cmd == nil || a.cmd.Process == nil {
		return
	}
	c := a.cmd
	a.cmd = nil
	_ = c.Process.Kill()
	a.status.SetTitle("Idle")
	a.stop.Disable()
	systray.SetTooltip("doubletake — AirPlay sender")
}

func (a *app) setQuality(q string) {
	a.mu.Lock()
	a.set.Quality = q
	if q == "1080p" {
		a.q1080.Check()
		a.q4k.Uncheck()
	} else {
		a.q4k.Check()
		a.q1080.Uncheck()
	}
	s := a.set
	a.mu.Unlock()
	s.save()
}

func (a *app) setBuffer(b string) {
	a.mu.Lock()
	a.set.Buffer = b
	a.bLow.Uncheck()
	a.bSmooth.Uncheck()
	a.bMovie.Uncheck()
	switch b {
	case "low":
		a.bLow.Check()
	case "movie":
		a.bMovie.Check()
	default:
		a.bSmooth.Check()
	}
	s := a.set
	a.mu.Unlock()
	s.save()
}

func (a *app) setMonitor(idx int) {
	a.mu.Lock()
	a.set.Monitor = idx
	for i, it := range a.displayItems {
		if i == idx {
			it.Check()
		} else {
			it.Uncheck()
		}
	}
	s := a.set
	a.mu.Unlock()
	s.save()
}
