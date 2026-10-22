package airplay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Tests for the audio-only (HomePod / AirPlay 2 speaker) sender path.

func TestSetupAudioEndToEndWithHomePodReceiver(t *testing.T) {
	server, client, ctx := newReceiverServerTestPair(t, ReceiverConfig{Profile: ReceiverProfileHomePod, Auth: ReceiverAuthNone})
	if !IsAudioOnlyModel(client.info.Model) {
		t.Fatalf("HomePod fixture model %q is not routed to audio-only", client.info.Model)
	}
	if err := client.Pair(ctx, ""); err != nil {
		t.Fatalf("transient pair: %v", err)
	}
	if !client.encrypted {
		t.Fatal("HomePod control channel is not encrypted after transient pairing")
	}
	if client.info.SupportsFairPlaySAP() {
		if err := client.FairPlaySetup(ctx); err != nil {
			t.Fatalf("FairPlay setup: %v", err)
		}
	}

	session, err := client.SetupAudio(ctx, StreamConfig{})
	if err != nil {
		t.Fatalf("SetupAudio: %v", err)
	}
	if !session.HasAudio() || session.AudioStream() == nil {
		t.Fatal("audio-only session has no audio stream")
	}
	if session.AudioCodec() != AudioCodecALAC {
		t.Fatalf("audio codec = %v, want ALAC from the receiver's realtime audio mask", session.AudioCodec())
	}
	select {
	case <-session.firstFrameSent:
	default:
		t.Fatal("audio-only session must not wait for a video frame")
	}

	// Stream ~100 ms of PCM through the real ALAC/ChaCha/RTP path.
	_, spf, _, _, _, _ := AudioCodecALAC.Info()
	pcm := make([]byte, 12*int(spf)*audioBytesPerSampleFrame)
	for i := range pcm {
		pcm[i] = byte(i * 7)
	}
	capture := &AudioCapture{pcmPipe: io.NopCloser(bytes.NewReader(pcm)), waitCh: make(chan struct{}), codec: AudioCodecALAC}
	if err := session.StreamAudio(ctx, capture, session.AudioStream()); !errors.Is(err, io.EOF) {
		t.Fatalf("StreamAudio = %v, want EOF after the PCM ran out", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for server.Stats().AudioPackets == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := session.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("close audio session: %v", err)
	}

	stats := server.Stats()
	if stats.AudioPackets == 0 {
		t.Fatalf("receiver got no audio packets: %+v", stats)
	}
	if stats.SetupRequests != 2 || stats.RecordRequests != 1 || stats.TeardownRequests != 1 {
		t.Fatalf("control stats = %+v, want control+audio SETUP, RECORD, TEARDOWN", stats)
	}
	if stats.VideoConnections != 0 || stats.VideoPackets != 0 {
		t.Fatalf("audio-only session opened video: %+v", stats)
	}
	if stats.ParameterRequests != 2 {
		t.Fatalf("SET_PARAMETER requests = %d, want the volume sent twice", stats.ParameterRequests)
	}
}

func TestHomePodReceiverRejectsScreenMirroring(t *testing.T) {
	_, client, ctx := newReceiverServerTestPair(t, ReceiverConfig{Profile: ReceiverProfileHomePod, Auth: ReceiverAuthNone})
	if err := client.Pair(ctx, ""); err != nil {
		t.Fatalf("pair: %v", err)
	}
	if err := client.FairPlaySetup(ctx); err != nil {
		t.Fatalf("FairPlay setup: %v", err)
	}
	if session, err := client.SetupMirror(ctx, StreamConfig{NoAudio: true}); err == nil {
		session.Close()
		t.Fatal("HomePod fixture accepted a screen-mirroring session")
	}
}

func TestAudioOnlySessionPlistOmitsScreenMirroringFlag(t *testing.T) {
	screen := mirrorSetupRequest{timingProtocol: timingProtocolPTP}.sessionPlist()
	if screen["isScreenMirroringSession"] != true {
		t.Fatalf("screen session plist = %v, want isScreenMirroringSession=true", screen)
	}
	audio := mirrorSetupRequest{timingProtocol: timingProtocolPTP, kind: sessionAudioOnly}.sessionPlist()
	if _, ok := audio["isScreenMirroringSession"]; ok {
		t.Fatalf("audio-only session plist advertises isScreenMirroringSession: %v", audio)
	}
	if audio["timingProtocol"] != timingProtocolPTP {
		t.Fatalf("audio-only session plist lost its timing protocol: %v", audio)
	}
}

func TestRealtimeAudioCodecReadsAudioMasks(t *testing.T) {
	withFormats := func(f StreamFormats) *ReceiverInfo {
		return &ReceiverInfo{SupportedFormats: f, hasSupportedFormats: true}
	}
	for _, test := range []struct {
		name       string
		info       *ReceiverInfo
		allowELD   bool
		want       AudioCodec
		wantErr    error
		wantErrSub string
	}{
		{name: "no info assumes ALAC", want: AudioCodecALAC},
		{name: "empty masks assume ALAC", info: withFormats(StreamFormats{}), want: AudioCodecALAC},
		{name: "audioStream ALAC", info: withFormats(StreamFormats{AudioStream: FormatMask(screenAudioFormatALAC)}), want: AudioCodecALAC},
		{name: "bufferStream ALAC", info: withFormats(StreamFormats{BufferStream: FormatMask(screenAudioFormatALAC)}), want: AudioCodecALAC},
		{name: "screen mask is ignored", info: withFormats(StreamFormats{ScreenStream: FormatMask(screenAudioFormatAACELD44100Stereo), AudioStream: FormatMask(screenAudioFormatALAC)}), allowELD: true, want: AudioCodecALAC},
		{name: "AAC-ELD preferred when encodable", info: withFormats(StreamFormats{AudioStream: FormatMask(screenAudioFormatALAC | screenAudioFormatAACELD44100Stereo)}), allowELD: true, want: AudioCodecAACELD},
		{name: "ALAC when AAC-ELD encoder missing", info: withFormats(StreamFormats{AudioStream: FormatMask(screenAudioFormatALAC | screenAudioFormatAACELD44100Stereo)}), want: AudioCodecALAC},
		{name: "AAC-ELD only without encoder", info: withFormats(StreamFormats{AudioStream: FormatMask(screenAudioFormatAACELD44100Stereo)}), wantErr: ErrAACELDUnavailable},
		{name: "unsupported mask", info: withFormats(StreamFormats{AudioStream: FormatMask(screenAudioFormatPCM44100Stereo)}), wantErrSub: "unsupported realtime audio mask"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := realtimeAudioCodec(test.info, test.allowELD)
			switch {
			case test.wantErr != nil:
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("realtimeAudioCodec error = %v, want %v", err, test.wantErr)
				}
			case test.wantErrSub != "":
				if err == nil || !strings.Contains(err.Error(), test.wantErrSub) {
					t.Fatalf("realtimeAudioCodec error = %v, want substring %q", err, test.wantErrSub)
				}
			case err != nil:
				t.Fatalf("realtimeAudioCodec: %v", err)
			case got != test.want:
				t.Fatalf("realtimeAudioCodec = %v, want %v", got, test.want)
			}
		})
	}
}

