// Command doubletake-discover lists AirPlay receivers found via mDNS on the local
// network and exits. It is a thin wrapper over the same discovery the sender uses,
// handy for confirming a device is reachable and for finding its current IP.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"doubletake/internal/airplay"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	devices, err := airplay.DiscoverAirPlayDevices(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "discovery error:", err)
		os.Exit(1)
	}
	if len(devices) == 0 {
		fmt.Println("No AirPlay devices found on the local network.")
		return
	}
	fmt.Printf("Found %d AirPlay receiver(s):\n", len(devices))
	for _, d := range devices {
		fmt.Printf("  %-24s %s:%d  (model=%s)\n", d.Name, d.IP, d.Port, d.Model)
	}
}
