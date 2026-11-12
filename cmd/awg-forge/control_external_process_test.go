package main

import (
	"net"
	"net/netip"
	"os"
	"runtime"
	"testing"
)

// TestControlNonLoopbackProcesses uses a real interface on Linux root. It runs
// the full approval/presence/renewal/restart/revocation/restore process contract
// with the unchanged local browser UI and explicit external listener consent.
func TestControlNonLoopbackProcesses(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("non-loopback process gate requires Linux root")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range addresses {
			prefix, err := netip.ParsePrefix(value.String())
			if err != nil {
				continue
			}
			address := prefix.Addr()
			if address.Is4() && address.IsGlobalUnicast() && !address.IsLoopback() {
				runEnrollmentProcesses(t, address.String())
				return
			}
		}
	}
	t.Fatal("Linux non-loopback process gate requires an active IPv4 interface")
}