func TestCompatibilityForSessionSelectsAudioByKind(t *testing.T) {
	// The screen and realtime masks disagree: the screen path offers only
	// AAC-ELD while the speaker path offers ALAC. Each session kind must read
	// its own mask.
	info := &ReceiverInfo{
		SupportedFormats: StreamFormats{
			ScreenStream: FormatMask(screenAudioFormatAACELD44100Stereo),
			AudioStream:  FormatMask(screenAudioFormatALAC),
		},
		hasSupportedFormats: true,
	}
	policy, err := compatibilityForSession(info, true, true, sessionAudioOnly)
	if err != nil {
		t.Fatalf("audio-only compatibility: %v", err)
	}
	if policy.audioCodec != AudioCodecALAC {
		t.Fatalf("audio-only codec = %v, want ALAC from audioStream", policy.audioCodec)
	}
	if screen, err := compatibilityForSession(info, true, true, sessionScreenMirror); err == nil && screen.audioCodec == AudioCodecALAC {
		t.Fatal("screen compatibility read the realtime audioStream mask instead of screenStream")
	}
}

func TestIsAudioOnlyModel(t *testing.T) {
	for model, want := range map[string]bool{
		"AudioAccessory1,1": true, // HomePod
		"AudioAccessory5,1": true, // HomePod mini
		"AudioAccessory6,1": true, // HomePod (2nd gen)
		"AppleTV14,1":       false,
		"AppleTV3,2":        false,
		"":                  false,
		"audioaccessory5,1": false, // model identifiers are case-sensitive
	} {
		if got := IsAudioOnlyModel(model); got != want {
			t.Errorf("IsAudioOnlyModel(%q) = %t, want %t", model, got, want)
		}
	}
}

