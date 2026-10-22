// Package sender is the doubletake/AyrPlay command-line AirPlay sender: flag
// parsing, discovery, pairing, and the mirror and audio-only streaming loops.
package sender

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"doubletake/internal/airplay"
	"doubletake/internal/daemon"
)

func parseXID(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	return strconv.ParseUint(s, 0, 64) // accepts decimal or 0xhex
}

// parsePortRange parses a "min-max" string into inclusive port bounds.
// An empty string returns (0, 0, nil) meaning "let the OS pick".
func parsePortRange(s string) (int, int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, nil
	}
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected MIN-MAX, got %q", s)
	}
	lo, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("min: %w", err)
	}
	hi, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("max: %w", err)
	}
	if lo < 1 || hi > 65535 || lo > hi {
		return 0, 0, fmt.Errorf("range %d-%d out of bounds (1-65535, min<=max)", lo, hi)
	}
	if hi-lo+1 < 3 {
		return 0, 0, fmt.Errorf("range %d-%d too small; need 3 consecutive UDP ports", lo, hi)
	}
	return lo, hi, nil
}

// machinePromptMode makes credential prompts machine-readable for a GUI
// front-end: a marker line on stdout, with the value read as a bare line from
// stdin. It is set once from the -machine-prompts flag at startup.
var machinePromptMode bool

// options is the parsed and validated sender command line.
type options struct {
	target          string
	port            int
	credential      string // -code, overridden by $DOUBLETAKE_CODE
	credFile        string
	credBackend     string
	forcePair       bool
	fps             int
	bitrate         int
	targetLatencyMs int
	hwaccel         string
	videoCodec      string
	testMode        bool
	noEncrypt       bool
	directKey       bool
	noAudio         bool
	audioOnly       bool
	portMin         int
	portMax         int
	debug           bool
	daemonize       bool
	socketPath      string
	x11WindowID     uint64
	x11WindowName   string
	noCursor        bool
	monitorIndex    int
	machinePrompts  bool
}

// rawFlags holds flag values that are parsed further after flag.Parse.
type rawFlags struct {
	code        string
	portRange   string
	x11WindowID string
}

// errUsage marks a command-line parse error the flag package already printed.
var errUsage = errors.New("usage error")

