package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/storage"
)

func cleanArchivePath(archivePath string) (string, error) {
	archivePath = strings.ReplaceAll(strings.TrimSpace(archivePath), "\\", "/")
	if archivePath == "" || strings.HasPrefix(archivePath, "/") || strings.Contains(archivePath, "\x00") {
		return "", errors.New("invalid archive path")
	}
	for _, part := range strings.Split(archivePath, "/") {
		if part == ".." {
			return "", errors.New("invalid archive path")
		}
	}
	clean := path.Clean(archivePath)
	if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", errors.New("invalid archive path")
	}
	return clean, nil
}

func safeRestorePath(root, archivePath string) (string, error) {
	clean, err := cleanArchivePath(archivePath)
	if err != nil {
		return "", err
	}
	if clean == storage.StateLockFileName || clean == storage.StateMutationLockFileName {
		return "", errors.New("backup contains reserved state lock path")
	}
	dst := filepath.Join(root, filepath.FromSlash(clean))
	rel, err := filepath.Rel(root, dst)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("invalid archive path")
	}
	return dst, nil
}

func preRestoreBackupFile(cfg config.Config, state config.State, password string) (restoreFile, error) {
	archive, err := createFromState(cfg, state, password, Options{})
	if err != nil {
		return restoreFile{}, err
	}
	path := "backups/pre-restore-" + time.Now().UTC().Format("20060102-150405") + ".afbackup"
	return restoreFile{Path: path, Data: archive.Data}, nil
}

func loadRestoreTargetState(root string) (config.State, bool, error) {
	state, err := storage.New(root).Load()
	if errors.Is(err, os.ErrNotExist) {
		return config.State{}, false, nil
	}
	if err != nil {
		return config.State{}, false, err
	}
	return state, true, nil
}

func replaceRestoredState(files []restoreFile, state config.State) ([]restoreFile, error) {
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, err
	}
	result := append([]restoreFile(nil), files...)
	for i := range result {
		if result[i].Path == "state.json" {
			result[i].Data = b
			return result, nil
		}
	}
	return nil, errors.New("validated backup state.json is missing")
}

func restoreFiles(root string, files []restoreFile) error {
	return restoreFilesWithRename(root, files, os.Rename)
}

func restoreFilesWithRename(root string, files []restoreFile, rename func(string, string) error) error {
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	if err := os.Chmod(root, 0700); err != nil {
		return err
	}

	suffix := time.Now().UTC().Format("20060102-150405")
	tmp := filepath.Join(root, ".restore-tmp-"+suffix)
	old := filepath.Join(root, ".restore-old-"+suffix)
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := os.RemoveAll(old); err != nil {
		return err
	}
	if err := os.MkdirAll(tmp, 0700); err != nil {
		return err
	}
	for _, file := range files {
		dst, err := safeRestorePath(tmp, file.Path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(dst, file.Data, 0600); err != nil {
			return err
		}
	}
	if err := os.Chmod(tmp, 0700); err != nil {
		return err
	}

	if err := os.MkdirAll(old, 0700); err != nil {
		return err
	}
	stateName := "state.json"
	currentStatePath := filepath.Join(root, stateName)
	stagedStatePath := filepath.Join(tmp, stateName)
	if err := copyPrivateFileIfExists(currentStatePath, filepath.Join(old, stateName)); err != nil {
		return err
	}
	if err := moveRootEntries(root, old, rename, filepath.Base(tmp), filepath.Base(old), storage.StateLockFileName, storage.StateMutationLockFileName, stateName); err != nil {
		rollbackErr := moveRootEntries(old, root, rename, stateName)
		if rollbackErr == nil {
			_ = os.RemoveAll(tmp)
			_ = os.RemoveAll(old)
			return err
		}
		return errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr))
	}
	rollback := func(cause error) error {
		if cleanupErr := removeRootEntries(root, filepath.Base(tmp), filepath.Base(old), storage.StateLockFileName, storage.StateMutationLockFileName, stateName); cleanupErr != nil {
			return errors.Join(cause, fmt.Errorf("rollback cleanup failed: %w", cleanupErr))
		}
		if rollbackErr := moveRootEntries(old, root, rename, stateName); rollbackErr != nil {
			return errors.Join(cause, fmt.Errorf("rollback failed: %w", rollbackErr))
		}
		_ = os.RemoveAll(tmp)
		_ = os.RemoveAll(old)
		return cause
	}
	if err := moveRootEntries(tmp, root, rename, stateName); err != nil {
		return rollback(err)
	}
	if err := rename(stagedStatePath, currentStatePath); err != nil {
		return rollback(err)
	}
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	_ = os.RemoveAll(old)
	return nil
}

func copyPrivateFileIfExists(src, dst string) error {
	b, err := os.ReadFile(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0600)
}

func removeRootEntries(root string, skipNames ...string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	skip := map[string]bool{}
	for _, name := range skipNames {
		skip[name] = true
	}
	for _, entry := range entries {
		name := entry.Name()
		if skip[name] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			return err
		}
	}
	return nil
}

func moveRootEntries(src, dst string, rename func(string, string) error, skipNames ...string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	skip := map[string]bool{}
	for _, name := range skipNames {
		skip[name] = true
	}
	for _, entry := range entries {
		name := entry.Name()
		if skip[name] {
			continue
		}
		if err := rename(filepath.Join(src, name), filepath.Join(dst, name)); err != nil {
			return err
		}
	}
	return nil
}