func TestStreamAudioAudioOnlySkipsVideoPrewarm(t *testing.T) {
	// A mirror session holds audio until the first video frame; an audio-only
	// session has none, so StreamAudio must start even though firstFrameSent is
	// still open.
	ctrlConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctrlConn.Close() })
	ctrlPeer, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctrlPeer.Close() })
	dataConn := &recordingPacketConn{}
	stream := &AudioStream{
		conn: dataConn, ctrlConn: ctrlConn, remoteAddr: &netUDPAddrForAudioTest,
		ctrlAddr:       ctrlPeer.LocalAddr().(*net.UDPAddr),
		ct:             byte(AudioCodecALAC),
		latencySamples: targetLatencySamples44k1(),
	}
	_, spf, _, _, _, _ := AudioCodecALAC.Info()
	pcm := make([]byte, 4*int(spf)*audioBytesPerSampleFrame)
	capture := &AudioCapture{pcmPipe: io.NopCloser(bytes.NewReader(pcm)), waitCh: make(chan struct{}), codec: AudioCodecALAC}
	session := &MirrorSession{firstFrameSent: make(chan struct{}), timingProtocol: timingProtocolNTP, audioOnly: true}

	done := make(chan error, 1)
	go func() { done <- session.StreamAudio(context.Background(), capture, stream) }()
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("StreamAudio = %v, want EOF", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("audio-only StreamAudio blocked waiting for a video frame")
	}
	if len(dataConn.packets) == 0 {
		t.Fatal("audio-only StreamAudio sent no packets")
	}
}

func TestWindowsAudioLoopbackSource(t *testing.T) {
	args, ok := windowsAudioLoopbackSource()
	if runtime.GOOS != "windows" {
		if ok || args != nil {
			t.Fatalf("windowsAudioLoopbackSource() = %v, %t off Windows, want nil, false", args, ok)
		}
		return
	}
	if !ok || strings.Join(args, " ") != "wasapi2src loopback=true" {
		t.Fatalf("windowsAudioLoopbackSource() = %v, %t, want wasapi2src loopback=true", args, ok)
	}
}

func TestHomePodReceiverValidatesRealtimeAudioDescriptor(t *testing.T) {
	profile, err := receiverProfile(ReceiverProfileHomePod)
	if err != nil {
		t.Fatal(err)
	}
	if !profile.audioOnly || profile.displayWidth != 0 {
		t.Fatalf("HomePod profile = %+v, want an audio-only receiver without a display", profile)
	}
	ct, spf, format, _, _, _ := AudioCodecALAC.Info()
	valid := func() map[string]any {
		return map[string]any{
			"type": int64(96), "ct": int64(ct), "spf": int64(spf), "audioFormat": int64(format),
			"controlPort": int64(6001), "isMedia": true, "latencyMin": int64(11025), "latencyMax": int64(88200),
			"shk": make([]byte, 32),
		}
	}
	if err := validateRealtimeAudioDescriptor(profile, valid(), true); err != nil {
		t.Fatalf("canonical descriptor rejected: %v", err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"streamConnections layout": func(d map[string]any) { d["streamConnections"] = map[string]any{} },
		"missing controlPort":      func(d map[string]any) { delete(d, "controlPort") },
		"usingScreen":              func(d map[string]any) { d["usingScreen"] = true },
		"not media":                func(d map[string]any) { d["isMedia"] = false },
		"no latency window":        func(d map[string]any) { delete(d, "latencyMax") },
		"short key":                func(d map[string]any) { d["shk"] = make([]byte, 16) },
		"unadvertised codec":       func(d map[string]any) { d["audioFormat"] = int64(screenAudioFormatAACELD44100Stereo) },
	} {
		d := valid()
		mutate(d)
		if err := validateRealtimeAudioDescriptor(profile, d, true); err == nil {
			t.Errorf("%s: descriptor accepted, want rejection", name)
		}
	}
}
