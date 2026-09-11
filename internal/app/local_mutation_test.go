package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/firewall"
)

func TestManagedNodeLocalDesiredStateMutationsAdvanceGeneration(t *testing.T) {
	originalRegisterWarp := registerWarp
	registerWarp = func(_ context.Context, privateKey, _ string) (config.Warp, error) {
		return config.Warp{
			InterfaceName:       "warp0",
			DeviceID:            "device-id",
			AccessToken:         "access-token",
			PrivateKey:          privateKey,
			PeerPublicKey:       "warp-peer-public-key",
			Endpoint:            "engage.cloudflareclient.com:2408",
			AddressV4:           "172.16.0.2/32",
			MTU:                 1280,
			PersistentKeepalive: 25,
		}, nil
	}
	t.Cleanup(func() { registerWarp = originalRegisterWarp })

	tests := []struct {
		name   string
		mutate func(*Service, config.State, string, string) error
	}{
		{
			name: "create tunnel",
			mutate: func(service *Service, _ config.State, _, _ string) error {
				_, err := service.CreateTunnel("awg_legacy_1_0", "local", "10.99.0.0/24", 51999)
				return err
			},
		},
		{
			name: "create WARP tunnel",
			mutate: func(service *Service, _ config.State, _, _ string) error {
				_, err := service.CreateTunnelWithOptions(context.Background(), TunnelCreateOptions{
					ProfileID:  "awg_legacy_1_0",
					Name:       "local-warp",
					Subnet:     "10.98.0.0/24",
					Port:       51998,
					EgressMode: config.EgressWarp,
				})
				return err
			},
		},
		{
			name: "update tunnel settings",
			mutate: func(service *Service, state config.State, _, _ string) error {
				tunnel := state.Tunnels[0]
				_, err := service.UpdateTunnelSettings(tunnel.ID, TunnelSettingsUpdate{
					Name:       tunnel.Name,
					ServerHost: tunnel.ServerHost,
					EgressMode: tunnel.EgressMode,
					Subnet:     tunnel.IPv4Subnet,
					DNS:        "9.9.9.9",
					AllowedIPs: tunnel.AllowedIPs,
					Keepalive:  tunnel.Keepalive,
					MTU:        tunnel.MTU,
					Port:       tunnel.ListenPort,
					Enabled:    tunnel.Enabled,
				})
				return err
			},
		},
		{
			name: "delete tunnel",
			mutate: func(service *Service, _ config.State, _, secondTunnelID string) error {
				return service.DeleteTunnel(secondTunnelID)
			},
		},
		{
			name: "update protocol",
			mutate: func(service *Service, state config.State, _, _ string) error {
				return service.UpdateTunnelProtocol(state.Tunnels[0].ID, "awg_legacy_1_0", config.ProtocolParams{})
			},
		},
		{
			name: "regenerate protocol",
			mutate: func(service *Service, state config.State, _, _ string) error {
				return service.RegenerateTunnelProtocol(state.Tunnels[0].ID, "awg_legacy_1_0")
			},
		},
		{
			name: "add client",
			mutate: func(service *Service, state config.State, _, _ string) error {
				persisted := false
				_, err := service.AddClientToTunnelWithOptions(state.Tunnels[0].ID, "local-client", ClientCreateOptions{
					Persist: func(config.Client) error {
						persisted = true
						return nil
					},
					RollbackPersist: func(config.Client) error { return nil },
				})
				if err == nil && !persisted {
					return errors.New("client persistence did not run")
				}
				return err
			},
		},
		{
			name: "remove client",
			mutate: func(service *Service, _ config.State, clientID, _ string) error {
				return service.RemoveClient(clientID)
			},
		},
		{
			name: "set client enabled",
			mutate: func(service *Service, _ config.State, clientID, _ string) error {
				return service.SetClientEnabled(clientID, false)
			},
		},
		{
			name: "disable client for traffic limit",
			mutate: func(service *Service, _ config.State, clientID, _ string) error {
				_, err := service.DisableClientForTrafficLimit(clientID, 2, 1, "lifetime")
				return err
			},
		},
		{
			name: "update client settings",
			mutate: func(service *Service, _ config.State, clientID, _ string) error {
				_, err := service.UpdateClientSettings(clientID, "renamed", "local")
				return err
			},
		},
		{
			name: "register WARP",
			mutate: func(service *Service, _ config.State, _, _ string) error {
				_, err := service.RegisterWarp(context.Background())
				return err
			},
		},
		{
			name: "import WARP",
			mutate: func(service *Service, _ config.State, _, _ string) error {
				_, err := service.ImportWarpConfig(testWarpConfig("172.16.0.3/32"))
				return err
			},
		},
		{
			name: "delete WARP",
			mutate: func(service *Service, _ config.State, _, _ string) error {
				return service.DeleteWarpConfig(context.Background())
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, state, clientID, secondTunnelID := managedMutationTestService(t)
			before := state.ManagedNode.DesiredGeneration
			if err := test.mutate(service, state, clientID, secondTunnelID); err != nil {
				t.Fatal(err)
			}
			assertManagedGeneration(t, service, before+1)
		})
	}
}

