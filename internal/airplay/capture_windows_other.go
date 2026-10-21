//go:build !windows

package airplay

import (
	"context"
	"fmt"
)

// startPreparedWindowsCapture is unavailable off Windows. The capture dispatch in
// capture.go references it on every platform, so this stub keeps non-Windows
// builds linking.
func startPreparedWindowsCapture(ctx context.Context, cfg CaptureConfig, encoder encoderResult, timestampedOutput bool) (*ScreenCapture, error) {
	return nil, fmt.Errorf("windows screen capture is not supported on this platform")
}
