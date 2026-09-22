package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const (
	StateLockFileName         = ".state.lock"
	StateMutationLockFileName = ".state.mutation.lock"
)

// ErrStateDirectoryInUse indicates that another process owns the state lock.
var ErrStateDirectoryInUse = errors.New("state directory is in use")

// StateLock prevents a running server and an offline restore from accessing
// the state directory concurrently. The lock file is local runtime metadata
// and is intentionally excluded from backups.
type StateLock struct {
	file *os.File
}

// AcquireStateLock acquires the state directory exclusively without waiting.
func AcquireStateLock(dir string) (*StateLock, error) {
	return acquireStateFileLock(dir, StateLockFileName, unix.LOCK_EX|unix.LOCK_NB)
}

// AcquireStateMutationLock serializes complete state mutation transactions
// across the server and concurrently invoked CLI processes.
func AcquireStateMutationLock(dir string) (*StateLock, error) {
	return acquireStateFileLock(dir, StateMutationLockFileName, unix.LOCK_EX)
}

func acquireStateFileLock(dir, name string, operation int) (*StateLock, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), operation); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrStateDirectoryInUse
		}
		return nil, fmt.Errorf("lock state directory: %w", err)
	}
	return &StateLock{file: file}, nil
}

func (l *StateLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	return errors.Join(unlockErr, closeErr)
}
