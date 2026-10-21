//go:build windows

package airplay

// windowsAudioLoopbackSource returns the GStreamer source stage that captures the
// Windows system audio mix via WASAPI loopback. The shared audio tail resamples
// it to the AirPlay 44.1 kHz / S16 format before the built-in ALAC encoder.
func windowsAudioLoopbackSource() ([]string, bool) {
	// low-latency is intentionally off: WASAPI's tiny low-latency buffers jitter
	// under Windows scheduling. Default shared-mode buffering is much smoother,
	// and the AirPlay timing policy (plus -target-latency-ms) absorbs the few ms
	// of extra latency.
	return []string{"wasapi2src", "loopback=true"}, true
}