func TestManagedNodeTrafficLimitReleaseAdvancesGeneration(t *testing.T) {
	service, state, clientID, _ := managedMutationTestService(t)
	state.Tunnels[0].Clients[0].Enabled = false
	if err := service.store.Save(state); err != nil {
		t.Fatal(err)
	}

	released, err := service.EnableClientForTrafficLimitRelease(clientID, "rolling_30d")
	if err != nil {
		t.Fatal(err)
	}
	if !released {
		t.Fatal("client was not released")
	}
	assertManagedGeneration(t, service, state.ManagedNode.DesiredGeneration+1)
}

func TestManagedNodeAutomaticRepairAdvancesGeneration(t *testing.T) {
	service, state, _, _ := managedMutationTestService(t)
	state.ExternalInterface = ""
	if err := service.store.Save(state); err != nil {
		t.Fatal(err)
	}

	repaired, err := service.Init()
	if err != nil {
		t.Fatal(err)
	}
	if repaired.ExternalInterface != service.cfg.ExternalInterface {
		t.Fatalf("external interface = %q, want %q", repaired.ExternalInterface, service.cfg.ExternalInterface)
	}
	assertManagedGeneration(t, service, state.ManagedNode.DesiredGeneration+1)
}

func TestManagedNodeLocalMutationFencesStaleControllerOperation(t *testing.T) {
	service, state, clientID, _ := managedMutationTestService(t)
	if err := service.SetClientEnabled(clientID, false); err != nil {
		t.Fatal(err)
	}
	assertManagedGeneration(t, service, state.ManagedNode.DesiredGeneration+1)

	mutateCalled := false
	_, err := service.commitDesiredState(desiredStateMutation{
		Request: testDesiredMutationRequest(),
		Mutate: func(*config.State) error {
			mutateCalled = true
			return nil
		},
		Apply:    func(config.State, config.State) error { return nil },
		Rollback: func(config.State, config.State) error { return nil },
	})
	if !errors.Is(err, ErrDesiredGenerationMismatch) {
		t.Fatalf("error = %v, want %v", err, ErrDesiredGenerationMismatch)
	}
	if mutateCalled {
		t.Fatal("stale controller operation reached mutation callback")
	}
}

