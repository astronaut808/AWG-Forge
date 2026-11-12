package app

import (
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/storage"
)

func TestServerNameMetadataPreservesManagedConfiguration(t *testing.T) {
	t.Setenv("SERVER_NAME", "ProcessName")
	service, before, _, _ := managedMutationTestService(t)
	service.runtimeOps.applyTunnel = func(_ config.Tunnel) error {
		t.Fatal("renaming must not apply tunnel runtime")
		return nil
	}
	check := func(want string) {
		t.Helper()
		after, err := service.State()
		if err != nil {
			t.Fatal(err)
		}
		if after.ServerName != want {
			t.Fatalf("name = %q, want %q", after.ServerName, want)
		}
		if !reflect.DeepEqual(before.ManagedNode, after.ManagedNode) || !reflect.DeepEqual(before.Tunnels, after.Tunnels) {
			t.Fatal("renaming changed managed identity, generation or client configuration")
		}
		if os.Getenv("SERVER_NAME") != "ProcessName" {
			t.Fatal("renaming changed process startup environment")
		}
	}
	for _, name := range []string{"UIName", "", "UIName"} {
		if _, err := service.UpdateServerName(name); err != nil {
			t.Fatal(err)
		}
		check(name)
	}
	for _, env := range []string{"StartupName", "", "ChangedName"} {
		cfg := service.cfg
		cfg.ServerName = env
		service = New(cfg)
		want := env
		if want == "" {
			want = "StartupName"
		}
		check(want)
	}
}

func TestServerNameUpdateHonorsPendingRestore(t *testing.T) {
	service := New(testServiceConfig(t))
	if _, err := service.Init(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(service.store.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.store.BeginRestorePending("11111111-1111-4111-8111-111111111111"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateServerName("UnsafeDuringRestore"); !errors.Is(err, storage.ErrRestorePending) {
		t.Fatalf("rename during restore = %v", err)
	}
	after, err := os.ReadFile(service.store.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("rename changed state during pending restore")
	}
}
