package storage

import (
	"errors"
	"os"
	"testing"
)

func TestRestorePendingBlocksUntilCleared(t *testing.T) {
	store := New(t.TempDir())
	if err := store.CheckRestorePending(); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginRestorePending("controller-test-id"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(store.CheckRestorePending(), ErrRestorePending) {
		t.Fatal("existing restore marker did not block startup")
	}
	if err := store.BeginRestorePending("controller-test-id"); err == nil {
		t.Fatal("second restore replaced the pending marker")
	}
	info, err := os.Lstat(store.RestorePendingPath())
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatalf("restore marker mode = %v", info.Mode())
	}
	if err := store.ClearRestorePending(); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckRestorePending(); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedRestorePendingBlocksStartup(t *testing.T) {
	store := New(t.TempDir())
	if err := os.WriteFile(store.RestorePendingPath(), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(store.CheckRestorePending(), ErrRestorePending) {
		t.Fatal("malformed restore marker did not block startup")
	}
}