func newFlagSet(prog string, opts *options, raw *rawFlags) *flag.FlagSet {
	flags := flag.NewFlagSet(prog, flag.ContinueOnError)
	flags.StringVar(&opts.target, "target", "", "Apple TV IP address or hostname (skip discovery)")
	flags.IntVar(&opts.port, "port", 7000, "AirPlay port")
	flags.StringVar(&raw.code, "code", "", "Pairing PIN shown on the receiver, or the password set on it when \"Require Password\" is enabled; prefer $DOUBLETAKE_CODE so it stays out of shell history and ps output")
	flags.StringVar(&opts.credFile, "creds", airplay.DefaultCredentialsPath(), "Path to saved pairing credentials")
	flags.StringVar(&opts.credBackend, "cred-backend", "file", "Credential storage backend: file or keyring (system keyring via Secret Service)")
	flags.BoolVar(&opts.forcePair, "pair", false, "Force new pairing even if credentials exist")
	flags.IntVar(&opts.fps, "fps", 30, "Frames per second")
	flags.IntVar(&opts.bitrate, "bitrate", 0, "Video bitrate in kbps (0 = auto, default tunes for resolution/FPS)")
	flags.IntVar(&opts.targetLatencyMs, "target-latency-ms", 0, "Joint audio/video playout latency override in milliseconds (0 = automatic connection profile)")
	flags.StringVar(&opts.hwaccel, "hwaccel", "auto", "Encoder: auto, nvenc, vaapi, openh264, none (x264/x265)")
	flags.StringVar(&opts.videoCodec, "video-codec", "auto", "Screen codec: auto, h264, or hevc (auto uses H.264 for receiver compatibility)")
	flags.BoolVar(&opts.testMode, "test", false, "Use synthetic video (videotestsrc) instead of screen capture for debugging")
	flags.BoolVar(&opts.noEncrypt, "no-encrypt", false, "Disable RTSP header encryption (debugging only; video frames are always encrypted)")
	flags.BoolVar(&opts.directKey, "direct-key", false, "Use shk/shiv directly without SHA-512 derivation")
	flags.BoolVar(&opts.noAudio, "no-audio", false, "Disable audio streaming")
	flags.BoolVar(&opts.audioOnly, "audio-only", false, "Stream system audio only (HomePod/AirPlay speaker): no screen capture, encoder, or video stream")
	flags.StringVar(&raw.portRange, "port-range", "", "Local UDP port range for receiver timing/audio (e.g. \"60000-60010\"); empty = OS ephemeral. Needs at least 3 ports.")
	flags.BoolVar(&opts.debug, "debug", false, "Enable verbose debug logging")
	flags.BoolVar(&opts.daemonize, "daemonize", false, "Run as background daemon with Unix socket control interface")
	flags.StringVar(&opts.socketPath, "socket", daemon.DefaultSocketPath(), "Unix socket path for daemon control interface")
	flags.StringVar(&raw.x11WindowID, "x11-window-id", "", "X11 window id to capture, decimal or 0xhex")
	flags.StringVar(&opts.x11WindowName, "x11-window-name", "", "X11 window name to capture; prefer -x11-window-id")
	flags.BoolVar(&opts.noCursor, "no-cursor", false, "Don't show the mouse cursor in the captured video")
	flags.IntVar(&opts.monitorIndex, "monitor", 0, "Windows: 0-based index of the display to mirror")
	flags.BoolVar(&opts.machinePrompts, "machine-prompts", false, "Emit machine-readable credential-request markers on stdout and read credentials as bare lines from stdin (used by the GUI front-end)")
	return flags
}

// PrintDefaults writes the sender's flag reference to w.
func PrintDefaults(w io.Writer) {
	var opts options
	var raw rawFlags
	flags := newFlagSet("", &opts, &raw)
	flags.SetOutput(w)
	flags.PrintDefaults()
}

// parseOptions parses and validates the sender flags. getenv supplies
// $DOUBLETAKE_CODE so tests do not depend on the real environment; flag errors
// and -h usage are written to output.
func parseOptions(prog string, args []string, getenv func(string) string, output io.Writer) (options, error) {
	var opts options
	var raw rawFlags
	flags := newFlagSet(prog, &opts, &raw)
	flags.SetOutput(output)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return opts, err
		}
		return opts, fmt.Errorf("%w: %v", errUsage, err)
	}
	if flags.NArg() > 0 {
		return opts, fmt.Errorf("unexpected argument %q (all options are -flags)", flags.Arg(0))
	}
	if err := airplay.ValidateHWAccel(opts.hwaccel); err != nil {
		return opts, fmt.Errorf("invalid -hwaccel: %v", err)
	}
	if err := airplay.ValidateVideoCodec(opts.videoCodec); err != nil {
		return opts, fmt.Errorf("invalid -video-codec: %v", err)
	}
	var err error
	opts.portMin, opts.portMax, err = parsePortRange(raw.portRange)
	if err != nil {
		return opts, fmt.Errorf("invalid -port-range: %v", err)
	}
	if opts.audioOnly && opts.noAudio {
		return opts, fmt.Errorf("-audio-only and -no-audio are mutually exclusive")
	}
	if opts.monitorIndex < 0 {
		return opts, fmt.Errorf("invalid -monitor %d: must be 0 or greater", opts.monitorIndex)
	}
	opts.x11WindowID, err = parseXID(raw.x11WindowID)
	if err != nil {
		return opts, fmt.Errorf("invalid -x11-window-id: %v", err)
	}
	// The environment wins over the flag: it keeps the code out of shell history
	// and out of `ps`, where a command line is readable by other users.
	opts.credential = raw.code
	if env := getenv("DOUBLETAKE_CODE"); env != "" {
		opts.credential = env
	}
	return opts, nil
}

