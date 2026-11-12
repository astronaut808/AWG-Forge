package nodeagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/google/uuid"
)

func TestCappedOutputRejectsOversizeWithoutRetainingIt(t *testing.T) {
	var output cappedOutput
	if _, err := output.Write(make([]byte, maxRuntimeObservationBytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte("x")); err == nil {
		t.Fatal("oversize runtime output accepted")
	}
	if output.Len() != maxRuntimeObservationBytes {
		t.Fatalf("retained output = %d", output.Len())
	}
}

func TestObservationCommandBoundsAndCancelsSlowRuntime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "awg")
	writeAWG := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeAWG("sleep 10")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := observationCommand(ctx, "show", "awg0", "transfer"); err == nil {
		t.Fatal("cancelled command accepted")
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("command shutdown took %s", elapsed)
	}
}

func TestCollectSnapshotExportsOnlyDesiredAllowlistWhenRuntimeMissing(t *testing.T) {
	cfg := config.Config{ConfigDir: t.TempDir(), ServerHost: "vpn.example.test", ExternalInterface: "eth0", DatabaseMode: "sqlite"}
	initial := app.New(cfg)
	state, err := initial.Init()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := initial.AddClient("phone"); err != nil {
		t.Fatal(err)
	}
	state, err = initial.State()
	if err != nil {
		t.Fatal(err)
	}
	state.Mode = config.ModeNode
	state.ManagedNode = &config.ManagedNodeState{NodeID: uuid.NewString(), ControllerID: uuid.NewString(), StateEpoch: uuid.NewString(), BindingEpoch: 1, BootSequence: 1, DesiredGeneration: 2}
	state.NodeConnection = &config.NodeConnectionState{ControllerURL: "https://control.example.test", CAPin: strings.Repeat("a", 64), CredentialGeneration: strings.Repeat("b", 32)}
	state.Tunnels[0].ServerPrivateKey = "private-canary"
	state.Tunnels[0].Clients[0].PrivateKey = "client-private-canary"
	state.Tunnels[0].Clients[0].PresharedKey = "psk-canary"
	if err := storage.New(cfg.ConfigDir).Save(state); err != nil {
		t.Fatal(err)
	}
	// PATH contains no awg, so collection must represent missing runtime rather than fabricate health.
	t.Setenv("PATH", t.TempDir())
	snapshot, err := collectSnapshot(context.Background(), app.New(cfg), cfg, uuid.NewString(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Observations.Tunnels[0].Known || snapshot.Observations.Tunnels[0].Up || snapshot.Observations.Doctor.RuntimeUnknown != 1 {
		t.Fatalf("missing runtime reported as healthy: %#v", snapshot.Observations)
	}
	encoded := string(mustSnapshotJSON(t, snapshot))
	for _, canary := range []string{"private-canary", "client-private-canary", "psk-canary"} {
		if strings.Contains(encoded, canary) {
			t.Fatalf("snapshot leaked %q", canary)
		}
	}
}

func mustSnapshotJSON(t *testing.T, snapshot any) []byte {
	t.Helper()
	b, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
