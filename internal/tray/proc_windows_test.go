//go:build windows

package tray

import "testing"

func TestSingleInstanceMutex(t *testing.T) {
	release, ok := acquireSingleInstance()
	if !ok {
		t.Skip("an AyrPlay tray is already running in this session")
	}
	if _, again := acquireSingleInstance(); again {
		t.Fatal("a second tray acquired the single-instance mutex")
	}
	release()
	release2, ok := acquireSingleInstance()
	if !ok {
		t.Fatal("tray could not start after the first one exited")
	}
	release2()
}

func TestDisplayCountIsAtLeastOne(t *testing.T) {
	if n := displayCount(); n < 1 {
		t.Fatalf("displayCount() = %d, want >= 1", n)
	}
}
