package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/warp"
)

func TestClientAndProtocolMutationsDoNotReconcileWarp(t *testing.T) {
	svc, state := runtimeTransactionService(t)
	recorder := runtimeRecorder{}
	svc.runtimeOps = recorder.operations()

	client, err := svc.AddClientToTunnel(state.Tunnels[0].ID, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateClientSettingsWithOptions(client.ID, ClientSettingsUpdate{
		Name:      client.Name,
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetClientEnabled(client.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetClientEnabled(client.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.RegenerateTunnelProtocol(state.Tunnels[0].ID, state.Tunnels[0].ProtocolProfileID); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveClient(client.ID); err != nil {
		t.Fatal(err)
	}

	if len(recorder.applied) != 6 {
		t.Fatalf("tunnel apply calls = %d, want 6", len(recorder.applied))
	}
	if len(recorder.routeCounts) != 0 {
		t.Fatalf("WARP reconcile calls = %d, want 0 for client and protocol mutations", len(recorder.routeCounts))
	}
}

func TestCreateWarpTunnelFailureRestoresPreviousRuntime(t *testing.T) {
	svc, previous := runtimeTransactionService(t)
	recorder := runtimeRecorder{failWarpCall: 1}
	svc.runtimeOps = recorder.operations()

	created, err := svc.CreateTunnelWithOptions(context.Background(), TunnelCreateOptions{
		ProfileID:  "awg_1_5",
		Name:       "awg15",
		Subnet:     "10.15.0.0/24",
		Port:       51825,
		EgressMode: config.EgressWarp,
	})
	if err == nil {
		t.Fatal("expected WARP apply failure")
	}
	var applyErr *ApplyError
	if !errors.As(err, &applyErr) {
		t.Fatalf("error = %v, want ApplyError", err)
	}
	if created.ID != "" {
		t.Fatalf("created tunnel = %#v, want zero value after rollback", created)
	}

	state, stateErr := svc.State()
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	if len(state.Tunnels) != len(previous.Tunnels) || state.Tunnels[0].ID != previous.Tunnels[0].ID {
		t.Fatalf("tunnels after rollback = %#v, want previous state", state.Tunnels)
	}
	if len(recorder.applied) != 1 {
		t.Fatalf("applied tunnels = %v, want only the new tunnel", recorder.applied)
	}
	if len(recorder.removed) != 1 || recorder.removed[0] != recorder.applied[0] {
		t.Fatalf("removed tunnels = %v, want failed new tunnel %v", recorder.removed, recorder.applied)
	}
	if len(recorder.routeCounts) != 2 || recorder.routeCounts[0] != 2 || recorder.routeCounts[1] != 1 {
		t.Fatalf("WARP route counts = %v, want failed [2] followed by restored [1]", recorder.routeCounts)
	}
	if state.Warp.LastApplyError != previous.Warp.LastApplyError {
		t.Fatalf("WARP error after rollback = %q, want previous %q", state.Warp.LastApplyError, previous.Warp.LastApplyError)
	}
}

func TestCreateWANTunnelDoesNotReconcileExistingWarpRoutes(t *testing.T) {
	svc, _ := runtimeTransactionService(t)
	recorder := runtimeRecorder{}
	svc.runtimeOps = recorder.operations()

	if _, err := svc.CreateTunnelWithOptions(context.Background(), TunnelCreateOptions{
		ProfileID:  "awg_1_5",
		Name:       "awg15",
		Subnet:     "10.15.0.0/24",
		Port:       51825,
		EgressMode: config.EgressWAN,
	}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.routeCounts) != 0 {
		t.Fatalf("WARP reconcile calls = %d, want 0 when route set is unchanged", len(recorder.routeCounts))
	}
}

func TestNonRoutingTunnelSettingsDoNotReconcileWarp(t *testing.T) {
	svc, state := runtimeTransactionService(t)
	recorder := runtimeRecorder{}
	svc.runtimeOps = recorder.operations()

	tunnel := state.Tunnels[0]
	update := tunnelSettingsUpdate(tunnel, true)
	update.DNS = "9.9.9.9"
	if _, err := svc.UpdateTunnelSettings(tunnel.ID, update); err != nil {
		t.Fatal(err)
	}
	if len(recorder.applied) != 1 {
		t.Fatalf("tunnel apply calls = %d, want 1", len(recorder.applied))
	}
	if len(recorder.routeCounts) != 0 {
		t.Fatalf("WARP reconcile calls = %d, want 0 for non-routing settings", len(recorder.routeCounts))
	}
}

func TestTunnelSettingsRejectReservedRelayPortBeforeRuntimeChanges(t *testing.T) {
	for _, tt := range []struct {
		name       string
		changeMTU  bool
		enableWarp bool
	}{
		{name: "port only"},
		{name: "port and restart", changeMTU: true},
		{name: "port and WARP registration", enableWarp: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := New(testServiceConfig(t))
			state, err := svc.State()
			if err != nil {
				t.Fatal(err)
			}
			svc.cfg.ApplyConfig = true
			recorder := runtimeRecorder{}
			svc.runtimeOps = recorder.operations()
			tunnel := state.Tunnels[0]
			update := tunnelSettingsUpdate(tunnel, true)
			update.Port++
			if tt.changeMTU {
				update.MTU = 1280
			}
			if tt.enableWarp {
				update.EgressMode = config.EgressWarp
				oldRegisterWarp := registerWarp
				t.Cleanup(func() { registerWarp = oldRegisterWarp })
				registerWarp = func(context.Context, string, string) (config.Warp, error) {
					t.Error("WARP registration started for a reserved port")
					return config.Warp{}, errors.New("unexpected WARP registration")
				}
			}
			svc.runtimeOps.managedRelayPorts = func() (map[int]bool, error) {
				return map[int]bool{update.Port: true}, nil
			}
			statePath := svc.store.StatePath()
			before, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}

			_, err = svc.UpdateTunnelSettings(tunnel.ID, update)
			if err == nil || !strings.Contains(err.Error(), "reserved by an awg-forge relay") {
				t.Errorf("update error = %v, want relay port conflict", err)
			}
			if len(recorder.removed) != 0 || len(recorder.applied) != 0 || len(recorder.routeCounts) != 0 {
				t.Errorf("runtime changed after conflict: removed %v, applied %v, reconciled %v", recorder.removed, recorder.applied, recorder.routeCounts)
			}
			after, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Error("persisted state changed after rejected update")
			}
		})
	}
}

func TestMTUChangeRestartsTunnelRuntime(t *testing.T) {
	for _, test := range []struct {
		name string
		mtu  int
	}{
		{name: "explicit", mtu: 1280},
		{name: "auto", mtu: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, state := runtimeTransactionService(t)
			recorder := runtimeRecorder{}
			svc.runtimeOps = recorder.operations()

			tunnel := state.Tunnels[0]
			update := tunnelSettingsUpdate(tunnel, true)
			update.MTU = test.mtu
			updated, err := svc.UpdateTunnelSettings(tunnel.ID, update)
			if err != nil {
				t.Fatal(err)
			}
			if updated.MTU != test.mtu {
				t.Fatalf("MTU = %d, want %d", updated.MTU, test.mtu)
			}
			if len(recorder.removed) != 1 || recorder.removed[0] != tunnel.ID {
				t.Fatalf("removed tunnels = %v, want [%s]", recorder.removed, tunnel.ID)
			}
			if len(recorder.applied) != 1 || recorder.applied[0] != tunnel.ID {
				t.Fatalf("applied tunnels = %v, want [%s]", recorder.applied, tunnel.ID)
			}
		})
	}
}

func TestSubnetChangeRestartsTunnelRuntime(t *testing.T) {
	installEmptyIPTables(t)
	svc, state := runtimeTransactionService(t)
	recorder := runtimeRecorder{}
	svc.runtimeOps = recorder.operations()

	tunnel := state.Tunnels[0]
	update := tunnelSettingsUpdate(tunnel, true)
	update.Subnet = "10.29.0.0/24"
	updated, err := svc.UpdateTunnelSettings(tunnel.ID, update)
	if err != nil {
		t.Fatal(err)
	}
	if updated.IPv4Subnet != "10.29.0.0/24" || updated.ServerAddress != "10.29.0.1" {
		t.Fatalf("updated addressing = %s, %s", updated.IPv4Subnet, updated.ServerAddress)
	}
	if len(recorder.removed) != 1 || len(recorder.applied) != 1 {
		t.Fatalf("runtime calls: remove=%d apply=%d, want 1/1", len(recorder.removed), len(recorder.applied))
	}
	if len(recorder.routeCounts) != 1 || recorder.routeCounts[0] != 1 {
		t.Fatalf("WARP route counts = %v, want [1]", recorder.routeCounts)
	}
}

func TestSubnetChangeWithClientsRemainsRejected(t *testing.T) {
	svc, state := runtimeTransactionService(t)
	recorder := runtimeRecorder{}
	svc.runtimeOps = recorder.operations()
	state.Tunnels[0].Clients = []config.Client{{ID: "client-1"}}
	if err := svc.store.Save(state); err != nil {
		t.Fatal(err)
	}

	tunnel := state.Tunnels[0]
	update := tunnelSettingsUpdate(tunnel, true)
	update.Subnet = "10.29.0.0/24"
	if _, err := svc.UpdateTunnelSettings(tunnel.ID, update); err == nil {
		t.Fatal("expected subnet change with existing clients to fail")
	}
	if len(recorder.removed) != 0 || len(recorder.applied) != 0 {
		t.Fatalf("runtime calls after rejected update: remove=%d apply=%d, want 0/0", len(recorder.removed), len(recorder.applied))
	}
}

func TestInterfaceRenameRestartsTunnelRuntime(t *testing.T) {
	installEmptyIPTables(t)
	svc, state := runtimeTransactionService(t)
	recorder := runtimeRecorder{}
	svc.runtimeOps = recorder.operations()

	tunnel := state.Tunnels[0]
	update := tunnelSettingsUpdate(tunnel, true)
	update.Name = "awg-renamed"
	updated, err := svc.UpdateTunnelSettings(tunnel.ID, update)
	if err != nil {
		t.Fatal(err)
	}
	if updated.InterfaceName != "awg-renamed" {
		t.Fatalf("interface name = %q, want awg-renamed", updated.InterfaceName)
	}
	if len(recorder.removed) != 1 || len(recorder.applied) != 1 {
		t.Fatalf("runtime calls: remove=%d apply=%d, want 1/1", len(recorder.removed), len(recorder.applied))
	}
}

func installEmptyIPTables(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\ncase \" $* \" in\n  *\" -C \"*) exit 1 ;;\n  *) exit 0 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(binDir, "iptables"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
}

func TestMTURestartRemovalFailureRollsBackStateAndRuntime(t *testing.T) {
	svc, state := runtimeTransactionService(t)
	recorder := runtimeRecorder{failRemoveCall: 1}
	svc.runtimeOps = recorder.operations()

	tunnel := state.Tunnels[0]
	update := tunnelSettingsUpdate(tunnel, true)
	update.MTU = 1280
	if _, err := svc.UpdateTunnelSettings(tunnel.ID, update); err == nil {
		t.Fatal("expected runtime removal failure")
	}

	current, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if current.Tunnels[0].MTU != tunnel.MTU {
		t.Fatalf("MTU after rollback = %d, want %d", current.Tunnels[0].MTU, tunnel.MTU)
	}
	if len(recorder.removed) != 2 || len(recorder.applied) != 1 {
		t.Fatalf("runtime rollback calls: remove=%d apply=%d, want 2/1", len(recorder.removed), len(recorder.applied))
	}
}

func TestMTURestartApplyFailureRollsBackStateAndRuntime(t *testing.T) {
	svc, state := runtimeTransactionService(t)
	recorder := runtimeRecorder{failApplyCall: 1}
	svc.runtimeOps = recorder.operations()

	tunnel := state.Tunnels[0]
	update := tunnelSettingsUpdate(tunnel, true)
	update.MTU = 1280
	if _, err := svc.UpdateTunnelSettings(tunnel.ID, update); err == nil {
		t.Fatal("expected runtime apply failure")
	}

	current, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if current.Tunnels[0].MTU != tunnel.MTU {
		t.Fatalf("MTU after rollback = %d, want %d", current.Tunnels[0].MTU, tunnel.MTU)
	}
	if len(recorder.removed) != 2 || len(recorder.applied) != 2 {
		t.Fatalf("runtime rollback calls: remove=%d apply=%d, want 2/2", len(recorder.removed), len(recorder.applied))
	}
}

func TestRestartTunnelRecreatesRuntime(t *testing.T) {
	svc, state := runtimeTransactionService(t)
	recorder := runtimeRecorder{}
	svc.runtimeOps = recorder.operations()

	if err := svc.RestartTunnelByID(state.Tunnels[0].ID); err != nil {
		t.Fatal(err)
	}
	if len(recorder.removed) != 1 || len(recorder.applied) != 1 {
		t.Fatalf("runtime restart calls: remove=%d apply=%d, want 1/1", len(recorder.removed), len(recorder.applied))
	}
}

func TestRestartTunnelRemovalFailureAttemptsRestore(t *testing.T) {
	svc, state := runtimeTransactionService(t)
	recorder := runtimeRecorder{failRemoveCall: 1}
	svc.runtimeOps = recorder.operations()

	if err := svc.RestartTunnelByID(state.Tunnels[0].ID); err == nil {
		t.Fatal("expected runtime removal failure")
	}
	if len(recorder.removed) != 1 || len(recorder.applied) != 1 {
		t.Fatalf("runtime recovery calls: remove=%d apply=%d, want 1/1", len(recorder.removed), len(recorder.applied))
	}
}

func TestDisableAndEnableWarpTunnelUpdatesBothRuntimes(t *testing.T) {
	svc, state := runtimeTransactionService(t)
	recorder := runtimeRecorder{}
	svc.runtimeOps = recorder.operations()

	tunnel := state.Tunnels[0]
	disabled, err := svc.UpdateTunnelSettings(tunnel.ID, tunnelSettingsUpdate(tunnel, false))
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Enabled {
		t.Fatal("tunnel remains enabled after disable")
	}
	if len(recorder.removed) != 1 || len(recorder.applied) != 0 {
		t.Fatalf("runtime calls after disable: remove=%d apply=%d, want 1/0", len(recorder.removed), len(recorder.applied))
	}

	enabled, err := svc.UpdateTunnelSettings(tunnel.ID, tunnelSettingsUpdate(disabled, true))
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled {
		t.Fatal("tunnel remains disabled after enable")
	}
	if len(recorder.removed) != 1 || len(recorder.applied) != 1 {
		t.Fatalf("runtime calls after enable: remove=%d apply=%d, want 1/1", len(recorder.removed), len(recorder.applied))
	}
	if len(recorder.routeCounts) != 2 || recorder.routeCounts[0] != 0 || recorder.routeCounts[1] != 1 {
		t.Fatalf("WARP route counts = %v, want [0 1]", recorder.routeCounts)
	}
}

func TestDisableWarpTunnelFailureRestoresPreviousRuntime(t *testing.T) {
	svc, state := runtimeTransactionService(t)
	recorder := runtimeRecorder{failWarpCall: 1}
	svc.runtimeOps = recorder.operations()

	tunnel := state.Tunnels[0]
	if _, err := svc.UpdateTunnelSettings(tunnel.ID, tunnelSettingsUpdate(tunnel, false)); err == nil {
		t.Fatal("expected WARP apply failure")
	}

	current, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	if !current.Tunnels[0].Enabled {
		t.Fatal("disabled tunnel state was not rolled back")
	}
	if len(recorder.removed) != 2 || len(recorder.applied) != 1 {
		t.Fatalf("runtime rollback calls: remove=%d apply=%d, want 2/1", len(recorder.removed), len(recorder.applied))
	}
	if len(recorder.routeCounts) != 2 || recorder.routeCounts[0] != 0 || recorder.routeCounts[1] != 1 {
		t.Fatalf("WARP route counts = %v, want failed [0] followed by restored [1]", recorder.routeCounts)
	}
}

func TestDeleteTunnelReconcilesWarpOnlyForWarpRoute(t *testing.T) {
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "iptables"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	svc, _ := runtimeTransactionService(t)
	recorder := runtimeRecorder{}
	svc.runtimeOps = recorder.operations()

	wanTunnel, err := svc.CreateTunnelWithOptions(context.Background(), TunnelCreateOptions{
		ProfileID:  "awg_1_5",
		Name:       "awg15",
		Subnet:     "10.15.0.0/24",
		Port:       51825,
		EgressMode: config.EgressWAN,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteTunnel(wanTunnel.ID); err != nil {
		t.Fatal(err)
	}
	if len(recorder.routeCounts) != 0 {
		t.Fatalf("WARP reconcile calls after WAN delete = %v, want none", recorder.routeCounts)
	}

	warpTunnel, err := svc.CreateTunnelWithOptions(context.Background(), TunnelCreateOptions{
		ProfileID:  "awg_1_5",
		Name:       "awg15",
		Subnet:     "10.15.0.0/24",
		Port:       51825,
		EgressMode: config.EgressWarp,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder.routeCounts = nil
	if err := svc.DeleteTunnel(warpTunnel.ID); err != nil {
		t.Fatal(err)
	}
	if len(recorder.routeCounts) != 1 || recorder.routeCounts[0] != 1 {
		t.Fatalf("WARP route counts after WARP delete = %v, want [1]", recorder.routeCounts)
	}
	if len(recorder.removed) != 2 {
		t.Fatalf("removed tunnel runtimes = %d, want 2", len(recorder.removed))
	}
}

func runtimeTransactionService(t *testing.T) (*Service, config.State) {
	t.Helper()
	cfg := testServiceConfig(t)
	svc := New(cfg)
	state, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	state.Warp = config.Warp{
		InterfaceName:       "warp0",
		PrivateKey:          "warp-private",
		PeerPublicKey:       "warp-public",
		Endpoint:            "warp.example:2408",
		AddressV4:           "172.16.0.2/32",
		MTU:                 1280,
		PersistentKeepalive: 25,
	}
	state.Tunnels[0].EgressMode = config.EgressWarp
	if err := svc.store.Save(state); err != nil {
		t.Fatal(err)
	}
	svc.cfg.ApplyConfig = true
	return svc, state
}

func tunnelSettingsUpdate(tunnel config.Tunnel, enabled bool) TunnelSettingsUpdate {
	return TunnelSettingsUpdate{
		Name:       tunnel.Name,
		ServerHost: tunnel.ServerHost,
		EgressMode: tunnel.EgressMode,
		Subnet:     tunnel.IPv4Subnet,
		DNS:        tunnel.DNS,
		AllowedIPs: tunnel.AllowedIPs,
		Keepalive:  tunnel.Keepalive,
		MTU:        tunnel.MTU,
		Port:       tunnel.ListenPort,
		Enabled:    enabled,
	}
}

type runtimeRecorder struct {
	applied        []string
	removed        []string
	routeCounts    []int
	failApplyCall  int
	failRemoveCall int
	failWarpCall   int
}

func (r *runtimeRecorder) operations() runtimeOperations {
	return runtimeOperations{
		managedRelayPorts: func() (map[int]bool, error) { return nil, nil },
		applyTunnel: func(tunnel config.Tunnel) error {
			r.applied = append(r.applied, tunnel.ID)
			if r.failApplyCall > 0 && len(r.applied) == r.failApplyCall {
				return errors.New("forced tunnel apply failure")
			}
			return nil
		},
		removeTunnel: func(tunnel config.Tunnel) error {
			r.removed = append(r.removed, tunnel.ID)
			if r.failRemoveCall > 0 && len(r.removed) == r.failRemoveCall {
				return errors.New("forced tunnel removal failure")
			}
			return nil
		},
		reconcileWarp: func(state config.State) error {
			r.routeCounts = append(r.routeCounts, len(warp.RoutesForState(state)))
			if r.failWarpCall > 0 && len(r.routeCounts) == r.failWarpCall {
				return errors.New("forced WARP apply failure")
			}
			return nil
		},
	}
}
