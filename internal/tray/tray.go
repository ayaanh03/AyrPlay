// Package tray is AyrPlay's system-tray front-end. It discovers AirPlay
// receivers on the network, lists them in a click-to-stream menu, exposes
// quality/buffer/audio/display/debug presets, and drives the command-line
// sender as a hidden subprocess so there are no flags to remember.
package tray

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
	"github.com/ncruces/zenity"

	"doubletake/internal/airplay"
	"doubletake/internal/gstreamer"
)

//go:embed icon.ico
var iconData []byte

const (
	deviceSlots = 12
	idleTooltip = "AyrPlay — AirPlay sender"
)

// Options configures the tray.
type Options struct {
	// SenderPath is the executable launched for each session (AyrPlay passes
	// its own executable).
	SenderPath string
	// SenderArgs precede the sender's -flags, e.g. the "send" subcommand.
	SenderArgs []string
}

type deviceSlot struct {
	item  *systray.MenuItem
	ip    string
	name  string
	model string
}

type app struct {
	opts         Options
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

// Run shows the tray icon and blocks until the user quits. Only one tray runs
// per user session; a second launch tells the user where to find the first.
func Run(opts Options) {
	release, ok := acquireSingleInstance()
	if !ok {
		_ = zenity.Info("AyrPlay is already running. Look for its icon in the system tray (you may need to click ^ to show hidden icons).",
			zenity.Title("AyrPlay"))
		return
	}
	defer release()
	a := &app{opts: opts, set: loadSettings(configPath())}
	systray.Run(a.onReady, a.onExit)
}

func (a *app) onReady() {
	systray.SetIcon(iconData)
	systray.SetTitle("")
	systray.SetTooltip(idleTooltip)

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
	a.q1080 = quality.AddSubMenuItemCheckbox("1080p (Wi-Fi friendly)", "H.264, smooth over Wi-Fi", a.set.Quality == quality1080p)
	a.q4k = quality.AddSubMenuItemCheckbox("4K (wired recommended)", "HEVC at the receiver's max canvas", a.set.Quality == quality4K)
	go func() {
		for range a.q1080.ClickedCh {
			a.setQuality(quality1080p)
		}
	}()
	go func() {
		for range a.q4k.ClickedCh {
			a.setQuality(quality4K)
		}
	}()

	buffer := systray.AddMenuItem("Buffer", "Playout buffer (bigger = smoother, more delay)")
	a.bLow = buffer.AddSubMenuItemCheckbox("Low latency (0.2s)", "", a.set.Buffer == bufferLow)
	a.bSmooth = buffer.AddSubMenuItemCheckbox("Smooth (1s)", "", a.set.Buffer == bufferSmooth)
	a.bMovie = buffer.AddSubMenuItemCheckbox("Movie (2s)", "", a.set.Buffer == bufferMovie)
	go func() {
		for range a.bLow.ClickedCh {
			a.setBuffer(bufferLow)
		}
	}()
	go func() {
		for range a.bSmooth.ClickedCh {
			a.setBuffer(bufferSmooth)
		}
	}()
	go func() {
		for range a.bMovie.ClickedCh {
			a.setBuffer(bufferMovie)
		}
	}()

	display := systray.AddMenuItem("Display", "Which screen to mirror")
	n := displayCount()
	a.set = a.set.normalized(n)
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
			s.save(configPath())
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
			s.save(configPath())
		}
	}()
	openLogs := systray.AddMenuItem("Open logs folder", "Show the debug log and settings folder")
	go func() {
		for range openLogs.ClickedCh {
			_ = os.MkdirAll(dataDir(), 0o755)
			openInShell(dataDir())
		}
	}()
	checkSetup := systray.AddMenuItem("Check setup…", "Check that GStreamer and its plugins are installed")
	go func() {
		for range checkSetup.ClickedCh {
			a.showSetupReport()
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
	quit := systray.AddMenuItem("Quit", "Exit AyrPlay")
	go func() {
		<-quit.ClickedCh
		a.stopMirror()
		systray.Quit()
	}()

	go func() {
		a.scan()
		// Point a first-time user at the one-time GStreamer install before they
		// click a receiver and get an opaque failure. Only the cheap PATH lookup
		// runs here; the full plugin check (which can take seconds while
		// GStreamer builds its registry) waits for "Check setup…".
		if _, err := exec.LookPath("gst-launch-1.0"); err != nil {
			a.setStatusIfIdle(setupStatus(gstreamer.Report{}))
		}
	}()
}

func (a *app) onExit() {
	a.stopMirror()
}

func (a *app) diagnose() gstreamer.Report {
	return gstreamer.Diagnose(gstreamer.SystemEnv(runtime.GOOS, ""))
}

// setupStatus is the status line for an incomplete GStreamer install.
func setupStatus(report gstreamer.Report) string {
	if report.LaunchTool == "" || report.InspectTool == "" {
		return "GStreamer not found — click Check setup…"
	}
	return "GStreamer plugins missing — click Check setup…"
}

// showSetupReport runs the doctor checks and shows the result in a dialog,
// offering the GStreamer download page when something required is missing.
func (a *app) showSetupReport() {
	report := a.diagnose()
	var b strings.Builder
	report.WriteText(&b)
	if report.Ready {
		_ = zenity.Info("Everything AyrPlay needs is installed.\n\n"+b.String(), zenity.Title("AyrPlay — setup"))
		a.setStatusIfIdle("Idle")
		return
	}
	err := zenity.Question(b.String(), zenity.Title("AyrPlay — setup"),
		zenity.OKLabel("Open GStreamer download page"), zenity.CancelLabel("Close"), zenity.WarningIcon)
	if err == nil {
		openInShell(gstreamer.DownloadURL)
	}
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
	a.setStatusIfIdle(scanStatus(len(devices)))
}

func scanStatus(found int) string {
	if found == 0 {
		return "No receivers found"
	}
	return fmt.Sprintf("Idle — %d receiver(s)", found)
}

// startMirror launches the sender targeting ip with the current presets.
func (a *app) startMirror(ip, name, model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopLocked()

	audioOnly := airplay.IsAudioOnlyModel(model)
	args := append(append([]string{}, a.opts.SenderArgs...), senderArgs(a.set, ip, model)...)
	cmd := exec.Command(a.opts.SenderPath, args...)
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

	// stderr carries the sender's log; keep its last line so a failure can be
	// shown in the status line, and tee it to the debug log when enabled.
	lastErr := &lastLineWriter{}
	var logf *os.File
	cmd.Stderr = lastErr
	if a.set.Debug {
		_ = os.MkdirAll(dataDir(), 0o755)
		if f, err := os.OpenFile(logPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
			logf = f
			cmd.Stderr = io.MultiWriter(f, lastErr)
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

	runStatus, runTip := runningStatus(audioOnly, name)
	a.status.SetTitle(runStatus)
	systray.SetTooltip(runTip)
	a.stop.Enable()

	go func(c *exec.Cmd, in io.WriteCloser, out io.ReadCloser, lf *os.File) {
		var tee io.Writer
		if lf != nil {
			tee = lf
		}
		ask := func(kind string) (string, bool) {
			return a.askCredential(c, kind, name, runStatus, dctx)
		}
		outcome := pumpChildOutput(out, in, tee, ask)
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
		systray.SetTooltip(idleTooltip)
		a.status.SetTitle(exitStatus(err, outcome, lastErr.Last()))
	}(cmd, stdin, stdout, logf)
}

// askCredential pops a native dialog for a pairing PIN or password. It touches
// the tray UI only while this session is still the active one, so a stopped or
// superseded session can never clobber the current status. ok is false when
// the dialog was cancelled or left empty.
func (a *app) askCredential(c *exec.Cmd, kind, dispName, runStatus string, dctx context.Context) (string, bool) {
	if !a.isCurrent(c) {
		return "", false
	}
	prompt, label, hide := credentialPrompt(kind, dispName)
	opts := []zenity.Option{zenity.Title("AyrPlay — pairing"), zenity.Context(dctx)}
	if hide {
		opts = append(opts, zenity.HideText())
	}
	a.setStatusIfCurrent(c, label)
	value, err := zenity.Entry(prompt, opts...)
	if err != nil || strings.TrimSpace(value) == "" {
		return "", false
	}
	a.setStatusIfCurrent(c, runStatus)
	return value, true
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
	systray.SetTooltip(idleTooltip)
}

func (a *app) setQuality(q string) {
	a.mu.Lock()
	a.set.Quality = q
	if q == quality1080p {
		a.q1080.Check()
		a.q4k.Uncheck()
	} else {
		a.q4k.Check()
		a.q1080.Uncheck()
	}
	s := a.set
	a.mu.Unlock()
	s.save(configPath())
}

func (a *app) setBuffer(b string) {
	a.mu.Lock()
	a.set.Buffer = b
	a.bLow.Uncheck()
	a.bSmooth.Uncheck()
	a.bMovie.Uncheck()
	switch b {
	case bufferLow:
		a.bLow.Check()
	case bufferMovie:
		a.bMovie.Check()
	default:
		a.bSmooth.Check()
	}
	s := a.set
	a.mu.Unlock()
	s.save(configPath())
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
	s.save(configPath())
}
