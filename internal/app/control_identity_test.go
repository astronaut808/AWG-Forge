package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/storage"
)

func TestPrepareControlIdentityIsExplicitIdempotentAndDisabled(t *testing.T) {
	cfg := controllerTestConfig(t)
	svc := newFastControllerTestService(cfg)
	state, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	if state.Controller != nil {
		t.Fatal("ordinary initialization prepared controller")
	}
	if _, err := os.Lstat(filepath.Join(cfg.ConfigDir, "control")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ordinary initialization created control files: %v", err)
	}
	if _, err := svc.ActivateController(context.Background(), controllerTestActivationRequest(t, time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(cfg.ConfigDir, "control")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("controller activation created control files: %v", err)
	}
	before, err := storage.New(cfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	request := ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "Control.Example.com", Port: 8443}
	prepared, err := svc.PrepareControlIdentity(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Enabled || prepared.Advertised != "control.example.com" || prepared.CAPin == "" {
		t.Fatalf("invalid public preparation result: %+v", prepared)
	}
	after, err := storage.New(cfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if after.Controller == nil || after.Controller.Control == nil || after.Controller.Control.Enabled || !reflect.DeepEqual(before.Tunnels, after.Tunnels) {
		t.Fatal("preparation changed tunnel state or enabled control")
	}
	request.Advertised = "control.example.com"
	repeated, err := newFastControllerTestService(cfg).PrepareControlIdentity(context.Background(), request)
	if err != nil || !reflect.DeepEqual(repeated, prepared) {
		t.Fatalf("repeat preparation = %+v, %v", repeated, err)
	}
	request.Port++
	if _, err := svc.PrepareControlIdentity(context.Background(), request); err == nil {
		t.Fatal("different endpoint replaced committed identity")
	}
	if _, err := os.Lstat(storage.New(cfg.ConfigDir).ControlIdentityJournalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preparation journal remains: %v", err)
	}
	for _, rel := range []string{"control", "control/ca", "control/server", "control/ca/" + prepared.CAGeneration, "control/server/" + prepared.ServerGeneration} {
		assertControlMode(t, filepath.Join(cfg.ConfigDir, rel), 0700)
	}
	paths, _ := storage.ControlIdentityRelativePaths(prepared.CAGeneration, prepared.ServerGeneration)
	for _, rel := range paths {
		assertControlMode(t, filepath.Join(cfg.ConfigDir, rel), 0600)
	}
}

func TestPrepareControlIdentityRecoversPrecommitFailures(t *testing.T) {
	for _, point := range []string{"journal", "directory:config", "directory:control", "directory:ca", "directory:server", "generation:ca", "file:ca:key.pem", "file:ca:cert.pem", "generation:server", "file:server:key.pem", "file:server:cert.pem", "before-state"} {
		t.Run(point, func(t *testing.T) {
			cfg := controllerTestConfig(t)
			svc := newFastControllerTestService(cfg)
			if _, err := svc.Init(); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ActivateController(context.Background(), controllerTestActivationRequest(t, time.Now().UTC())); err != nil {
				t.Fatal(err)
			}
			svc.controlIdentityStep = func(step string) error {
				if step == point {
					return errors.New("injected failure")
				}
				return nil
			}
			if _, err := svc.PrepareControlIdentity(context.Background(), ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "control.example.com", Port: 8443}); err == nil {
				t.Fatal("injected failure did not stop preparation")
			}
			state, err := storage.New(cfg.ConfigDir).Load()
			if err != nil || state.Controller.Control != nil {
				t.Fatalf("uncommitted identity became active: %v", err)
			}
			if _, err := newFastControllerTestService(cfg).Init(); err != nil {
				t.Fatalf("browser and tunnels unavailable after optional control failure: %v", err)
			}
		})
	}
}

func TestCorruptPreparedIdentityDoesNotBlockLocalInitOrRegenerate(t *testing.T) {
	cfg := controllerTestConfig(t)
	svc := newFastControllerTestService(cfg)
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ActivateController(context.Background(), controllerTestActivationRequest(t, time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	request := ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "control.example.com", Port: 8443}
	prepared, err := svc.PrepareControlIdentity(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.ConfigDir, "control", "server", prepared.ServerGeneration, "cert.pem")
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newFastControllerTestService(cfg).Init(); err != nil {
		t.Fatalf("local runtime blocked by optional control identity: %v", err)
	}
	if _, err := svc.PrepareControlIdentity(context.Background(), request); err == nil {
		t.Fatal("corrupt committed identity was silently replaced")
	}
	state, err := storage.New(cfg.ConfigDir).Load()
	if err != nil || state.Controller.Control.CAGeneration != prepared.CAGeneration {
		t.Fatal("committed generation changed after corruption")
	}
}

func TestPrepareControlIdentityStateCommitAndJournalCleanupFailures(t *testing.T) {
	for _, point := range []string{"state-save", "after-state", "journal-cleanup"} {
		t.Run(point, func(t *testing.T) {
			cfg := controllerTestConfig(t)
			svc := newFastControllerTestService(cfg)
			if _, err := svc.Init(); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ActivateController(context.Background(), controllerTestActivationRequest(t, time.Now().UTC())); err != nil {
				t.Fatal(err)
			}
			store := storage.New(cfg.ConfigDir)
			if point == "state-save" {
				svc.saveState = func(config.State) error { return errors.New("injected state save failure") }
			} else {
				svc.controlIdentityStep = func(step string) error {
					if step == "after-state" && point == "after-state" {
						return errors.New("injected postcommit failure")
					}
					if step == "after-state" && point == "journal-cleanup" {
						return os.Chmod(store.ControlIdentityJournalPath(), 0644)
					}
					return nil
				}
			}
			request := ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "control.example.com", Port: 8443}
			if _, err := svc.PrepareControlIdentity(context.Background(), request); err == nil {
				t.Fatal("injected failure did not surface")
			}
			state, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if point == "state-save" && state.Controller.Control != nil {
				t.Fatal("failed state save activated uncommitted identity")
			}
			if point != "state-save" && state.Controller.Control == nil {
				t.Fatal("postcommit failure lost committed identity")
			}
			if point == "journal-cleanup" {
				if _, err := svc.PrepareControlIdentity(context.Background(), request); err == nil {
					t.Fatal("unsafe journal allowed a second preparation")
				}
				if err := os.Chmod(store.ControlIdentityJournalPath(), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := newFastControllerTestService(cfg).Init(); err != nil {
				t.Fatalf("local runtime blocked after optional control failure: %v", err)
			}
			repeated, err := newFastControllerTestService(cfg).PrepareControlIdentity(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if point != "state-save" && repeated.CAGeneration != state.Controller.Control.CAGeneration {
				t.Fatal("retry regenerated CA after committed identity")
			}
		})
	}
}

func TestPrepareControlIdentityRejectsPendingStateTransactions(t *testing.T) {
	for _, pending := range []string{"desired", "restore"} {
		t.Run(pending, func(t *testing.T) {
			cfg := controllerTestConfig(t)
			svc := newFastControllerTestService(cfg)
			if _, err := svc.Init(); err != nil {
				t.Fatal(err)
			}
			activated, err := svc.ActivateController(context.Background(), controllerTestActivationRequest(t, time.Now().UTC()))
			if err != nil {
				t.Fatal(err)
			}
			store := storage.New(cfg.ConfigDir)
			if pending == "desired" {
				if err := store.SavePendingDesiredStateCommit(storage.PendingDesiredStateCommit{OperationID: "pending"}); err != nil {
					t.Fatal(err)
				}
			} else if err := store.BeginRestorePending(activated.ControllerID); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.PrepareControlIdentity(context.Background(), ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "control.example.com", Port: 8443}); err == nil {
				t.Fatal("pending state transaction allowed identity preparation")
			}
			if _, err := os.Lstat(filepath.Join(cfg.ConfigDir, "control")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("identity files created during pending transaction: %v", err)
			}
		})
	}
}

func assertControlMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
	}
}
