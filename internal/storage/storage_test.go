package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astronaut808/awg-forge/internal/config"
)

func TestDeleteRenderedTunnelRejectsUnsafeInterfaceName(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(outside, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}

	store := New(filepath.Join(root, "data"))
	err := store.DeleteRenderedTunnel("../outside")
	if err == nil {
		t.Fatal("expected unsafe interface name to be rejected")
	}
	if !strings.Contains(err.Error(), "invalid tunnel path component") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("outside marker was touched: %v", err)
	}
}

func TestWriteRenderedTunnelRejectsUnsafeClientID(t *testing.T) {
	store := New(t.TempDir())
	err := store.WriteRenderedTunnel(config.Tunnel{InterfaceName: "awg0"}, "server", map[string]string{
		"../client": "client",
	})
	if err == nil {
		t.Fatal("expected unsafe client id to be rejected")
	}
	if !strings.Contains(err.Error(), "invalid client path component") {
		t.Fatalf("unexpected error: %v", err)
	}
}
