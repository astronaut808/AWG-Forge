package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	if clean == storage.StateLockFileName || clean == storage.StateMutationLockFileName || clean == storage.RestorePendingFileName {
		return "", errors.New("backup contains reserved state lock path")
	}
	if clean == "backups" || strings.HasPrefix(clean, "backups/") {
		return "", errors.New("backup contains reserved backup history path")
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

func preRestoreBackupFile(ctx context.Context, cfg config.Config, state config.State, password string) (restoreFile, error) {
	archive, err := createFromState(ctx, cfg, state, password, Options{})
	if err != nil {
		return restoreFile{}, err
	}
	path := "backups/pre-restore-" + time.Now().UTC().Format("20060102-150405.000000000") + ".afbackup"
	return restoreFile{Path: path, Data: archive.Data}, nil
}

func savePreRestoreBackup(root string, file restoreFile) error {
	if !strings.HasPrefix(file.Path, "backups/pre-restore-") || !strings.HasSuffix(file.Path, ".afbackup") {
		return errors.New("invalid pre-restore backup path")
	}
	dir := filepath.Join(root, "backups")
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return errors.New("backup history directory must be private and non-symlinked")
	}
	path := filepath.Join(root, filepath.FromSlash(file.Path))
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := out.Write(file.Data)
	syncErr := out.Sync()
	closeErr := out.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = os.Remove(path)
		return err
	}
	stored, err := os.Open(path)
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, readErr := io.Copy(hash, stored)
	closeErr = stored.Close()
	expected := sha256.Sum256(file.Data)
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	if n != int64(len(file.Data)) || !bytes.Equal(hash.Sum(nil), expected[:]) {
		_ = os.Remove(path)
		return errors.New("written pre-restore backup does not match verified archive")
	}
	return errors.Join(syncDirectory(dir), syncDirectory(root))
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
	return restoreFilesWithAudit(root, files, filepath.Join(root, "audit.log"))
}

func restoreFilesWithRename(root string, files []restoreFile, rename func(string, string) error) error {
	return restoreFilesWithRenameAndAudit(root, files, rename, filepath.Join(root, "audit.log"))
}

func restoreFilesWithAudit(root string, files []restoreFile, auditPath string) error {
	return restoreFilesWithRenameAndAudit(root, files, os.Rename, auditPath)
}

func restoreFilesWithRenameAndAudit(root string, files []restoreFile, rename func(string, string) error, auditPath string) error {
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	if err := os.Chmod(root, 0700); err != nil {
		return err
	}
	preserve, err := auditRestorePreserveNames(root, auditPath)
	if err != nil {
		return err
	}
	if err := validatePreservedRootCollisions(files, preserve); err != nil {
		return err
	}

	tmp, err := os.MkdirTemp(root, ".restore-tmp-")
	if err != nil {
		return err
	}
	old, err := os.MkdirTemp(root, ".restore-old-")
	if err != nil {
		_ = os.RemoveAll(tmp)
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

	if err := os.Chmod(old, 0700); err != nil {
		return err
	}
	stateName := "state.json"
	currentStatePath := filepath.Join(root, stateName)
	stagedStatePath := filepath.Join(tmp, stateName)
	if err := copyPrivateFileIfExists(currentStatePath, filepath.Join(old, stateName)); err != nil {
		return err
	}
	skip := append([]string{filepath.Base(tmp), filepath.Base(old), storage.StateLockFileName, storage.StateMutationLockFileName, storage.RestorePendingFileName, "backups", stateName}, preserve...)
	if err := moveRootEntries(root, old, rename, skip...); err != nil {
		rollbackErr := moveRootEntries(old, root, rename, stateName)
		if rollbackErr == nil {
			_ = os.RemoveAll(tmp)
			_ = os.RemoveAll(old)
			return err
		}
		return errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr))
	}
	rollback := func(cause error) error {
		if cleanupErr := removeRootEntries(root, skip...); cleanupErr != nil {
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
	if err := os.RemoveAll(old); err != nil {
		return err
	}
	return nil
}

func auditRestorePreserveNames(root, auditPath string) ([]string, error) {
	if auditPath == "" {
		auditPath = filepath.Join(root, "audit.log")
	}
	if !filepath.IsAbs(auditPath) {
		return nil, errors.New("audit log path must be absolute during restore")
	}
	rel, err := filepath.Rel(root, auditPath)
	if err != nil {
		return nil, err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil, nil
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) > 1 {
		for _, managed := range []string{"tunnels", "tls", "control", "backups"} {
			if parts[0] == managed {
				return nil, errors.New("audit log path overlaps restored application data")
			}
		}
		current := root
		for _, part := range parts[:len(parts)-1] {
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if errors.Is(err, os.ErrNotExist) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
				return nil, errors.New("audit log directory must be private and non-symlinked")
			}
		}
		if _, err := privateAuditEntries(current, parts[len(parts)-1]); err != nil {
			return nil, err
		}
		return []string{parts[0]}, nil
	}
	return privateAuditEntries(root, parts[0])
}

func privateAuditEntries(dir, base string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.Name() == base || strings.HasPrefix(entry.Name(), base+".") {
			info, err := entry.Info()
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
				return nil, errors.New("audit log and rotations must be private regular files")
			}
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

func validatePreservedRootCollisions(files []restoreFile, preserved []string) error {
	keep := map[string]bool{}
	for _, name := range preserved {
		keep[name] = true
	}
	for _, file := range files {
		clean, err := cleanArchivePath(file.Path)
		if err != nil {
			return err
		}
		if keep[strings.SplitN(clean, "/", 2)[0]] {
			return fmt.Errorf("audit log path overlaps restored file %q", clean)
		}
	}
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
