package airplay

import "strings"

// Markers exchanged between the doubletake CLI and a front-end (the system tray)
// when the CLI runs in -machine-prompts mode. The CLI prints a credential-request
// marker on stdout at the exact moment pairing needs a value, then reads the
// value as a bare line from stdin — this lets a GUI drive PIN/password entry
// through a live pairing session (the Apple TV shows its PIN only after the
// sender starts the exchange, so the value cannot be supplied up front). The CLI
// also prints a status marker once pairing completes, so the front-end can tell
// a pairing failure apart from a later streaming error.
const (
	credentialRequestPrefix = "@@DOUBLETAKE_NEED_CREDENTIAL:"
	statusPrefix            = "@@DOUBLETAKE_STATUS:"
	markerSuffix            = "@@"
)

// CredentialRequestLine returns the marker the CLI prints to ask a front-end for
// a pairing credential. kind is "pin" or "password".
func CredentialRequestLine(kind string) string {
	return credentialRequestPrefix + kind + markerSuffix
}

// ParseCredentialRequest reports whether line is a credential-request marker and,
// if so, returns the requested kind ("pin" or "password").
func ParseCredentialRequest(line string) (kind string, ok bool) {
	return parseMarker(line, credentialRequestPrefix)
}

// StatusLine returns a status marker the CLI prints to report progress, e.g.
// StatusLine("paired") once pairing has succeeded.
func StatusLine(state string) string {
	return statusPrefix + state + markerSuffix
}

// ParseStatus reports whether line is a status marker and, if so, returns the
// state (e.g. "paired").
func ParseStatus(line string) (state string, ok bool) {
	return parseMarker(line, statusPrefix)
}

// parseMarker extracts the value between prefix and the common suffix. It
// tolerates surrounding whitespace so a line reader can pass raw lines through,
// and rejects an empty value.
func parseMarker(line, prefix string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, markerSuffix) {
		return "", false
	}
	val := line[len(prefix) : len(line)-len(markerSuffix)]
	if val == "" {
		return "", false
	}
	return val, true
}
