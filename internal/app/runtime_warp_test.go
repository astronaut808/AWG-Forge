package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/warp"
)

func TestReconcileWarpRuntimeReplacesConfigAfterStoppingOldRuntime(t *testing.T) {
	oldState := warpReconcileTestState(
		warpReconcileTestTunnel("awg0", "10.8.0.0/24"),
		warpReconcileTestTunnel("awg1", "10.9.0.0/24"),
	)
	nextState := warpReconcileTestState(warpReconcileTestTunnel("awg0", "10.8.0.0/24"))
	runtimeDir := t.TempDir()
	runtimePath := filepath.Join(runtimeDir, "warp0.conf")
	oldConfig := writeWarpReconcileTestConfig(t, runtimePath, oldState)

	var calls []string
	commands := warpRuntimeCommands{
		interfaceExists: func(string) bool { return true },
		down: func(string) error {
			calls = append(calls, "down")
			contents := readWarpReconcileTestConfig(t, runtimePath)
			if contents != oldConfig {
				t.Fatal("WARP runtime config was replaced before the old runtime was stopped")
			}
			if !strings.Contains(contents, "ip rule del from 10.9.0.0/24 lookup 200") {
				t.Fatal("old WARP runtime config does not clean up the removed route")
			}
			return nil
		},
		up: func(string) error {
			calls = append(calls, "up")
			contents := readWarpReconcileTestConfig(t, runtimePath)
			if strings.Contains(contents, "10.9.0.0/24") {
				t.Fatal("replacement WARP runtime config still contains the removed route")
			}
			return nil
		},
	}

	if err := reconcileWarpRuntime(nextState, runtimeDir, commands); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"down", "up"}) {
		t.Fatalf("runtime calls = %v, want [down up]", calls)
	}
	info, err := os.Stat(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0600); got != want {
		t.Fatalf("runtime config mode = %o, want %o", got, want)
	}
}

func TestReconcileWarpRuntimeWithoutExistingInterface(t *testing.T) {
	state := warpReconcileTestState(warpReconcileTestTunnel("awg0", "10.8.0.0/24"))
	runtimeDir := t.TempDir()
	var calls []string
	commands := warpRuntimeCommands{
		interfaceExists: func(string) bool { return false },
		down: func(string) error {
			calls = append(calls, "down")
			return nil
		},
		up: func(string) error {
			calls = append(calls, "up")
			return nil
		},
	}

	if err := reconcileWarpRuntime(state, runtimeDir, commands); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"up"}) {
		t.Fatalf("runtime calls = %v, want [up]", calls)
	}
}

func TestReconcileWarpRuntimeRecoversMissingConfigForLiveInterface(t *testing.T) {
	state := warpReconcileTestState(
		warpReconcileTestTunnel("awg0", "10.8.0.0/24"),
		warpReconcileTestTunnel("awg1", "10.9.0.0/24"),
	)
	state.Tunnels[1].EgressMode = config.EgressWAN
	runtimeDir := t.TempDir()
	runtimePath := filepath.Join(runtimeDir, "warp0.conf")
	var calls []string
	commands := warpRuntimeCommands{
		interfaceExists: func(string) bool { return true },
		verifyOwner: func(w config.Warp) error {
			calls = append(calls, "verify")
			if w.RuntimeInterface() != "warp0" || w.PrivateKey != state.Warp.PrivateKey {
				t.Fatal("ownership must be checked against the stored WARP interface and key")
			}
			return nil
		},
		down: func(configPath string) error {
			calls = append(calls, "down")
			if configPath == runtimePath || filepath.Base(configPath) != "warp0.conf" {
				t.Fatal("expected a temporary teardown config named after the interface")
			}
			contents := readWarpReconcileTestConfig(t, configPath)
			for _, want := range []string{
				"ip rule del from 10.8.0.0/24 lookup 200",
				"ip rule del from 10.9.0.0/24 lookup 200",
				"ip route del 10.9.0.0/24 dev awg1 table 200",
			} {
				if !strings.Contains(contents, want) {
					t.Fatalf("recovered teardown config is missing %q", want)
				}
			}
			if strings.Contains(contents, "PrivateKey") {
				t.Fatal("teardown config includes a private key")
			}
			return nil
		},
		up: func(string) error {
			calls = append(calls, "up")
			if contents := readWarpReconcileTestConfig(t, runtimePath); strings.Contains(contents, "10.9.0.0/24") {
				t.Fatal("replacement WARP config still routes the WAN subnet")
			}
			return nil
		},
	}
	if err := reconcileWarpRuntime(state, runtimeDir, commands); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"verify", "down", "up"}) {
		t.Fatalf("runtime calls = %v, want [verify down up]", calls)
	}
}

