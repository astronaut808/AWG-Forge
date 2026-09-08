package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
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
