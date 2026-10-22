package airplay

import "fmt"

// windowsCapturePipeline builds the gst-launch-1.0 arguments for Windows screen
// capture via DXGI (d3d11screencapturesrc). It reuses the shared scaling,
// encoding, and Annex-B path in buildGstVideoPipeline, so only the source and the
// GPU-side pre-convert stages differ: the framerate cap must sit on the
// D3D11Memory caps ahead of d3d11download, which hands system-memory frames to
// the shared videoconvert. It has no build tag so it can be tested anywhere.
func windowsCapturePipeline(cfg CaptureConfig, encoder encoderResult, timestampedOutput bool) []string {
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
	return buildGstVideoPipeline(source, beforeConvert, nil, encoder, cfg.MaxWidth, cfg.MaxHeight, timestampedOutput)
}
