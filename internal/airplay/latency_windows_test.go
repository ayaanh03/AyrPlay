//go:build windows

package airplay

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestAdaptersContainWirelessName(t *testing.T) {
	name := func(s string) *uint16 {
		p, err := windows.UTF16PtrFromString(s)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	adapters := make([]windows.IpAdapterAddresses, 3)
	adapters[0] = windows.IpAdapterAddresses{IfType: windows.IF_TYPE_ETHERNET_CSMACD, FriendlyName: name("Ethernet"), Next: &adapters[1]}
	adapters[1] = windows.IpAdapterAddresses{IfType: windows.IF_TYPE_IEEE80211, FriendlyName: name("Wi-Fi"), Next: &adapters[2]}
	adapters[2] = windows.IpAdapterAddresses{IfType: windows.IF_TYPE_ETHERNET_CSMACD, FriendlyName: name("Wi-Fi 2 (USB dongle in Ethernet mode)")}
	buf := unsafe.Slice((*byte)(unsafe.Pointer(&adapters[0])), len(adapters)*int(unsafe.Sizeof(adapters[0])))

	for iface, want := range map[string]bool{
		"Wi-Fi":                                 true,
		"Ethernet":                              false,
		"Wi-Fi 2 (USB dongle in Ethernet mode)": false,
		"Missing adapter":                       false,
	} {
		if got := adaptersContainWirelessName(buf, iface); got != want {
			t.Errorf("adaptersContainWirelessName(%q) = %t, want %t", iface, got, want)
		}
	}
}

func TestOSInterfaceIsWirelessQueriesLiveAdapters(t *testing.T) {
	// The live adapter table varies by machine; only the always-false inputs
	// are deterministic, and the call must not fail or panic.
	if osInterfaceIsWireless("") {
		t.Fatal("empty interface name reported as wireless")
	}
	if osInterfaceIsWireless("AyrPlay test adapter that does not exist") {
		t.Fatal("missing adapter reported as wireless")
	}
}
