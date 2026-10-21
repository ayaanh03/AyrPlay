//go:build !windows

package airplay

// windowsAudioLoopbackSource has no effect off Windows; StartAudioCapture falls
// through to PulseAudio/PipeWire source detection.
func windowsAudioLoopbackSource() ([]string, bool) {
	return nil, false
}