func TestReconcileWarpRuntimeLeavesUnverifiedInterfaceAlone(t *testing.T) {
	state := warpReconcileTestState(warpReconcileTestTunnel("awg0", "10.8.0.0/24"))
	runtimeDir := t.TempDir()
	ownerErr := errors.New("wrong public key")
	commands := warpRuntimeCommands{
		interfaceExists: func(string) bool { return true },
		verifyOwner:     func(config.Warp) error { return ownerErr },
	}
	if err := reconcileWarpRuntime(state, runtimeDir, commands); !errors.Is(err, ownerErr) {
		t.Fatalf("expected the ownership error, got %v", err)
	}
	if entries, err := os.ReadDir(runtimeDir); err != nil || len(entries) != 0 {
		t.Fatalf("unexpected runtime files after an ownership check failed (count=%d, error=%v)", len(entries), err)
	}
}

func TestReconcileWarpRuntimeRejectsUnsupportedCPSBeforeStoppingInterface(t *testing.T) {
	state := warpReconcileTestState(warpReconcileTestTunnel("awg0", "10.8.0.0/24"))
	runtimeDir := t.TempDir()
	runtimePath := filepath.Join(runtimeDir, "warp0.conf")
	oldConfig := writeWarpReconcileTestConfig(t, runtimePath, state)
	state.Warp.ProtocolParams = config.ProtocolParams{"I1": "<c>"}
	commands := warpRuntimeCommands{
		interfaceExists: func(string) bool {
			t.Fatal("runtime probed before validating WARP config")
			return true
		},
	}
	if err := reconcileWarpRuntime(state, runtimeDir, commands); err == nil || !strings.Contains(err.Error(), "I1") {
		t.Fatal("expected a WARP validation error for I1")
	}
	if got := readWarpReconcileTestConfig(t, runtimePath); got != oldConfig {
		t.Fatal("rejected CPS value replaced the working runtime config")
	}
}

func TestReconcileWarpRuntimeStopsLastRouteUsingExistingConfig(t *testing.T) {
	oldState := warpReconcileTestState(warpReconcileTestTunnel("awg0", "10.8.0.0/24"))
	runtimeDir := t.TempDir()
	runtimePath := filepath.Join(runtimeDir, "warp0.conf")
	oldConfig := writeWarpReconcileTestConfig(t, runtimePath, oldState)
	var calls []string
	commands := warpRuntimeCommands{
		interfaceExists: func(string) bool { return true },
		down: func(string) error {
			calls = append(calls, "down")
			if got := readWarpReconcileTestConfig(t, runtimePath); got != oldConfig {
				t.Fatal("last WARP route was not stopped with its existing runtime config")
			}
			return nil
		},
		up: func(string) error {
			calls = append(calls, "up")
			return nil
		},
	}

	if err := reconcileWarpRuntime(warpReconcileTestState(), runtimeDir, commands); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"down"}) {
		t.Fatalf("runtime calls = %v, want [down]", calls)
	}
}

