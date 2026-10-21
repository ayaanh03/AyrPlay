//go:build !windows

package airplay

// osInterfaceIsWireless is the non-Windows fallback for
// networkInterfaceIsWireless. On Linux the /sys/class/net markers are the
// authoritative wireless signal, so any interface that reaches here (no sysfs
// match) is treated as not wireless, preserving the prior behaviour.
func osInterfaceIsWireless(string) bool { return false }
