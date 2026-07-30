package app_test

import (
	"strings"
	"testing"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/render"
)

func TestAWG3RequiresExplicitExperimentalFlag(t *testing.T) {
	cfg := testConfig(t)
	cfg.ProtocolProfile = "awg_3_0"
	if _, err := app.New(cfg).Init(); err == nil || !strings.Contains(err.Error(), "AWG3_EXPERIMENTAL=true") {
		t.Fatalf("expected explicit AWG3 opt-in error, got %v", err)
	}
}

func TestAWG3RequiresCompatibleRuntimeImage(t *testing.T) {
	cfg := testConfig(t)
	cfg.ProtocolProfile = "awg_3_0"
	cfg.AWG3Experimental = true
	if _, err := app.New(cfg).Init(); err == nil || !strings.Contains(err.Error(), "experimental image") {
		t.Fatalf("expected experimental image error, got %v", err)
	}
}

func TestAWG3RegenerationRotatesSecretAndRevision(t *testing.T) {
	cfg := testConfig(t)
	cfg.AWG3Experimental = true
	cfg.AWG3Runtime = true
	svc := app.New(cfg)
	tunnel, err := svc.CreateTunnel("awg_3_0", "awg30", "10.30.0.0/24", 51840)
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	beforeTunnel := findTunnel(t, before, tunnel.ID)
	oldSecret := beforeTunnel.ProtocolSecrets["HeaderProtectionKey"]
	oldRevision := beforeTunnel.ConfigRevision
	if oldSecret == "" {
		t.Fatal("missing AWG3 header protection key")
	}
	if err := svc.RegenerateTunnelProtocol(tunnel.ID, "awg_3_0"); err != nil {
		t.Fatal(err)
	}
	after, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	updated := findTunnel(t, after, tunnel.ID)
	if updated.ProtocolSecrets["HeaderProtectionKey"] == oldSecret {
		t.Fatal("AWG3 regeneration did not rotate the header protection key")
	}
	if updated.ConfigRevision <= oldRevision {
		t.Fatal("AWG3 regeneration did not increase config revision")
	}
	client, err := svc.AddClientToTunnel(tunnel.ID, "AWG3 Phone")
	if err != nil {
		t.Fatal(err)
	}
	conf, _, err := svc.ClientConfigForDownload(client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conf, "HeaderProtectionKey = "+updated.ProtocolSecrets["HeaderProtectionKey"]) {
		t.Fatal("AWG3 client config does not contain the current header protection key")
	}
	serverConf, err := render.ServerConfig(after, updated)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(serverConf, "HeaderProtectionKey = "+updated.ProtocolSecrets["HeaderProtectionKey"]) {
		t.Fatal("AWG3 server config does not contain the current header protection key")
	}
}

func TestAWG3PersistentKeepaliveRangeUpdatesClientConfigAndRevision(t *testing.T) {
	cfg := testConfig(t)
	cfg.AWG3Experimental = true
	cfg.AWG3Runtime = true
	svc := app.New(cfg)
	tunnel, err := svc.CreateTunnel("awg_3_0", "awg30", "10.30.0.0/24", 51840)
	if err != nil {
		t.Fatal(err)
	}
	client, err := svc.AddClientToTunnel(tunnel.ID, "AWG3 Phone")
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	beforeTunnel := findTunnel(t, before, tunnel.ID)
	params := make(config.ProtocolParams, len(beforeTunnel.ProtocolParams))
	for key, value := range beforeTunnel.ProtocolParams {
		params[key] = value
	}
	params["PersistentKeepalive"] = "20-30"
	if err := svc.UpdateTunnelProtocol(tunnel.ID, "awg_3_0", params); err != nil {
		t.Fatal(err)
	}
	after, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	updated := findTunnel(t, after, tunnel.ID)
	if updated.ConfigRevision <= beforeTunnel.ConfigRevision {
		t.Fatal("AWG3 PersistentKeepalive update did not increase config revision")
	}
	conf, _, err := svc.ClientConfigForDownload(client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conf, "PersistentKeepalive = 20-30") {
		t.Fatalf("AWG3 client config did not use the persistent keepalive range:\n%s", conf)
	}
}

func findTunnel(t *testing.T, state config.State, id string) config.Tunnel {
	t.Helper()
	for _, tunnel := range state.Tunnels {
		if tunnel.ID == id {
			return tunnel
		}
	}
	t.Fatalf("tunnel %s not found", id)
	return config.Tunnel{}
}
