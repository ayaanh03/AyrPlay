//go:build !windows

package airplay

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNetworkInterfaceIsWirelessUsesSysfsOffWindows(t *testing.T) {
	classPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(classPath, "wlan0", "wireless"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(classPath, "eth0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !networkInterfaceIsWireless("wlan0", classPath) {
		t.Fatal("sysfs wireless marker not detected")
	}
	// Without a sysfs marker the OS fallback answers, which is "not wireless"
	// off Windows.
	if networkInterfaceIsWireless("eth0", classPath) || osInterfaceIsWireless("wlan0") {
		t.Fatal("non-Windows fallback reported a wireless interface")
	}
}
