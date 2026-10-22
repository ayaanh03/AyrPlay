//go:build windows

package airplay

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// startPreparedWindowsCapture mirrors startPreparedTestCapture but sources frames
// from the Windows desktop via DXGI (see windowsCapturePipeline).
func startPreparedWindowsCapture(ctx context.Context, cfg CaptureConfig, encoder encoderResult, timestampedOutput bool) (*ScreenCapture, error) {
	captureCtx, cancel := context.WithCancel(ctx)

	gstArgs := windowsCapturePipeline(cfg, encoder, timestampedOutput)

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
