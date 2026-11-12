package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
)

func TestClientCRUDProcessCrashRecovery(t *testing.T) {
	if dir := os.Getenv("AWG_TEST_CRUD_PROCESS_DIR"); dir != "" {
		raw, err := os.ReadFile(filepath.Join(dir, "fixture.json"))
		if err != nil {
			os.Exit(81)
		}
		var fixture struct {
			Config    config.Config
			Operation controlapi.OperationEnvelope
		}
		if json.Unmarshal(raw, &fixture) != nil {
			os.Exit(82)
		}
		svc := New(fixture.Config)
		if _, err := svc.StartManagedNodeBoot(); err != nil {
			os.Exit(83)
		}
		point := os.Getenv("AWG_TEST_CRUD_PROCESS_POINT")
		if point == "accepted" {
			st, _ := svc.store.Load()
			m := st.ManagedNode
			if svc.store.SaveClientOperations([]ClientOperationOutcome{{ControllerID: m.ControllerID, NodeID: m.NodeID, Operation: fixture.Operation, Status: "accepted"}}) != nil {
				os.Exit(84)
			}
			os.Exit(79) // injected accepted checkpoint, real process death without defers
		}
		svc.cfg.ApplyConfig = true
		svc.runtimeOps.applyTunnel = func(config.Tunnel) error {
			if point == "runtime" {
				os.Exit(79)
			}
			return nil
		}
		if out, err := svc.ExecuteClientOperation(context.Background(), fixture.Operation); err != nil || out.Status != "succeeded" {
			os.Exit(85)
		}
		os.Exit(79) // exact durable commit followed by real process death
	}
	for _, kind := range []string{"client.create", "client.delete"} {
		for _, point := range []string{"accepted", "runtime", "committed"} {
			t.Run(kind+"/"+point, func(t *testing.T) {
				svc, state, tunnel, c := managedClientOperationService(t)
				id := c.ID
				if kind == "client.create" {
					id = "4023456789abcdef"
				}
				op := clientCRUDOperation(state, tunnel.ID, id, kind)
				fixture := struct {
					Config    config.Config
					Operation controlapi.OperationEnvelope
				}{svc.cfg, op}
				raw, _ := json.Marshal(fixture)
				if err := os.WriteFile(filepath.Join(svc.cfg.ConfigDir, "fixture.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				// Run only a private copy of this test binary under a fixed name.
				source, err := os.Open(executable)
				if err != nil {
					t.Fatal(err)
				}
				helperDir := t.TempDir()
				target, err := os.OpenFile(filepath.Join(helperDir, "client-crud-recovery-helper"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
				if err != nil {
					_ = source.Close()
					t.Fatal(err)
				}
				_, copyErr := io.Copy(target, source)
				if err := errors.Join(copyErr, target.Close(), source.Close()); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				child := exec.CommandContext(ctx, "./client-crud-recovery-helper", "-test.run=^TestClientCRUDProcessCrashRecovery$")
				child.Dir = helperDir
				child.Env = append(os.Environ(), "AWG_TEST_CRUD_PROCESS_DIR="+svc.cfg.ConfigDir, "AWG_TEST_CRUD_PROCESS_POINT="+point)
				var exit *exec.ExitError
				if err := child.Run(); !errors.As(err, &exit) || exit.ExitCode() != 79 {
					t.Fatalf("child missed %s crash point: %v", point, err)
				}
				recovered := New(svc.cfg)
				if _, err := recovered.StartManagedNodeBoot(); err != nil {
					t.Fatal(err)
				}
				before, _ := recovered.store.Load()
				out, err := recovered.ExecuteClientOperation(context.Background(), op)
				if err != nil {
					t.Fatal(err)
				}
				if point == "committed" {
					if !out.receipt().MatchesClientSuccess(op) {
						t.Fatal("committed receipt not recovered")
					}
					if before.ManagedNode.DesiredGeneration != 1 {
						t.Fatal("commit generation lost")
					}
				} else if out.Code != "interrupted_rolled_back" || before.ManagedNode.DesiredGeneration != 0 {
					t.Fatalf("precommit process recovery: %s", out.Code)
				}
				after, _ := recovered.store.Load()
				if !reflect.DeepEqual(before.Tunnels, after.Tunnels) || before.ManagedNode.DesiredGeneration != after.ManagedNode.DesiredGeneration {
					t.Fatal("recovery blindly executed mutation")
				}
			})
		}
	}
}
