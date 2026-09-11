package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStateLockRejectsConcurrentOwnerAndCanBeReacquired(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireStateLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireStateLock(dir); !errors.Is(err, ErrStateDirectoryInUse) {
		t.Fatalf("second lock error = %v, want %v", err, ErrStateDirectoryInUse)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := AcquireStateLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Error(err)
		}
	})
	info, err := os.Stat(filepath.Join(dir, StateLockFileName))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("lock mode = %04o, want 0600", got)
	}
}

func TestStateMutationLockSerializesOwners(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireStateMutationLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan *StateLock, 1)
	errs := make(chan error, 1)
	go func() {
		second, err := AcquireStateMutationLock(dir)
		if err != nil {
			errs <- err
			return
		}
		acquired <- second
	}()
	select {
	case second := <-acquired:
		_ = second.Close()
		t.Fatal("second mutation lock acquired before first was released")
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case second := <-acquired:
		if err := second.Close(); err != nil {
			t.Fatal(err)
		}
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("second mutation lock did not acquire after release")
	}
	info, err := os.Stat(filepath.Join(dir, StateMutationLockFileName))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("mutation lock mode = %04o, want 0600", got)
	}
}