// Main runs the sender command line. args excludes the program name; prog is
// the name shown in usage. Like a main function, it exits the process on fatal
// errors.
func Main(prog string, args []string) {
	opts, err := parseOptions(prog, args, os.Getenv, os.Stderr)
	switch {
	case errors.Is(err, flag.ErrHelp):
		os.Exit(0)
	case errors.Is(err, errUsage):
		os.Exit(2)
	case err != nil:
		log.Fatal(err)
	}
	credential := opts.credential

	airplay.SetTargetLatency(time.Duration(opts.targetLatencyMs) * time.Millisecond)

	airplay.SetDebugMode(opts.debug)
	machinePromptMode = opts.machinePrompts

	if opts.daemonize {
		runDaemon(daemon.Config{
			SocketPath:  opts.socketPath,
			CredFile:    opts.credFile,
			CredBackend: opts.credBackend,
			FPS:         opts.fps,
			Bitrate:     opts.bitrate,
			PortMin:     opts.portMin,
			PortMax:     opts.portMax,
			HWAccel:     opts.hwaccel,
			VideoCodec:  airplay.VideoCodec(opts.videoCodec),
			Debug:       opts.debug,
			TestMode:    opts.testMode,
			NoEncrypt:   opts.noEncrypt,
			DirectKey:   opts.directKey,
			NoAudio:     opts.noAudio,
			ShowCursor:  !opts.noCursor,
			Code:        credential,
		})
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("shutting down...")
		cancel()
		// Give goroutines a moment to clean up, then force exit
		go func() {
			time.Sleep(3 * time.Second)
			log.Println("forced exit (timeout)")
			os.Exit(1)
		}()
		// Also force exit on second signal
		<-sigCh
		log.Println("forced exit")
		os.Exit(1)
	}()

	var addr string
	var advertisement *airplay.AirPlayDevice
	if opts.target != "" {
		addr = opts.target
	} else {
		device, err := selectDevice(ctx)
		if err != nil {
			log.Fatalf("discovery failed: %v", err)
		}
		addr = device.IP
		opts.port = device.Port
		advertisement = device
		fmt.Printf("selected: %s (%s:%d)\n", device.Name, device.IP, device.Port)
	}

	newClient := func() *airplay.AirPlayClient {
		if advertisement != nil {
			device := *advertisement
			device.IP = addr
			device.Port = opts.port
			return airplay.NewAirPlayClientForDevice(device)
		}
		return airplay.NewAirPlayClient(addr, opts.port)
	}
	client := newClient()
	client.SetPassword(credential)
	if err := client.Connect(ctx); err != nil {
		log.Fatalf("connect failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	info, err := client.GetInfo()
	if err != nil {
		log.Fatalf("get info failed: %v", err)
	}
	log.Printf("connected to: %s (model: %s, initialVolume: %.1f)", info.Name, info.Model, info.InitialVolume)
	if credential == "" && info.RequiresPassword() {
		credential = readCredential(bufio.NewReader(os.Stdin), "Enter the receiver's configured password: ", "password")
		if credential == "" {
			log.Fatal("password cannot be empty")
		}
		client.SetPassword(credential)
	}

	// Pairing flow:
	// 1. If --pair is forced, do full pair-setup and save credentials.
	// 2. If saved credentials exist, load them and do pair-verify only.
	// 3. Pair directly when a legacy receiver requires its configured password,
	//    or when the receiver advertises one-time PIN pairing.
	// 4. Otherwise, try transient pairing and use a fresh connection for fallback.
	//
	// -code alone does not force pairing. A receiver with "Require Password"
	// needs the code on every session, so treating its presence as "pair again"
	// would re-pair on every run and throw away working credentials. When a
	// pairing PIN is what is actually wanted, -pair asks for one.
	needFullPair := opts.forcePair

	credStore, err := newCredentialStore(opts.credBackend, opts.credFile)
	if err != nil {
		log.Fatalf("failed to load credentials: %v", err)
	}

	var savedCreds *airplay.SavedCredentials
	if !needFullPair {
		savedCreds = credStore.Lookup(info.DeviceID)
	}

	reconnect := func() {
		_ = client.Close()
		client = newClient()
		client.SetPassword(credential)
		if err := client.Connect(ctx); err != nil {
			log.Fatalf("reconnect failed: %v", err)
		}
		var err error
		info, err = client.GetInfo()
		if err != nil {
			log.Fatalf("get info after reconnect failed: %v", err)
		}
	}

	savePairingCredentials := func() {
		if client.PairKeys == nil {
			return
		}
		if err := credStore.SavePairing(info.DeviceID, client.PairingID,
			client.PairKeys.Ed25519Public, client.PairKeys.Ed25519Private,
			client.PairingProtocol()); err != nil {
			log.Printf("warning: failed to save credentials: %v", err)
		} else {
			log.Printf("credentials saved (%s)", opts.credBackend)
		}
	}
	pairWithCredential := func(value string, expectPIN bool) {
		if value == "" {
			value = credentialOrPrompt(credential, client, expectPIN)
		}
		// AirPlay uses the same user-entered value for pair-setup and, on
		// password-protected receivers, the later HTTP Digest challenge.
		// Keep it for reconnects as well as the current connection.
		credential = value
		if err := client.Pair(ctx, value); err != nil {
			log.Fatalf("pairing failed: %v", err)
		}
		savePairingCredentials()
	}
	if needFullPair {
		if forcePairUsesTransient(info) {
			// A modern receiver's fixed playback password belongs only to HTTP
			// Digest. Passing it to full SRP pair-setup is rejected as a bad PIN,
			// even when -pair was requested. Keep the one entered value for Digest
			// and establish this session with transient HAP pairing.
			if err := client.Pair(ctx, ""); err != nil {
				log.Fatalf("transient pairing failed for password-protected receiver: %v", err)
			}
		} else {
			// Full pair-setup with a supplied PIN/password, or request an onscreen PIN.
			pairWithCredential(credential, !info.RequiresPassword())
		}
	} else if savedCreds != nil && savedCreds.HasPairingCredentials() {
		// Use saved credentials — pair-verify
		log.Printf("using saved credentials (%s)", opts.credBackend)
		verifyErr := restoreSavedPairing(client, savedCreds)
		if verifyErr == nil {
			verifyErr = client.PairVerify(ctx)
		}
		if verifyErr != nil {
			log.Printf("pair-verify with saved creds failed: %v", verifyErr)
			reconnect()
			if passwordRequiresPairing(info) {
				pairWithCredential("", false)
			} else if info.RequiredPairingCredential() == airplay.PairingCredentialPIN {
				pairWithCredential("", true)
			} else if err := client.Pair(ctx, ""); err != nil {
				if info.RequiresPassword() {
					log.Fatalf("transient pairing failed for password-protected receiver: %v", err)
				}
				log.Printf("transient pairing fallback failed: %v, requesting pairing credentials", err)
				// A failed pairing exchange may leave receiver state on this socket.
				// Start and finish credential pairing together on a clean connection.
				reconnect()
				pairWithCredential("", false)
			}
		}
	} else {
		if passwordRequiresPairing(info) {
			pairWithCredential("", false)
		} else if info.RequiredPairingCredential() == airplay.PairingCredentialPIN {
			pairWithCredential("", true)
		} else if err := client.Pair(ctx, ""); err != nil {
			if info.RequiresPassword() {
				log.Fatalf("transient pairing failed for password-protected receiver: %v", err)
			}
			log.Printf("transient pairing failed: %v, requesting pairing credentials", err)
			reconnect()
			pairWithCredential("", false)
		}
	}
	log.Println("pairing complete")
	if machinePromptMode {
		// Tell a front-end that pairing succeeded, so a later streaming error is
		// not misreported as a pairing failure.
		fmt.Println(airplay.StatusLine("paired"))
	}

	// FairPlay setup — establishes fp-setup state and ekey/eiv used for the
	// final encrypted mirror stream. Pair-verify and FairPlay are both needed
	// for compatibility with the normal modern flow.
	if client.FpEkey == nil {
		if err := client.FairPlaySetup(ctx); err != nil {
			if !errors.Is(err, airplay.ErrFairPlayUnsupported) {
				log.Fatalf("FairPlay setup failed: %v", err)
			}
			log.Printf("FairPlay SAP unsupported (%v); continuing with pair-verify DataStream setup", err)
		} else {
			log.Println("FairPlay setup complete")
		}
	}

	// Audio-only (HomePod/AirPlay speaker): no screen capture, encoder, or video
	// stream. Set up the audio session, capture system audio via WASAPI loopback,
	// stream it, and return — leaving the mirror path below completely untouched.
	if opts.audioOnly {
		session, err := client.SetupAudio(ctx, airplay.StreamConfig{
			NoEncrypt: opts.noEncrypt,
			DirectKey: opts.directKey,
			PortMin:   opts.portMin,
			PortMax:   opts.portMax,
		})
		if err != nil {
			log.Fatalf("audio setup failed: %v", err)
		}
		defer session.Close()
		if !session.HasAudio() {
			log.Fatal("receiver did not provide an audio stream")
		}
		go func() {
			<-ctx.Done()
			session.Close()
		}()
		audioCapture, err := airplay.StartAudioCapture(ctx, opts.testMode, session.AudioCodec())
		if err != nil {
			log.Fatalf("audio capture failed: %v", err)
		}
		defer audioCapture.Stop()
		log.Println("audio capture started (audio-only)")
		if err := session.StreamAudio(ctx, audioCapture, session.AudioStream()); err != nil && ctx.Err() == nil {
			log.Printf("audio streaming error: %v", err)
		}
		return
	}

	streamCfg := airplay.StreamConfig{
		FPS:        opts.fps,
		Bitrate:    opts.bitrate,
		VideoCodec: airplay.VideoCodec(opts.videoCodec),
		NoEncrypt:  opts.noEncrypt,
		DirectKey:  opts.directKey,
		NoAudio:    opts.noAudio,
		PortMin:    opts.portMin,
		PortMax:    opts.portMax,
	}
	// Complete the potentially interactive Wayland portal request before SETUP,
	// but delay encoder startup until control SETUP returns session-time display
	// information. Some current receivers omit displays before that session
	// exists; committing the pipeline earlier silently selects the 720p fallback.
	captureCfg := airplay.CaptureConfig{
		FPS:           opts.fps,
		Bitrate:       opts.bitrate,
		HWAccel:       opts.hwaccel,
		VideoCodec:    airplay.VideoCodec(opts.videoCodec),
		X11WindowID:   opts.x11WindowID,
		X11WindowName: opts.x11WindowName,
		ShowCursor:    !opts.noCursor,
		MonitorIndex:  opts.monitorIndex,
	}
	if opts.testMode {
		if opts.noAudio {
			log.Println("using synthetic video (videotestsrc) for debugging")
		} else {
			log.Println("using synthetic video (videotestsrc) and audio test tone for debugging")
		}
	}
	prepareCapture := func(cfg airplay.CaptureConfig) (*airplay.CapturePreparation, error) {
		if opts.testMode {
			return airplay.PrepareTestCapture(ctx, cfg)
		}
		restoreToken := ""
		if creds := credStore.Lookup(info.DeviceID); creds != nil {
			restoreToken = creds.RestoreToken
		}
		cfg.RestoreToken = restoreToken
		cfg.SaveRestoreToken = func(token string) error {
			return credStore.SaveRestoreToken(info.DeviceID, token)
		}
		return airplay.PrepareCapture(ctx, cfg)
	}
	capturePreparation, err := prepareCapture(captureCfg)
	if err != nil {
		log.Fatalf("prepare screen capture: %v", err)
	}
	streamCfg.AutomaticHEVCAvailable = capturePreparation.AutomaticHEVCAvailable()
	streamCfg.MeasuredVideoLatency = capturePreparation.MeasuredVideoLatency()

	var capture *airplay.ScreenCapture
	var broadcast *airplay.BroadcastCapture
	var broadcastDone chan error
	startedWidth, startedHeight := -1, -1
	startedCodec := airplay.VideoCodec("")
	var liveVideoLead time.Duration
	prepareH264Fallback := func(cause error) error {
		fallbackCfg := captureCfg
		fallbackCfg.VideoCodec = airplay.VideoCodecH264
		replacement, fallbackErr := prepareCapture(fallbackCfg)
		if fallbackErr != nil {
			return fmt.Errorf("%v; prepare H.264 fallback: %w", cause, fallbackErr)
		}
		capturePreparation.Close()
		capturePreparation = replacement
		liveVideoLead = 0
		return fmt.Errorf("%w: %v", airplay.ErrAutomaticVideoCodecUnavailable, cause)
	}
	prepareVideo := func(width, height int, codec airplay.VideoCodec) (airplay.VideoPreparationResult, error) {
		if capture != nil {
			if codec == startedCodec {
				if width != startedWidth || height != startedHeight {
					log.Printf("receiver updated the %s canvas to %dx%d after capture started; keeping %dx%d for this session", codec, width, height, startedWidth, startedHeight)
				}
				return airplay.VideoPreparationResult{MinimumVideoLead: liveVideoLead}, nil
			}
			return airplay.VideoPreparationResult{}, fmt.Errorf("receiver changed video from %s %dx%d to %s %dx%d during setup", startedCodec, startedWidth, startedHeight, codec, width, height)
		}
		startedCapture, startErr := capturePreparation.StartWithCodec(width, height, codec)
		if startErr != nil {
			if captureCfg.VideoCodec == airplay.VideoCodecAuto && codec == airplay.VideoCodecHEVC {
				return airplay.VideoPreparationResult{}, prepareH264Fallback(startErr)
			}
			return airplay.VideoPreparationResult{}, startErr
		}
		if codec == airplay.VideoCodecHEVC && !airplay.HasExplicitTargetLatency() {
			liveVideoLead, startErr = airplay.MeasureVideoCaptureLatency(ctx, startedCapture, opts.fps)
			if startErr != nil {
				startedCapture.Stop()
				measurementErr := fmt.Errorf("measure production HEVC timing: %w", startErr)
				if captureCfg.VideoCodec == airplay.VideoCodecAuto {
					return airplay.VideoPreparationResult{}, prepareH264Fallback(measurementErr)
				}
				return airplay.VideoPreparationResult{}, measurementErr
			}
			log.Printf("[CAPTURE] production HEVC timing requires at least %v video lead", liveVideoLead)
		}
		activeBroadcast := airplay.NewBroadcastCaptureWithFrameRate(startedCapture, opts.fps)
		done := make(chan error, 1)
		capture = startedCapture
		broadcast = activeBroadcast
		broadcastDone = done
		startedWidth, startedHeight = width, height
		startedCodec = codec
		go func(active *airplay.BroadcastCapture, result chan<- error) {
			result <- active.Run()
		}(activeBroadcast, done)
		log.Printf("screen capture started at %dx%d using %s", width, height, codec)
		return airplay.VideoPreparationResult{MinimumVideoLead: liveVideoLead}, nil
	}
	defer func() {
		capturePreparation.Close()
		if capture != nil {
			capture.Stop()
			<-broadcastDone
		}
	}()

	session, err := client.SetupMirrorWithCalibratedVideoPreparation(ctx, streamCfg, prepareVideo)
	if errors.Is(err, airplay.ErrCredentialsRequired) {
		// Some legacy receivers do not advertise their configured password in
		// /info. They reveal it only by challenging the first media SETUP. Keep
		// the completed pairing/FairPlay state and running capture, configure the
		// cached Digest challenge, and retry this setup exactly once.
		credential = readCredential(bufio.NewReader(os.Stdin), "Enter the code shown on the receiver, or its configured password: ", "password")
		if credential == "" {
			log.Fatal("receiver code/password cannot be empty")
		}
		client.SetPassword(credential)
		if startedCodec != "" {
			// A late Digest challenge may expose richer authenticated display
			// metadata on the retry. The running encoder is single-use, so pin
			// the already safe concrete codec for the remainder of this session.
			streamCfg.VideoCodec = startedCodec
		}
		session, err = client.SetupMirrorWithCalibratedVideoPreparation(ctx, streamCfg, prepareVideo)
	}
	if err != nil {
		log.Fatalf("mirror setup failed: %v", err)
	}
	if capture == nil || broadcast == nil {
		log.Fatal("mirror setup completed without preparing video capture")
	}
	defer session.Close()
	log.Printf("mirror session ready (data port: %d)", session.DataPort)

	go func() {
		<-ctx.Done()
		capture.Stop()
		session.Close()
	}()

	// Start audio capture and streaming unless disabled.
	if !opts.noAudio && session.HasAudio() {
		audioCapture, err := airplay.StartAudioCapture(ctx, opts.testMode, session.AudioCodec())
		if err != nil {
			log.Printf("warning: audio capture failed: %v (continuing without audio)", err)
		} else {
			defer audioCapture.Stop()
			go func() {
				if err := session.StreamAudio(ctx, audioCapture, session.AudioStream()); err != nil && ctx.Err() == nil {
					log.Printf("audio streaming error: %v", err)
				}
			}()
			log.Println("audio capture started")
		}
	} else if !opts.noAudio {
		log.Println("audio disabled (receiver did not provide audio ports)")
	}

	// Keep the capture pipeline draining until the receiver is ready to consume
	// video. Holding its first access unit during setup can exhaust the portal
	// buffer pool. StreamFrames waits for the next parameter sets and keyframe.
	videoSink, err := broadcast.AddBackpressuredSink()
	if err != nil {
		log.Fatalf("attach video stream: %v", err)
	}
	defer videoSink.Close()
	if err := session.StreamFrames(ctx, videoSink.AsCapture(), 0*time.Second); err != nil && ctx.Err() == nil {
		log.Printf("streaming error: %v", err)
		return
	}
	log.Println("stream ended")
}

// credentialOrPrompt returns a supplied credential or makes one pairing-display
// request and prompts once. expectPIN is true only when the receiver advertised
// an on-screen PIN (or the user explicitly requested fresh pairing). A successful
// /pair-pin-start response alone cannot distinguish a PIN from a fixed password,
// so an unadvertised fallback keeps its prompt deliberately generic.
func credentialOrPrompt(value string, client *airplay.AirPlayClient, expectPIN bool) string {
	if value != "" {
		return value
	}

	reader := bufio.NewReader(os.Stdin)
	displayErr := client.StartPINDisplay()
	if displayErr != nil {
		log.Printf("warning: failed to trigger PIN display: %v", displayErr)
	}
	kind := "password"
	if expectPIN && displayErr == nil {
		kind = "pin"
	}
	credential := readCredential(reader, pairingCredentialPrompt(expectPIN, displayErr), kind)
	if credential == "" {
		log.Fatal("pairing credential cannot be empty")
	}
	return credential
}

func pairingCredentialPrompt(expectPIN bool, displayErr error) string {
	if expectPIN && displayErr == nil {
		return "Enter the PIN shown on the receiver: "
	}
	return "Enter the receiver's configured password or pairing PIN: "
}

func passwordRequiresPairing(info *airplay.ReceiverInfo) bool {
	// A failed transient exchange can put a legacy password pairer into an error
	// state, so receivers advertising that pairing generation start with SRP.
	// Modern receivers retain the password only for Digest, even when their
	// status flags also advertise on-screen PIN pairing.
	return info.RequiredPairingCredential() == airplay.PairingCredentialPassword
}

func restoreSavedPairing(client *airplay.AirPlayClient, saved *airplay.SavedCredentials) error {
	return client.RestorePairingCredentials(saved)
}

func forcePairUsesTransient(info *airplay.ReceiverInfo) bool {
	return info != nil && info.RequiresPassword() &&
		info.RequiredPairingCredential() == airplay.PairingCredentialNone
}

func readCredential(reader *bufio.Reader, prompt, kind string) string {
	if machinePromptMode {
		// Front-end protocol: announce the request on stdout so a GUI can pop a
		// dialog, then read the entered value as a bare line from stdin.
		fmt.Println(airplay.CredentialRequestLine(kind))
	} else {
		fmt.Print(prompt)
	}
	// Read the whole line rather than using fmt.Scanln, which stops at the
	// first space and would silently truncate a password containing one.
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		log.Fatalf("failed to read credential: %v", err)
	}
	line = strings.TrimRight(line, "\r\n")
	return line
}

func selectDevice(ctx context.Context) (*airplay.AirPlayDevice, error) {
	fmt.Println("searching for Apple TVs...")
	discoverCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	devices, err := airplay.DiscoverAirPlayDevices(discoverCtx)
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("no Apple TVs found")
	}

	sort.Slice(devices, func(i, j int) bool {
		return compareIPs(devices[i].IP, devices[j].IP) < 0
	})

	fmt.Println("\navailable devices:")
	for i, d := range devices {
		fmt.Printf("  [%d] %s (%s) - %s\n", i+1, d.Name, d.Model, d.IP)
	}

	fmt.Print("\nselect device [1]: ")
	var input string
	fmt.Scanln(&input)
	input = strings.TrimSpace(input)
	if input == "" {
		return &devices[0], nil
	}

	idx, err := strconv.Atoi(input)
	if err != nil || idx < 1 || idx > len(devices) {
		return nil, fmt.Errorf("invalid selection")
	}
	return &devices[idx-1], nil
}

