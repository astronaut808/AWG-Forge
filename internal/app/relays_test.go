package app_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
)

func TestRelayEndpointChangesOnlyAssignedClients(t *testing.T) {
	svc := app.New(testConfig(t))
	state, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	tunnel := state.Tunnels[0]
	relay, err := svc.SaveRelay(tunnel.ID, "", config.Relay{
		Name: "Edge", Host: "edge.example.com", Port: 40123, TargetIPv4: "192.0.2.20",
	})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := svc.AddClientToTunnel(tunnel.ID, "direct")
	if err != nil {
		t.Fatal(err)
	}
	routed, err := svc.AddClientToTunnelWithOptions(tunnel.ID, "routed", app.ClientCreateOptions{RelayID: relay.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveRelay(tunnel.ID, relay.ID); err == nil {
		t.Fatal("deleted a relay that still has clients")
	}
	directConfig, err := svc.ClientConfig(direct.ID)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := tunnel.ClientEndpoint(state.ServerHost, direct)
	if err != nil || !strings.Contains(directConfig, "Endpoint = "+endpoint.String()+"\n") {
		t.Fatal("direct endpoint changed")
	}
	relay.TargetIPv4 = "192.0.2.21"
	if _, err := svc.SaveRelay(tunnel.ID, relay.ID, relay); err != nil {
		t.Fatal(err)
	}
	checkStale := func(want bool) {
		t.Helper()
		state, err := svc.State()
		if err != nil {
			t.Fatal(err)
		}
		current := state.Tunnels[0]
		if current.ConfigRevision != tunnel.ConfigRevision || current.NeedsClientConfig(current.Clients[0]) || current.NeedsClientConfig(current.Clients[1]) != want {
			t.Fatal("incorrect config revision state")
		}
	}
	checkStale(false)
	relay.Port = 40124
	if _, err := svc.SaveRelay(tunnel.ID, relay.ID, relay); err != nil {
		t.Fatal(err)
	}
	checkStale(true)
	key, _, err := svc.ClientImportKey(routed.ID)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, "vpn://"))
	if err != nil || !strings.Contains(string(decoded), "Endpoint = edge.example.com:40124\n") {
		t.Fatal("import key contains the wrong endpoint")
	}
	checkStale(false)

	directRelay := ""
	updated, err := svc.UpdateClientSettingsWithOptions(routed.ID, app.ClientSettingsUpdate{Name: routed.Name, RelayID: &directRelay})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RelayID != "" {
		t.Fatal("client is still assigned to a relay")
	}
	checkStale(true)
	if err := svc.RemoveRelay(tunnel.ID, relay.ID); err != nil {
		t.Fatal(err)
	}
}
