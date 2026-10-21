//go:build windows

package airplay

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// osInterfaceIsWireless reports whether the Windows network interface whose
// friendly name is `name` (matching net.Interface.Name on Windows) is an
// IEEE 802.11 adapter. Windows has no Linux-style /sys/class/net markers, so
// without this the automatic latency policy would treat every Wi-Fi link as
// wired and under-buffer it. Querying the adapter table lets a Wi-Fi route use
// the roomier high-latency profile. Any failure degrades to "not wireless",
// which simply keeps the previous (wired) behaviour.
func osInterfaceIsWireless(name string) bool {
	if name == "" {
		return false
	}
	const flags = windows.GAA_FLAG_SKIP_ANYCAST |
		windows.GAA_FLAG_SKIP_MULTICAST |
		windows.GAA_FLAG_SKIP_DNS_SERVER

	// GetAdaptersAddresses fills a caller-provided buffer; start at the size
	// Microsoft recommends and grow if the adapter set does not fit.
	size := uint32(15 * 1024)
	for attempt := 0; attempt < 3; attempt++ {
		buf := make([]byte, size)
		err := windows.GetAdaptersAddresses(
			windows.AF_UNSPEC, flags, 0,
			(*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size,
		)
		switch err {
		case nil:
			return adaptersContainWirelessName(buf, name)
		case windows.ERROR_BUFFER_OVERFLOW:
			continue // `size` now holds the required length; retry larger
		default:
			return false
		}
	}
	return false
}

// adaptersContainWirelessName walks the adapter list materialised in buf and
// reports whether any 802.11 adapter has the given friendly name. The linked-
// list pointers alias buf, so buf must stay reachable for the whole walk.
func adaptersContainWirelessName(buf []byte, name string) bool {
	for aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); aa != nil; aa = aa.Next {
		if aa.IfType == windows.IF_TYPE_IEEE80211 && windows.UTF16PtrToString(aa.FriendlyName) == name {
			return true
		}
	}
	return false
}
