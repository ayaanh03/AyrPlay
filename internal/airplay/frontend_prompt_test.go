package airplay

import "testing"

func TestCredentialRequestRoundTrip(t *testing.T) {
	for _, kind := range []string{"pin", "password"} {
		line := CredentialRequestLine(kind)
		got, ok := ParseCredentialRequest(line)
		if !ok || got != kind {
			t.Fatalf("round trip %q: got (%q,%v), want (%q,true)", line, got, ok, kind)
		}
		// A line reader may hand us trailing CR/LF or spaces.
		if got2, ok2 := ParseCredentialRequest("  " + line + "\r\n"); !ok2 || got2 != kind {
			t.Fatalf("round trip with whitespace: got (%q,%v), want (%q,true)", got2, ok2, kind)
		}
	}

	for _, bad := range []string{
		"",
		"random log line",
		"connected to: Apple TV",
		"@@DOUBLETAKE_NEED_CREDENTIAL:@@", // empty kind
		"@@DOUBLETAKE_NEED_CREDENTIAL:pin", // missing suffix
	} {
		if kind, ok := ParseCredentialRequest(bad); ok {
			t.Fatalf("ParseCredentialRequest(%q) = (%q,true), want not ok", bad, kind)
		}
	}
}

func TestStatusRoundTrip(t *testing.T) {
	line := StatusLine("paired")
	if state, ok := ParseStatus(line); !ok || state != "paired" {
		t.Fatalf("ParseStatus(%q) = (%q,%v), want (\"paired\",true)", line, state, ok)
	}
	// A credential marker must not parse as a status marker, and vice versa.
	if _, ok := ParseStatus(CredentialRequestLine("pin")); ok {
		t.Fatalf("credential marker parsed as status")
	}
	if _, ok := ParseCredentialRequest(StatusLine("paired")); ok {
		t.Fatalf("status marker parsed as credential")
	}
}
