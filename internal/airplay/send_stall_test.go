package airplay

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

// Tests for congestion handling on the video data channel: a write that times
// out before any byte leaves is recoverable, and HEVC resyncs at the next
// keyframe instead of ending the session.

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// stallingConn is a net.Conn whose writes are recorded until stall reports
// that the next write should time out with nothing written.
type stallingConn struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	writes  int
	stallOn func(write int, p []byte) bool
}

func (c *stallingConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes++
	if c.stallOn != nil && c.stallOn(c.writes, p) {
		return 0, timeoutError{}
	}
	return c.buf.Write(p)
}

func (c *stallingConn) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.buf.Bytes()...)
}

func (c *stallingConn) Read([]byte) (int, error)         { return 0, net.ErrClosed }
func (c *stallingConn) Close() error                     { return nil }
func (c *stallingConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *stallingConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *stallingConn) SetDeadline(time.Time) error      { return nil }
func (c *stallingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *stallingConn) SetWriteDeadline(time.Time) error { return nil }

func newChaChaSendSession(t *testing.T, conn net.Conn) *MirrorSession {
	t.Helper()
	aead, err := chacha20poly1305.New(bytes.Repeat([]byte{0x11}, chacha20poly1305.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return &MirrorSession{dataConn: conn, chachaCipher: aead}
}

func TestSendFrameStallBeforeAnyByteIsRecoverable(t *testing.T) {
	conn := &stallingConn{stallOn: func(int, []byte) bool { return true }}
	session := newChaChaSendSession(t, conn)
	session.frameSeq, session.chachaNonce = 7, 7

	err := session.sendFrame([]byte{0, 0, 0, 1, 0x26}, true, 1, 0)
	if !errors.Is(err, errVideoSendStalled) {
		t.Fatalf("sendFrame = %v, want errVideoSendStalled", err)
	}
	if session.frameSeq != 7 || session.chachaNonce != 7 {
		t.Fatalf("after a stall frameSeq=%d nonce=%d, want both rolled back to 7", session.frameSeq, session.chachaNonce)
	}
	if len(conn.bytes()) != 0 {
		t.Fatal("a stalled frame must not put bytes on the wire")
	}

	// The next frame reuses the rolled-back nonce, so the receiver's counter
	// stays in lockstep.
	conn.stallOn = nil
	if err := session.sendFrame([]byte{0, 0, 0, 1, 0x26}, true, 1, 0); err != nil {
		t.Fatalf("sendFrame after stall: %v", err)
	}
	if session.frameSeq != 8 || session.chachaNonce != 8 {
		t.Fatalf("after a sent frame frameSeq=%d nonce=%d, want 8", session.frameSeq, session.chachaNonce)
	}
}

func TestSendFramePartialWriteIsFatal(t *testing.T) {
	// The header goes out, then the payload write times out: the frame boundary
	// on the wire is broken, so this must not be reported as a recoverable stall.
	conn := &stallingConn{stallOn: func(write int, _ []byte) bool { return write == 2 }}
	session := newChaChaSendSession(t, conn)

	err := session.sendFrame([]byte{0, 0, 0, 1, 0x26}, true, 1, 0)
	if err == nil || errors.Is(err, errVideoSendStalled) {
		t.Fatalf("sendFrame = %v, want a fatal (non-stall) error", err)
	}
}

func TestSendFrameNonTimeoutErrorIsFatal(t *testing.T) {
	session := &MirrorSession{dataConn: &errorConn{err: net.ErrClosed}}
	if err := session.sendFrame([]byte{1}, false, 1, 0); err == nil || errors.Is(err, errVideoSendStalled) {
		t.Fatalf("sendFrame = %v, want the write error", err)
	}
}

type errorConn struct {
	stallingConn
	err error
}

func (c *errorConn) Write([]byte) (int, error) { return 0, c.err }

func TestVideoSendBufferKB(t *testing.T) {
	if got := videoSendBufferKB(false); got != 512 {
		t.Fatalf("automatic latency send buffer = %dK, want 512K (several 4K keyframes)", got)
	}
	if got := videoSendBufferKB(true); got != 8*1024 {
		t.Fatalf("explicit playout buffer send buffer = %dK, want 8M", got)
	}
}

// testHEVCSPS is a 1280x720 Main-profile HEVC SPS.
var testHEVCSPS, _ = hex.DecodeString("42010101600000030090000003000003005da00280802d165959a4932bc05a70800001f480003a9804")

func TestHEVCStreamResyncsAtNextKeyframeAfterStall(t *testing.T) {
	vps := []byte{0x40, 0x01, 0x0c, 0x01}
	pps := []byte{0x44, 0x01, 0xc1, 0x72}
	idr := func(b byte) []byte { return []byte{0x26, 0x01, 0xaf, b} }   // IDR_W_RADL (19)
	trail := func(b byte) []byte { return []byte{0x02, 0x01, 0xd0, b} } // TRAIL_R (1)

	now := time.Now()
	pts := func(i int) time.Time { return now.Add(time.Duration(i) * 33 * time.Millisecond) }
	frames := []VideoAccessUnit{
		{AnnexB: joinAnnexBNALs(vps, testHEVCSPS, pps, idr(1)), PTS: pts(0)},
		{AnnexB: joinAnnexBNALs(trail(2)), PTS: pts(1)}, // stalls
		{AnnexB: joinAnnexBNALs(trail(3)), PTS: pts(2)}, // references the dropped frame: skipped
		{AnnexB: joinAnnexBNALs(idr(4)), PTS: pts(3)},   // resync point
		{AnnexB: joinAnnexBNALs(trail(5)), PTS: pts(4)},
	}
	capture := &ScreenCapture{frames: &sliceVideoAccessUnitReader{frames: frames}, waitCh: make(chan struct{})}

	videoHeaders := 0
	conn := &stallingConn{stallOn: func(_ int, p []byte) bool {
		if len(p) != 128 || p[4] != 0x00 {
			return false // codec packets and payloads
		}
		videoHeaders++
		return videoHeaders == 2 // the first non-key frame
	}}
	session := &MirrorSession{
		dataConn:       conn,
		videoCodec:     VideoCodecHEVC,
		firstFrameSent: make(chan struct{}),
		frameClockNow:  func() time.Time { return now },
		mediaClock: &mediaClock{
			anchorLocal:     now.Add(-time.Second),
			anchorTimestamp: compactTimestamp(10 * time.Second),
			timelineID:      1,
		},
	}
	if err := session.StreamFrames(context.Background(), capture, 0); err == nil {
		t.Fatal("StreamFrames returned nil at end of capture, want the EOF error")
	}

	// Parse what reached the wire: one codec packet then the video frames.
	wire := conn.bytes()
	var keyFlags []byte
	var lastBytes []byte
	codecPackets := 0
	for len(wire) >= 128 {
		size := int(binary.LittleEndian.Uint32(wire[:4]))
		kind, flag := wire[4], wire[5]
		payload := wire[128 : 128+size]
		wire = wire[128+size:]
		if kind == 1 {
			codecPackets++
			continue
		}
		keyFlags = append(keyFlags, flag)
		lastBytes = append(lastBytes, payload[len(payload)-1])
	}
	if codecPackets != 1 {
		t.Fatalf("codec packets = %d, want 1", codecPackets)
	}
	if want := []byte{0x10, 0x10, 0x00}; !bytes.Equal(keyFlags, want) {
		t.Fatalf("video frame key flags = %x, want %x (IDR, IDR after resync, trailing frame)", keyFlags, want)
	}
	if want := []byte{1, 4, 5}; !bytes.Equal(lastBytes, want) {
		t.Fatalf("frames on the wire = %v, want %v (frames 2 and 3 dropped)", lastBytes, want)
	}
}

func TestWindowsCapturePipeline(t *testing.T) {
	encoder := encoderResult{parts: gstStage{"nvh264enc"}, rawFormat: "NV12"}
	pipeline := windowsCapturePipeline(CaptureConfig{FPS: 60, MonitorIndex: 2, ShowCursor: false, MaxWidth: 1920, MaxHeight: 1080}, encoder, true)
	got := strings.Join(pipeline, " ")
	for _, want := range []string{
		"--quiet d3d11screencapturesrc monitor-index=2 show-cursor=false ! video/x-raw(memory:D3D11Memory),framerate=60/1 ! d3d11download ! queue",
		"videoconvert ! video/x-raw,format=NV12",
		"video/x-raw,width=1920,height=1080",
		"nvh264enc ! h264parse",
		"rtph264pay",
		"fdsink fd=1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("pipeline %q does not contain %q", got, want)
		}
	}

	defaults := strings.Join(windowsCapturePipeline(CaptureConfig{ShowCursor: true}, encoder, false), " ")
	for _, want := range []string{"monitor-index=0 show-cursor=true", "framerate=30/1"} {
		if !strings.Contains(defaults, want) {
			t.Fatalf("default pipeline %q does not contain %q", defaults, want)
		}
	}
}