func TestManagedNodeControllerOperationContinuesFromLocalGeneration(t *testing.T) {
	service, state, clientID, _ := managedMutationTestService(t)
	if err := service.SetClientEnabled(clientID, false); err != nil {
		t.Fatal(err)
	}

	request := testDesiredMutationRequest()
	request.ExpectedDesiredGeneration = state.ManagedNode.DesiredGeneration + 1
	result, err := service.commitDesiredState(desiredStateMutation{
		Request: request,
		Mutate: func(candidate *config.State) error {
			candidate.Tunnels[0].DNS = "9.9.9.9"
			return nil
		},
		Apply:    func(config.State, config.State) error { return nil },
		Rollback: func(config.State, config.State) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.DesiredGeneration != request.ExpectedDesiredGeneration+1 {
		t.Fatalf("receipt generation = %d, want %d", result.Receipt.DesiredGeneration, request.ExpectedDesiredGeneration+1)
	}
	assertManagedGeneration(t, service, request.ExpectedDesiredGeneration+1)
}

func TestManagedNodeLocalMutationRejectsGenerationExhaustion(t *testing.T) {
	service, state, clientID, _ := managedMutationTestService(t)
	state.ManagedNode.DesiredGeneration = math.MaxUint64
	if err := service.store.Save(state); err != nil {
		t.Fatal(err)
	}

	if err := service.SetClientEnabled(clientID, false); !errors.Is(err, ErrDesiredGenerationExhausted) {
		t.Fatalf("error = %v, want %v", err, ErrDesiredGenerationExhausted)
	}
	assertStateEqual(t, service, state)
}

func TestManagedNodeFailedLocalMutationRestoresGeneration(t *testing.T) {
	service, state := runtimeTransactionService(t)
	state.ManagedNode = testManagedNodeState()
	if err := service.store.Save(state); err != nil {
		t.Fatal(err)
	}
	recorder := runtimeRecorder{failApplyCall: 1}
	service.runtimeOps = recorder.operations()

	tunnel := state.Tunnels[0]
	update := tunnelSettingsUpdate(tunnel, tunnel.Enabled)
	update.MTU = 1280
	if _, err := service.UpdateTunnelSettings(tunnel.ID, update); err == nil {
		t.Fatal("expected runtime apply failure")
	}

	current, err := service.State()
	if err != nil {
		t.Fatal(err)
	}
	if current.Tunnels[0].MTU != tunnel.MTU {
		t.Fatalf("MTU after rollback = %d, want %d", current.Tunnels[0].MTU, tunnel.MTU)
	}
	assertManagedGeneration(t, service, state.ManagedNode.DesiredGeneration)
}

func TestManagedNodeFailedWarpMutationRestoresGeneration(t *testing.T) {
	service, state := runtimeTransactionService(t)
	state.ManagedNode = testManagedNodeState()
	if err := service.store.Save(state); err != nil {
		t.Fatal(err)
	}
	recorder := runtimeRecorder{failWarpCall: 1}
	service.runtimeOps = recorder.operations()

	if _, err := service.ImportWarpConfig(testWarpConfig("172.16.0.3/32")); err == nil {
		t.Fatal("expected WARP apply failure")
	}

	current, err := service.State()
	if err != nil {
		t.Fatal(err)
	}
	if current.Warp.AddressV4 != state.Warp.AddressV4 {
		t.Fatalf("WARP address after rollback = %q, want %q", current.Warp.AddressV4, state.Warp.AddressV4)
	}
	assertManagedGeneration(t, service, state.ManagedNode.DesiredGeneration)
}

func TestManagedNodeSerializesControllerAndLocalMutations(t *testing.T) {
	service, state, clientID, _ := managedMutationTestService(t)
	localService := New(service.cfg)
	mutateEntered := make(chan struct{})
	releaseMutation := make(chan struct{})
	localStarted := make(chan struct{})
	controllerDone := make(chan error, 1)
	localDone := make(chan error, 1)

	go func() {
		_, err := service.commitDesiredState(desiredStateMutation{
			Request: testDesiredMutationRequest(),
			Mutate: func(candidate *config.State) error {
				close(mutateEntered)
				<-releaseMutation
				candidate.Tunnels[0].DNS = "9.9.9.9"
				return nil
			},
			Apply:    func(config.State, config.State) error { return nil },
			Rollback: func(config.State, config.State) error { return nil },
		})
		controllerDone <- err
	}()
	<-mutateEntered

	go func() {
		close(localStarted)
		localDone <- localService.SetClientEnabled(clientID, false)
	}()
	<-localStarted
	select {
	case err := <-localDone:
		t.Fatalf("local mutation completed before controller transaction released its cross-process lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseMutation)

	if err := <-controllerDone; err != nil {
		t.Fatalf("controller mutation failed: %v", err)
	}
	if err := <-localDone; err != nil {
		t.Fatalf("local mutation failed: %v", err)
	}

	current, err := service.State()
	if err != nil {
		t.Fatal(err)
	}
	if current.Tunnels[0].DNS != "9.9.9.9" || current.Tunnels[0].Clients[0].Enabled {
		t.Fatalf("serialized state = %#v", current.Tunnels[0])
	}
	assertManagedGeneration(t, service, state.ManagedNode.DesiredGeneration+2)
}

func TestFirewallRepairSerializesWithDesiredStateMutation(t *testing.T) {
	service := New(testServiceConfig(t))
	if _, err := service.Init(); err != nil {
		t.Fatal(err)
	}
	secondTunnel, err := service.CreateTunnel("awg_legacy_1_0", "awg1", "10.9.0.0/24", 51821)
	if err != nil {
		t.Fatal(err)
	}

	repairEntered := make(chan struct{})
	releaseRepair := make(chan struct{})
	service.runtimeOps.repairFirewall = func(_ config.Config, state config.State) (firewall.Report, error) {
		if len(state.Tunnels) != 2 {
			return firewall.Report{}, fmt.Errorf("repair tunnel count = %d, want 2", len(state.Tunnels))
		}
		close(repairEntered)
		<-releaseRepair
		return firewall.Report{ApplyEnabled: true}, nil
	}

	repairDone := make(chan error, 1)
	go func() {
		_, err := service.FirewallRepair()
		repairDone <- err
	}()
	<-repairEntered

	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- New(service.cfg).DeleteTunnel(secondTunnel.ID)
	}()
	select {
	case err := <-mutationDone:
		t.Fatalf("state mutation completed while firewall repair used its snapshot: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseRepair)

	if err := <-repairDone; err != nil {
		t.Fatalf("firewall repair failed: %v", err)
	}
	if err := <-mutationDone; err != nil {
		t.Fatalf("state mutation failed: %v", err)
	}
}

func TestManagedNodeNoOpLocalMutationDoesNotAdvanceGeneration(t *testing.T) {
	service, state, clientID, _ := managedMutationTestService(t)
	if err := service.SetClientEnabled(clientID, true); err != nil {
		t.Fatal(err)
	}
	assertManagedGeneration(t, service, state.ManagedNode.DesiredGeneration)
}

func TestManagedNodeRuntimeMaintenanceDoesNotAdvanceGeneration(t *testing.T) {
	service, state, _, _ := managedMutationTestService(t)
	if err := service.RestartTunnelByID(state.Tunnels[0].ID); err != nil {
		t.Fatal(err)
	}
	assertManagedGeneration(t, service, state.ManagedNode.DesiredGeneration)
}

func managedMutationTestService(t *testing.T) (*Service, config.State, string, string) {
	t.Helper()
	service := New(testServiceConfig(t))
	state, err := service.Init()
	if err != nil {
		t.Fatal(err)
	}
	client, err := service.AddClientToTunnel(state.Tunnels[0].ID, "phone")
	if err != nil {
		t.Fatal(err)
	}
	secondTunnel, err := service.CreateTunnel("awg_legacy_1_0", "awg1", "10.9.0.0/24", 51821)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImportWarpConfig(testWarpConfig("172.16.0.2/32")); err != nil {
		t.Fatal(err)
	}
	state, err = service.State()
	if err != nil {
		t.Fatal(err)
	}
	state.ManagedNode = testManagedNodeState()
	if err := service.store.Save(state); err != nil {
		t.Fatal(err)
	}
	return service, state, client.ID, secondTunnel.ID
}

func testWarpConfig(address string) string {
	return `[Interface]
PrivateKey = warp-private-key
Address = ` + address + `

[Peer]
PublicKey = warp-peer-public-key
Endpoint = engage.cloudflareclient.com:2408
PersistentKeepalive = 25
`
}

func assertManagedGeneration(t *testing.T, service *Service, want uint64) {
	t.Helper()
	state, err := service.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.ManagedNode == nil || state.ManagedNode.DesiredGeneration != want {
		t.Fatalf("managed state = %#v, want desired generation %d", state.ManagedNode, want)
	}
}
