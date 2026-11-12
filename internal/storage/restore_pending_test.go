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

func TestRestorePendingDurabilityFaultBoundaries(t *testing.T) {
	for _, point := range []string{"before-create", "after-create", "after-write", "after-file-sync", "after-directory-sync"} {
		t.Run(point, func(t *testing.T) {
			store := New(t.TempDir())
			fault := errors.New("injected marker durability failure")
			err := store.beginRestorePending("controller-test-id", func(step string) error {
				if step == point {
					return fault
				}
				return nil
			})
			if !errors.Is(err, fault) {
				t.Fatalf("marker fault was not reached: %v", err)
			}
			if point == "before-create" {
				if err := store.CheckRestorePending(); err != nil {
					t.Fatal("pre-create fault published a marker", err)
				}
			} else if !errors.Is(store.CheckRestorePending(), ErrRestorePending) {
				t.Fatal("partial marker did not fence startup")
			}
		})
	}
}

func TestRestorePendingClearFaultDistinguishesUnlinkedMarker(t *testing.T) {
	for _, point := range []string{"before-unlink", "after-unlink", "after-clear-directory-sync"} {
		t.Run(point, func(t *testing.T) {
			store := New(t.TempDir())
			if err := store.BeginRestorePending("controller-test-id"); err != nil {
				t.Fatal(err)
			}
			fault := errors.New("injected clear durability failure")
			err := store.clearRestorePending(func(step string) error {
				if step == point {
					return fault
				}
				return nil
			})
			if !errors.Is(err, fault) {
				t.Fatalf("clear fault was not reached: %v", err)
			}
			if point == "before-unlink" {
				if !errors.Is(store.CheckRestorePending(), ErrRestorePending) {
					t.Fatal("pre-unlink failure lost marker")
				}
			} else if err := store.CheckRestorePending(); err != nil {
				t.Fatal("post-unlink failure cannot promise a retained marker", err)
			}
		})
	}
}
