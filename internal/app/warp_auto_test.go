package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
)

func TestUpdateTunnelSettingsAutoRegistersWarpEgress(t *testing.T) {
	oldRegisterWarp := registerWarp
	defer func() {
		registerWarp = oldRegisterWarp
	}()
	registerCalls := 0
	registerWarp = func(_ context.Context, privateKey, _ string) (config.Warp, error) {
		registerCalls++
		return config.Warp{
			InterfaceName:       "warp0",
			DeviceID:            "device-id",
			AccessToken:         "access-token",
			LicenseKey:          "license-key",
			ClientID:            "client-id",
			PrivateKey:          privateKey,
			PeerPublicKey:       "warp-peer-public-key",
			Endpoint:            "engage.cloudflareclient.com:2408",
			AddressV4:           "172.16.0.2",
			MTU:                 1280,
			PersistentKeepalive: 25,
			RegisteredAt:        time.Now().UTC(),
			UpdatedAt:           time.Now().UTC(),
		}, nil
	}

	svc := New(testServiceConfig(t))
	client, err := svc.AddClient("phone")
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	tunnel := state.Tunnels[0]
	revision := tunnel.ConfigRevision

	updated, err := svc.UpdateTunnelSettings(tunnel.ID, TunnelSettingsUpdate{
		Name:       tunnel.Name,
		ServerHost: tunnel.ServerHost,
		EgressMode: config.EgressWarp,
		Subnet:     tunnel.IPv4Subnet,
		DNS:        tunnel.DNS,
		AllowedIPs: tunnel.AllowedIPs,
		Keepalive:  tunnel.Keepalive,
		MTU:        tunnel.MTU,
		Port:       tunnel.ListenPort,
		Enabled:    tunnel.Enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if registerCalls != 1 {
		t.Fatalf("register calls = %d, want 1", registerCalls)
	}
	if updated.EgressMode != config.EgressWarp {
		t.Fatalf("egress mode = %q, want warp", updated.EgressMode)
	}
	state, err = svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Warp.Registered() {
		t.Fatal("expected WARP to be registered")
	}
	if state.Tunnels[0].ConfigRevision != revision {
		t.Fatalf("tunnel revision changed from %d to %d", revision, state.Tunnels[0].ConfigRevision)
	}
	if state.Tunnels[0].Clients[0].ConfigRevision != revision {
		t.Fatal("client config should remain fresh after automatic WARP egress registration")
	}
	conf, _, err := svc.ClientConfigForDownload(client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conf, "Endpoint = vpn.example.com:51820") {
		t.Fatalf("client config changed unexpectedly:\n%s", conf)
	}
}

func TestCreateTunnelAutoRegistersWarpEgress(t *testing.T) {
	oldRegisterWarp := registerWarp
	defer func() {
		registerWarp = oldRegisterWarp
	}()
	registerCalls := 0
	registerWarp = func(_ context.Context, privateKey, _ string) (config.Warp, error) {
		registerCalls++
		return config.Warp{
			InterfaceName:       "warp0",
			DeviceID:            "device-id",
			AccessToken:         "access-token",
			LicenseKey:          "license-key",
			ClientID:            "client-id",
			PrivateKey:          privateKey,
			PeerPublicKey:       "warp-peer-public-key",
			Endpoint:            "engage.cloudflareclient.com:2408",
			AddressV4:           "172.16.0.2",
			MTU:                 1280,
			PersistentKeepalive: 25,
			RegisteredAt:        time.Now().UTC(),
			UpdatedAt:           time.Now().UTC(),
		}, nil
	}

	svc := New(testServiceConfig(t))
	tunnel, err := svc.CreateTunnelWithOptions(context.Background(), TunnelCreateOptions{
		ProfileID:  "awg_2_0",
		Name:       "awg20",
		Subnet:     "10.20.0.0/24",
		Port:       51830,
		EgressMode: config.EgressWarp,
	})
	if err != nil {
		t.Fatal(err)
	}
	if registerCalls != 1 {
		t.Fatalf("register calls = %d, want 1", registerCalls)
	}
	if tunnel.EgressMode != config.EgressWarp {
		t.Fatalf("egress mode = %q, want warp", tunnel.EgressMode)
	}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Warp.Registered() {
		t.Fatal("expected WARP to be registered")
	}
	if state.Tunnels[1].EgressMode != config.EgressWarp {
		t.Fatalf("created tunnel egress = %q, want warp", state.Tunnels[1].EgressMode)
	}
}

func testServiceConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		ConfigDir:           t.TempDir(),
		TunnelName:          "awg0",
		ServerHost:          "vpn.example.com",
		ListenPort:          51820,
		WebUIHost:           "127.0.0.1",
		WebUIPort:           51821,
		ExternalInterface:   "eth0",
		IPv4Subnet:          "10.8.0.0/24",
		DNS:                 "1.1.1.1",
		AllowedIPs:          "0.0.0.0/0",
		PersistentKeepalive: 0,
		MTU:                 1420,
		ProtocolProfile:     "awg_legacy_1_0",
	}
}
