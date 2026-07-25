package app

import (
	"errors"
	"testing"

	"github.com/astronaut808/awg-forge/internal/config"
)

func TestSelectAutomaticUDPPort(t *testing.T) {
	cfg := config.Config{TunnelUDPPortRange: "30000-30003"}
	tunnels := []config.Tunnel{{ListenPort: 30001}}
	available := func(port int) bool {
		return port != 30002
	}
	port, portRange, err := selectAutomaticUDPPort(cfg, tunnels, func(limit int) (int, error) {
		if limit != 3 {
			t.Fatalf("candidate count = %d, want 3", limit)
		}
		return 1, nil
	}, available)
	if err != nil {
		t.Fatal(err)
	}
	if port != 30003 {
		t.Fatalf("port = %d, want 30003", port)
	}
	if portRange != "30000-30003" {
		t.Fatalf("range = %q", portRange)
	}
}

func TestSelectAutomaticUDPPortUsesPublishedIntersection(t *testing.T) {
	cfg := config.Config{
		TunnelUDPPortRange: "30000-30100",
		PublishedUDPPorts:  "30040-30049",
	}
	port, portRange, err := selectAutomaticUDPPort(cfg, nil, func(int) (int, error) {
		return 0, nil
	}, func(int) bool {
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if port != 30040 || portRange != "30040-30049" {
		t.Fatalf("port=%d range=%q", port, portRange)
	}
}

func TestSelectAutomaticUDPPortSkipsDeniedPorts(t *testing.T) {
	cfg := config.Config{TunnelUDPPortRange: "443,4500,30000"}
	port, _, err := selectAutomaticUDPPort(cfg, nil, func(int) (int, error) {
		return 0, nil
	}, func(int) bool {
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if port != 30000 {
		t.Fatalf("port = %d, want 30000", port)
	}
}

func TestSelectAutomaticUDPPortFailsWhenRangeIsExhausted(t *testing.T) {
	cfg := config.Config{TunnelUDPPortRange: "30000-30001"}
	_, _, err := selectAutomaticUDPPort(cfg, nil, func(int) (int, error) {
		return 0, nil
	}, func(int) bool {
		return false
	})
	if err == nil {
		t.Fatal("expected exhausted range to fail")
	}
}

func TestSelectAutomaticUDPPortPropagatesRandomFailure(t *testing.T) {
	cfg := config.Config{TunnelUDPPortRange: "30000"}
	_, _, err := selectAutomaticUDPPort(cfg, nil, func(int) (int, error) {
		return 0, errors.New("entropy unavailable")
	}, func(int) bool {
		return true
	})
	if err == nil {
		t.Fatal("expected random failure")
	}
}

func TestAutomaticUDPPortEligible(t *testing.T) {
	cfg := config.Config{TunnelUDPPortRange: "30000-30010"}
	eligible, portRange, err := automaticUDPPortEligible(cfg, 30005)
	if err != nil {
		t.Fatal(err)
	}
	if !eligible || portRange != "30000-30010" {
		t.Fatalf("eligible=%t range=%q", eligible, portRange)
	}
	eligible, _, err = automaticUDPPortEligible(cfg, 51820)
	if err != nil {
		t.Fatal(err)
	}
	if eligible {
		t.Fatal("port outside the automatic range must not be eligible")
	}
}
