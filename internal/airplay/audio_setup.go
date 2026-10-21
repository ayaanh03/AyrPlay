package airplay

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"net"
	"time"
)

const (
	// A realtime audio-only session advertises a wide accepted playout-latency
	// range (0.25s..2s at 44.1kHz), matching owntone's standard audio buffer.
	// The operating point inside that range is driven by the sync packets; the
	// TimeAnnounce lead actually handed to the RTP stream stays the connection
	// profile's audio latency, which always falls inside this window.
	audioRealtimeLatencyMin = 11025
	audioRealtimeLatencyMax = 88200
)

// setupAudioSession negotiates a video-free audio session with a HomePod or any
// other AirPlay 2 speaker. It is the sibling of setupMirrorSession: it reuses
// pairing/PTP/ChaCha/RTP machinery verbatim and omits every video concern (no
// type:110 stream, no TCP data connection, no video cipher, no data heartbeat).
func (c *AirPlayClient) setupAudioSession(ctx context.Context, cfg StreamConfig) (*MirrorSession, error) {
	sessionUUID := generateUUID()
	clientDeviceID := uuidToMAC(c.sessionID)
	senderName := pairingClientName()

	// Audio speakers advertise their formats in audioStream/bufferStream, not
	// screenStream, so negotiate with the audio-only session kind.
	policy, err := compatibilityForSession(c.info, c.encrypted, true, sessionAudioOnly)
	if err != nil {
		return nil, fmt.Errorf("negotiate realtime audio: %w", err)
	}
	sourceVersion := policy.sourceVersion()
	timingProtocol := policy.timing
	var clock *mediaClock
	if timingProtocol == timingProtocolPTP {
		clock = &mediaClock{}
	}

	setupRequest := mirrorSetupRequest{
		deviceID:       clientDeviceID,
		sessionUUID:    sessionUUID,
		sourceVersion:  sourceVersion,
		timingProtocol: timingProtocol,
		name:           senderName,
		kind:           sessionAudioOnly,
	}
	if timingProtocol == timingProtocolPTP {
		localAddress, err := localIPForConnection(c.conn)
		if err != nil {
			return nil, fmt.Errorf("determine PTP peer address: %w", err)
		}
		setupRequest.timingPeerID = generateUUID()
		setupRequest.timingPeerAddress = localAddress
		dbg("[PTP] local timing peer: id=%s address=%s", setupRequest.timingPeerID, localAddress)
	}
	dbg("[SETUP] audio-only timing protocol: %s (sourceVersion=%s)", timingProtocol, sourceVersion)

	// Allocate the consecutive timing/control/data UDP triple exactly as the
	// mirror path does. Only the control port is advertised; the receiver accepts
	// audio RTP from the data socket's independently allocated source port.
	audioPorts, err := allocateConsecutiveUDPPortsInRange(3, cfg.PortMin, cfg.PortMax)
	if err != nil {
		return nil, fmt.Errorf("allocate audio ports: %w", err)
	}
	timingConn := audioPorts[0]
	audioCtrlConn := audioPorts[1]
	audioDataConn := audioPorts[2]

	sessionCtx, cancelSession := context.WithCancel(ctx)
	var receiverEventConn net.Conn
	setupSucceeded := false
	controlConnectionUsable := true
	acceptedSessionURI := ""
	defer func() {
		if setupSucceeded {
			return
		}
		if !controlConnectionUsable {
			c.closeUnsafeMirrorControlConnection()
		} else if acceptedSessionURI != "" {
			if err := c.teardownFailedMirrorSetup(acceptedSessionURI, failedMirrorSetupTeardownTimeout); err != nil {
				dbg("[TEARDOWN] failed audio setup cleanup for %s: %v", acceptedSessionURI, err)
			} else {
				dbg("[TEARDOWN] released failed audio setup for %s", acceptedSessionURI)
			}
		}
		cancelSession()
		if receiverEventConn != nil {
			receiverEventConn.Close()
		}
		for _, conn := range audioPorts {
			conn.Close()
		}
	}()

	timingPort := timingConn.LocalAddr().(*net.UDPAddr).Port
	setupRequest.timingPort = timingPort
	dbg("[SETUP] consecutive UDP ports: base=%d ctrl=%d data=%d", timingPort, timingPort+1, timingPort+2)

	if timingProtocol == timingProtocolNTP {
		go ntpTimingResponder(sessionCtx, timingConn)
	}

	// The audio latency target follows the same connection profile as the mirror
	// path; only the descriptor's advertised latency window is widened below.
	latencyHint, _ := connectionLatencyHintForConnection(c.conn)
	latencies := screenLatenciesForHint(latencyHint)
	audioLatencySamples := samplesFor44k1(latencies.audio)

	audioStreamConnectionID := int64(time.Now().UnixNano() & 0x7FFFFFFFFFFFFFFF)
	selectedAudioCodec := policy.audioCodec
	audioURI := fmt.Sprintf("rtsp://%s:%d/%d", c.host, c.port, audioStreamConnectionID)
	audioMode := policy.audioSecurity
	var audioKey, audioIV, audioChaChaKey []byte
	if audioMode == audioSecurityChaCha {
		audioChaChaKey, err = generateAudioChaChaKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generate ChaCha20-Poly1305 audio key: %w", err)
		}
		dbg("[SETUP] audio encryption: ChaCha20-Poly1305 direct stream key (streamConnectionID=%d, shk=%d bytes)", audioStreamConnectionID, len(audioChaChaKey))
	} else if c.fpKey != nil && c.fpIV != nil {
		audioKey = c.fpKey
		audioIV = c.fpIV
		dbg("[SETUP] audio encryption: AES-128-CBC (fpKey/hashed %d bytes)", len(audioKey))
	} else {
		dbg("[SETUP] audio encryption: disabled (no FairPlay key available)")
	}
	audioControlLPort := audioCtrlConn.LocalAddr().(*net.UDPAddr).Port

	var receiverEventPort int
	observeEventPort := func(response map[string]interface{}) {
		if port := plistInt(response["eventPort"]); port > 0 {
			receiverEventPort = port
		}
	}
	connectEvent := func() error {
		if receiverEventConn != nil || receiverEventPort == 0 {
			return nil
		}
		conn, err := c.connectEventChannel(sessionCtx, receiverEventPort, clock)
		if err != nil {
			return fmt.Errorf("connect receiver event channel: %w", err)
		}
		receiverEventConn = conn
		return nil
	}
	configurePTPClock := func(response map[string]interface{}, headers map[string]string, receivedAt time.Time) error {
		if timingProtocol != timingProtocolPTP {
			return nil
		}
		if err := clock.configureFromSetup(response, headers, receivedAt); err != nil {
			if policy.permitsLocalPTPClock() {
				if fallbackErr := clock.configureFromLocalClock(); fallbackErr == nil {
					dbg("[PTP] %v; using local boot time on receiver timeline 0x%016x", err, clock.identity())
					return nil
				}
			}
			return fmt.Errorf("configure PTP media clock: %w", err)
		}
		return nil
	}

	firstSetup := true
	sendSetup := func(uri, phase string, request map[string]interface{}) (map[string]interface{}, map[string]string, time.Time, error) {
		perform := func() (map[string]interface{}, map[string]string, time.Time, error) {
			response, headers, receivedAt, accepted, connectionUsable, requestErr := c.requestSetup(uri, phase, request)
			if !connectionUsable {
				controlConnectionUsable = false
			}
			if accepted && acceptedSessionURI == "" {
				acceptedSessionURI = uri
			}
			return response, headers, receivedAt, requestErr
		}
		if !firstSetup {
			return perform()
		}
		firstSetup = false
		setupHintDone := make(chan struct{})
		go func() {
			select {
			case <-setupHintDone:
			case <-time.After(3 * time.Second):
				log.Printf("warning: receiver at %s has not responded to audio SETUP after 3s.", c.host)
				if timingProtocol == timingProtocolPTP {
					log.Printf("  HomePods require PTP on UDP 319/320; a host firewall blocking those ports breaks timing.")
				}
			}
		}()
		defer close(setupHintDone)
		return perform()
	}
	setupRTSPRequest := func(method, uri, contentType string, body []byte, headers map[string]string) ([]byte, map[string]string, error) {
		responseBody, responseHeaders, requestErr := c.rtspRequest(method, uri, contentType, body, headers)
		if hasIncompleteResponse(requestErr) {
			controlConnectionUsable = false
		}
		return responseBody, responseHeaders, requestErr
	}

	// Phase 1 — control SETUP creates the media session and returns the event
	// port. No display refresh and no video preparation: this is audio only.
	dbg("[SETUP] phase 1 (control): preparing audio session")
	controlPlist := setupRequest.controlPlist()
	if policy.fairPlayOnSessionRoot() {
		addFairPlayRootFields(controlPlist, c.FpEkey, c.fpIV, true)
	}
	controlResp, controlHeaders, controlReceivedAt, err := sendSetup(audioURI, "control", controlPlist)
	if err != nil {
		return nil, err
	}
	skipRecord, _ := controlResp["skipRecord"].(bool)
	if err := configurePTPClock(controlResp, controlHeaders, controlReceivedAt); err != nil {
		return nil, err
	}
	observeEventPort(controlResp)
	if err := connectEvent(); err != nil {
		return nil, err
	}

	// Phase 2 — the single realtime audio stream. Canonical owntone shape: flat
	// controlPort + shk layout, isMedia:true, no usingScreen, widened latency
	// window, supportsDynamicStreamID:false.
	audioCT, audioSPF, audioFmt, _, _, _ := selectedAudioCodec.Info()
	audioStreamDesc := map[string]interface{}{
		"type":                    int64(96),
		"streamConnectionID":      audioStreamConnectionID,
		"ct":                      audioCT,
		"spf":                     audioSPF,
		"sr":                      int64(44100),
		"audioFormat":             audioFmt,
		"audioMode":               "default",
		"isMedia":                 true,
		"latencyMin":              int64(audioRealtimeLatencyMin),
		"latencyMax":              int64(audioRealtimeLatencyMax),
		"supportsDynamicStreamID": false,
	}
	if useAudioRedundancy(selectedAudioCodec) {
		audioStreamDesc["redundantAudio"] = int64(audioRedundancyCount)
	}
	// Flat controlPort + shk layout (owntone-canonical for audio-only). This sets
	// controlPort and, when encrypted, the 32-byte shk; it never adds usingScreen.
	addScreenAudioStreamFields(audioStreamDesc, audioChaChaKey, audioControlLPort, audioLayoutControlPort)

	audioSetupPlist := streamOnlyPlist(audioStreamDesc)
	debugDumpPlist("audio-only setup plist", audioSetupPlist)
	debugDumpPlist("audio-only stream descriptor", audioStreamDesc)

	dbg("[SETUP] phase 2 (audio): ct=%d spf=%d audioFormat=0x%x controlPort=%d", audioCT, audioSPF, audioFmt, audioControlLPort)
	audioResp, audioRespHeaders, audioRespReceivedAt, err := sendSetup(audioURI, "audio stream", audioSetupPlist)
	if err != nil {
		return nil, err
	}
	if err := configurePTPClock(audioResp, audioRespHeaders, audioRespReceivedAt); err != nil {
		return nil, err
	}
	observeEventPort(audioResp)
	if err := connectEvent(); err != nil {
		return nil, err
	}

	audioDataPort := 0
	audioControlPort := 0
	if streams, ok := audioResp["streams"].([]interface{}); ok {
		for _, s := range streams {
			stream, ok := s.(map[string]interface{})
			if !ok {
				continue
			}
			if plistInt(stream["type"]) == 96 {
				audioDataPort, audioControlPort = plistStreamPorts(stream)
				dbg("[SETUP] audio stream: dataPort=%d controlPort=%d", audioDataPort, audioControlPort)
			}
		}
	}
	if audioDataPort == 0 {
		return nil, fmt.Errorf("no audio data port in SETUP response")
	}

	// The sender activates the session after all transport streams are set up. A
	// receiver may waive the final RECORD with skipRecord.
	if skipRecord {
		dbg("[SETUP] receiver returned skipRecord=true; session was started by SETUP")
	} else {
		recordHeaders := map[string]string{
			"Session":  sessionUUID,
			"Range":    "npt=0-",
			"RTP-Info": "seq=0;rtptime=0",
		}
		if _, _, err := setupRTSPRequest("RECORD", audioURI, "", nil, recordHeaders); err != nil {
			return nil, fmt.Errorf("RECORD: %w", err)
		}
	}

	// An audio-only session always carries audio, so it sets the receiver's
	// volume (0 dB = full scale). Sent twice, as real senders do.
	volumeBody := audioVolumeBody(false)
	if _, _, err := setupRTSPRequest("SET_PARAMETER", audioURI, "text/parameters", volumeBody, nil); err != nil {
		if !controlConnectionUsable {
			return nil, fmt.Errorf("SET_PARAMETER volume: %w", err)
		}
		dbg("[SETUP] SET_PARAMETER volume failed (non-fatal): %v", err)
	}
	if _, _, err := setupRTSPRequest("SET_PARAMETER", audioURI, "text/parameters", volumeBody, nil); err != nil && !controlConnectionUsable {
		return nil, fmt.Errorf("repeat SET_PARAMETER volume: %w", err)
	}

	if timingProtocol == timingProtocolPTP {
		// PTP uses the receiver's fixed 319/320 ports. The timing socket was only
		// reserved while allocating the consecutive control/data ports.
		_ = timingConn.Close()
		timingConn = nil
	}

	// An audio-only session has no video frame to wait for, so pre-close
	// firstFrameSent. StreamAudio also gates its prewarm on audioOnly.
	firstFrameSent := make(chan struct{})
	close(firstFrameSent)

	session := &MirrorSession{
		client:         c,
		eventConn:      receiverEventConn,
		cancel:         cancelSession,
		firstFrameSent: firstFrameSent,
		streamDone:     make(chan struct{}),
		sessionURI:     audioURI,
		timingConn:     timingConn,
		timingProtocol: timingProtocol,
		mediaClock:     clock,
		audioOnly:      true,
	}

	audioCTByte := byte(selectedAudioCodec)
	as, err := session.setupAudioStream(audioDataPort, audioControlPort, audioKey, audioIV, audioChaChaKey, audioMode, audioCTByte, audioLatencySamples, policy.audioRFC2198, audioCtrlConn, audioDataConn)
	if err != nil {
		return nil, fmt.Errorf("audio stream setup: %w", err)
	}
	session.audioStream = as
	dbg("[SETUP] audio-only stream ready")

	// Keepalive only: /feedback (also re-anchors the PTP clock). No data-channel
	// heartbeat and no video data connection to monitor.
	session.startWorker(func() { session.feedbackLoop(sessionCtx) })

	setupSucceeded = true
	return session, nil
}