func TestReconcileWarpRuntimeDownFailurePreservesExistingConfig(t *testing.T) {
	oldState := warpReconcileTestState(
		warpReconcileTestTunnel("awg0", "10.8.0.0/24"),
		warpReconcileTestTunnel("awg1", "10.9.0.0/24"),
	)
	nextState := warpReconcileTestState(warpReconcileTestTunnel("awg0", "10.8.0.0/24"))
	runtimeDir := t.TempDir()
	runtimePath := filepath.Join(runtimeDir, "warp0.conf")
	oldConfig := writeWarpReconcileTestConfig(t, runtimePath, oldState)
	downErr := errors.New("forced down failure")
	upCalled := false
	commands := warpRuntimeCommands{
		interfaceExists: func(string) bool { return true },
		down:            func(string) error { return downErr },
		up: func(string) error {
			upCalled = true
			return nil
		},
	}

	err := reconcileWarpRuntime(nextState, runtimeDir, commands)
	if !errors.Is(err, downErr) {
		t.Fatalf("error = %v, want forced down failure", err)
	}
	if upCalled {
		t.Fatal("new WARP runtime started after old runtime failed to stop")
	}
	if got := readWarpReconcileTestConfig(t, runtimePath); got != oldConfig {
		t.Fatal("WARP runtime config changed after down failure")
	}
	matches, err := filepath.Glob(filepath.Join(runtimeDir, ".warp-runtime-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("staged runtime configs were not removed: %v", matches)
	}
}

func TestReconcileWarpRuntimeStagesConfigBeforeStoppingRuntime(t *testing.T) {
	state := warpReconcileTestState(warpReconcileTestTunnel("awg0", "10.8.0.0/24"))
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(runtimeDir, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	commandCalled := false
	commands := warpRuntimeCommands{
		interfaceExists: func(string) bool {
			commandCalled = true
			return true
		},
		down: func(string) error {
			commandCalled = true
			return nil
		},
		up: func(string) error {
			commandCalled = true
			return nil
		},
	}

	if err := reconcileWarpRuntime(state, runtimeDir, commands); err == nil {
		t.Fatal("expected runtime config staging failure")
	}
	if commandCalled {
		t.Fatal("WARP runtime was touched before replacement config staging succeeded")
	}
}

func TestReconcileWarpRuntimeCanRestoreAfterUpFailure(t *testing.T) {
	oldState := warpReconcileTestState(
		warpReconcileTestTunnel("awg0", "10.8.0.0/24"),
		warpReconcileTestTunnel("awg1", "10.9.0.0/24"),
	)
	nextState := warpReconcileTestState(warpReconcileTestTunnel("awg0", "10.8.0.0/24"))
	runtimeDir := t.TempDir()
	runtimePath := filepath.Join(runtimeDir, "warp0.conf")
	oldConfig := writeWarpReconcileTestConfig(t, runtimePath, oldState)
	upErr := errors.New("forced up failure")

	err := reconcileWarpRuntime(nextState, runtimeDir, warpRuntimeCommands{
		interfaceExists: func(string) bool { return true },
		down:            func(string) error { return nil },
		up:              func(string) error { return upErr },
	})
	if !errors.Is(err, upErr) {
		t.Fatalf("error = %v, want forced up failure", err)
	}
	if got := readWarpReconcileTestConfig(t, runtimePath); got == oldConfig || strings.Contains(got, "10.9.0.0/24") {
		t.Fatal("failed desired WARP config was not persisted for status and rollback handling")
	}

	upCalled := false
	if err := reconcileWarpRuntime(oldState, runtimeDir, warpRuntimeCommands{
		interfaceExists: func(string) bool { return false },
		down:            func(string) error { return errors.New("unexpected down") },
		up: func(string) error {
			upCalled = true
			if got := readWarpReconcileTestConfig(t, runtimePath); got != oldConfig {
				t.Fatal("rollback did not restore the previous WARP runtime config")
			}
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if !upCalled {
		t.Fatal("rollback did not restart the previous WARP runtime")
	}
}

func TestRenderAllRecordsFailedWARPReplacement(t *testing.T) {
	svc, _ := runtimeTransactionService(t)
	recorder := runtimeRecorder{}
	svc.runtimeOps = recorder.operations()
	runtimeDir := t.TempDir()
	stopped := false
	svc.runtimeOps.reconcileWarp = func(state config.State) error {
		return reconcileWarpRuntime(state, runtimeDir, warpRuntimeCommands{
			interfaceExists: func(string) bool { return true },
			verifyOwner:     func(config.Warp) error { return nil },
			down: func(string) error {
				stopped = true
				return nil
			},
			up: func(string) error { return errors.New("forced userspace startup failure") },
		})
	}
	if err := svc.RenderAll(); err != nil {
		t.Fatal(err)
	}
	state, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !stopped || state.Warp.LastApplyError != "start WARP runtime: forced userspace startup failure" {
		t.Fatal("startup did not persist the failed WARP replacement for the upgrade readiness check")
	}
}

func warpReconcileTestState(tunnels ...config.Tunnel) config.State {
	return config.State{
		Warp: config.Warp{
			InterfaceName:       "warp0",
			PrivateKey:          "test-private-key",
			PeerPublicKey:       "test-public-key",
			Endpoint:            "162.159.192.1:2408",
			AddressV4:           "172.16.0.2/32",
			MTU:                 1280,
			PersistentKeepalive: 25,
		},
		Tunnels: tunnels,
	}
}

func warpReconcileTestTunnel(interfaceName, subnet string) config.Tunnel {
	return config.Tunnel{
		InterfaceName: interfaceName,
		IPv4Subnet:    subnet,
		EgressMode:    config.EgressWarp,
		Enabled:       true,
	}
}

func writeWarpReconcileTestConfig(t *testing.T, path string, state config.State) string {
	t.Helper()
	contents, err := warp.RenderConfig(state.Warp, warp.RoutesForState(state))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return contents
}

func readWarpReconcileTestConfig(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
