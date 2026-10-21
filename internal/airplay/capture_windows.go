//go:build windows

package airplay

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// startPreparedWindowsCapture mirrors startPreparedTestCapture but sources frames
// from the Windows desktop via DXGI (d3d11screencapturesrc). It reuses the shared
// scaling/encoding/Annex-B path in buildGstVideoPipeline, so only the source and
// the GPU-side pre-convert stages differ: the framerate cap must sit on the
// D3D11Memory caps ahead of d3d11download, which hands system-memory frames to the
// shared videoconvert.
func startPreparedWindowsCapture(ctx context.Context, cfg CaptureConfig, encoder encoderResult, timestampedOutput bool) (*ScreenCapture, error) {
	captureCtx, cancel := context.WithCancel(ctx)

	fps := cfg.FPS
	if fps <= 0 {
		fps = 30
	}

	source := gstStage{
		"d3d11screencapturesrc",
		fmt.Sprintf("monitor-index=%d", cfg.MonitorIndex),
		fmt.Sprintf("show-cursor=%t", cfg.ShowCursor),
	}
	beforeConvert := []gstStage{
		{fmt.Sprintf("video/x-raw(memory:D3D11Memory),framerate=%d/1", fps)},
		{"d3d11download"},
		lowLatencyVideoQueueStage(),
	}
	gstArgs := buildGstVideoPipeline(source, beforeConvert, nil, encoder, cfg.MaxWidth, cfg.MaxHeight, timestampedOutput)

	dbg("[CAPTURE] launching gst-launch-1.0 (windows d3d11) %s", strings.Join(gstArgs, " "))
	cmd := exec.CommandContext(captureCtx, "gst-launch-1.0", gstArgs...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("gst stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("gst stderr pipe: %w", err)
	}

	waitResult, err := startGStreamerCommand(cmd)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("start gst-launch-1.0: %w", err)
	}

	go logStderr("GST", stderr)

	capture := &ScreenCapture{
		cmd:    cmd,
		stdout: stdout,
		cancel: cancel,
		waitCh: make(chan struct{}),
	}
	if timestampedOutput {
		capture.frames = newRTPVideoAccessUnitReader(stdout, encoder.codec)
	}
	go func() {
		capture.waitErr = <-waitResult
		close(capture.waitCh)
	}()

	return capture, nil
}