// compareIPs compares two IP address strings numerically.
func compareIPs(a, b string) int {
	ipA := net.ParseIP(a)
	ipB := net.ParseIP(b)
	if ipA == nil && ipB == nil {
		return strings.Compare(a, b)
	}
	if ipA == nil {
		return 1
	}
	if ipB == nil {
		return -1
	}
	aBytes := ipA.To16()
	bBytes := ipB.To16()
	for i := range aBytes {
		if aBytes[i] < bBytes[i] {
			return -1
		}
		if aBytes[i] > bBytes[i] {
			return 1
		}
	}
	return 0
}

func runDaemon(cfg daemon.Config) {
	d, err := daemon.New(cfg)
	if err != nil {
		log.Fatalf("[daemon] %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("[daemon] shutting down...")
		cancel()
		d.Shutdown()
		<-sigCh
		log.Println("[daemon] forced exit")
		os.Exit(1)
	}()

	if err := d.Run(ctx); err != nil {
		log.Fatalf("[daemon] %v", err)
	}
}

func newCredentialStore(backend, filePath string) (*airplay.CredentialStore, error) {
	switch backend {
	case "keyring":
		kb, err := airplay.NewKeyringBackend()
		if err != nil {
			return nil, err
		}
		return airplay.NewCredentialStoreWithBackend(kb), nil
	case "file":
		return airplay.NewCredentialStore(filePath)
	default:
		return nil, fmt.Errorf("unknown credential backend %q (use \"file\" or \"keyring\")", backend)
	}
}
