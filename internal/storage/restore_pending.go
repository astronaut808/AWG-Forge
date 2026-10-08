package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const RestorePendingFileName = ".restore-pending.json"

var ErrRestorePending = errors.New("controller restore is pending offline recovery")

// RestorePending is secret-free evidence that a controller restore may have
// changed only part of the state directory. It is never included in a backup.
type RestorePending struct {
	ControllerID string    `json:"controller_id"`
	StartedAt    time.Time `json:"started_at"`
}

func (s Store) RestorePendingPath() string {
	return filepath.Join(s.dir, RestorePendingFileName)
}

// CheckRestorePending refuses startup for any existing marker, including a
// malformed or symlinked one. Offline recovery must inspect the directory.
func (s Store) CheckRestorePending() error {
	_, err := os.Lstat(s.RestorePendingPath())
	if err == nil {
		return ErrRestorePending
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("inspect controller restore marker: %w", err)
}

// BeginRestorePending durably records the fail-closed gate before any controller
// file is replaced. The caller must hold the exclusive state directory lock.
func (s Store) BeginRestorePending(controllerID string) error {
	return s.beginRestorePending(controllerID, nil)
}

func (s Store) beginRestorePending(controllerID string, step func(string) error) error {
	if controllerID == "" {
		return errors.New("controller ID is required for restore marker")
	}
	info, err := os.Lstat(s.dir)
	if err != nil {
		return fmt.Errorf("inspect state directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("state directory must be a private non-symlink directory")
	}
	if info.Mode().Perm() != 0700 {
		if err := os.Chmod(s.dir, 0700); err != nil {
			return fmt.Errorf("protect state directory: %w", err)
		}
	}
	if err := restorePendingStep(step, "before-create"); err != nil {
		return err
	}
	marker, err := os.OpenFile(s.RestorePendingPath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create controller restore marker: %w", err)
	}
	defer func() { _ = marker.Close() }()
	if err := restorePendingStep(step, "after-create"); err != nil {
		return err
	}
	body, err := json.Marshal(RestorePending{ControllerID: controllerID, StartedAt: time.Now().UTC()})
	if err != nil {
		_ = marker.Close()
		return err
	}
	body = append(body, '\n')
	if _, err := marker.Write(body); err != nil {
		_ = marker.Close()
		return fmt.Errorf("write controller restore marker: %w", err)
	}
	if err := restorePendingStep(step, "after-write"); err != nil {
		return err
	}
	if err := marker.Sync(); err != nil {
		_ = marker.Close()
		return fmt.Errorf("sync controller restore marker: %w", err)
	}
	if err := restorePendingStep(step, "after-file-sync"); err != nil {
		return err
	}
	if err := marker.Close(); err != nil {
		return fmt.Errorf("close controller restore marker: %w", err)
	}
	if err := syncRestoreDirectory(s.dir); err != nil {
		return err
	}
	return restorePendingStep(step, "after-directory-sync")
}

// ClearRestorePending is called only after the restored identity and database
// have been verified and browser and node credentials durably invalidated.
func (s Store) ClearRestorePending() error {
	return s.clearRestorePending(nil)
}

func (s Store) clearRestorePending(step func(string) error) error {
	info, err := os.Lstat(s.RestorePendingPath())
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return errors.New("invalid controller restore marker")
	}
	if err := restorePendingStep(step, "before-unlink"); err != nil {
		return err
	}
	if err := os.Remove(s.RestorePendingPath()); err != nil {
		return err
	}
	if err := restorePendingStep(step, "after-unlink"); err != nil {
		return err
	}
	if err := syncRestoreDirectory(s.dir); err != nil {
		return err
	}
	return restorePendingStep(step, "after-clear-directory-sync")
}

func restorePendingStep(step func(string) error, point string) error {
	if step != nil {
		return step(point)
	}
	return nil
}

func syncRestoreDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	return errors.Join(syncErr, dir.Close())
}
