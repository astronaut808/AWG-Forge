package app

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/astronaut808/awg-forge/internal/config"
)

func TestServerNameMetadataPreservesStandaloneConfiguration(t *testing.T) {
	t.Setenv("SERVER_NAME", "ProcessName")
	cfg := testServiceConfig(t)
	cfg.ServerName = "StartupName"
	service := New(cfg)
	before, err := service.Init()
	if err != nil {
		t.Fatal(err)
	}
	preventApply := func() {
		service.runtimeOps.applyTunnel = func(config.Tunnel) error {
			t.Fatal("renaming must not apply tunnel runtime")
			return nil
		}
	}
	preventApply()
	renderedPath := filepath.Join(service.store.TunnelDir(before.Tunnels[0].InterfaceName), "server.conf")
	rendered, err := os.ReadFile(renderedPath)
	if err != nil {
		t.Fatal(err)
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
		if !reflect.DeepEqual(before.Tunnels, after.Tunnels) {
			t.Fatal("renaming changed tunnel or client configuration")
		}
		current, err := os.ReadFile(renderedPath)
		if err != nil || !bytes.Equal(rendered, current) {
			t.Fatal("renaming changed rendered server configuration")
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
		service = New(cfg)
		preventApply()
		check(name)
	}
	for _, env := range []string{"ExternalName", "", "ChangedName"} {
		cfg := service.cfg
		cfg.ServerName = env
		service = New(cfg)
		preventApply()
		want := env
		if want == "" {
			want = "ExternalName"
		}
		check(want)
	}
}
