package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNodeIdentityRoundTripRequiresPrivateFiles(t *testing.T) {
	dir, err := os.MkdirTemp(".", "node-identity-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	store := New(dir)
	generation := "0123456789abcdef0123456789abcdef"
	material := NodeIdentityMaterial{CACert: []byte("ca"), Certificate: []byte("cert"), PrivateKey: []byte("key")}
	if err := store.SaveNodeIdentity(generation, material); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadNodeIdentity(generation)
	if err != nil || string(loaded.PrivateKey) != "key" {
		t.Fatalf("load = %#v, %v", loaded, err)
	}
	if err := os.Chmod(filepath.Join(dir, "node", generation, "key.pem"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadNodeIdentity(generation); err == nil {
		t.Fatal("permissive key was accepted")
	}
	if _, err := NodeIdentityRelativePaths("../bad"); err == nil {
		t.Fatal("unsafe generation accepted")
	}
	if err := store.SaveNodeIdentity(generation, material); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generation reuse = %v", err)
	}
}
