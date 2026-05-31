package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
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

func preRestoreBackupFile(ctx context.Context, cfg config.Config, password string) (restoreFile, bool, error) {
	if _, err := os.Stat(filepath.Join(cfg.ConfigDir, "state.json")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return restoreFile{}, false, nil
		}
		return restoreFile{}, false, err
	}
	service := app.New(cfg)
	archive, err := Create(ctx, cfg, service, password, Options{})
	if err != nil {
		return restoreFile{}, false, err
	}
	path := "backups/pre-restore-" + time.Now().UTC().Format("20060102-150405") + ".afbackup"
	return restoreFile{Path: path, Data: archive.Data}, true, nil
}

func restoreFiles(root string, files []restoreFile) error {
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
	if err := moveRootEntries(root, old, filepath.Base(tmp), filepath.Base(old)); err != nil {
		return err
	}
	if err := moveRootEntries(tmp, root); err != nil {
		if cleanupErr := removeRootEntries(root, filepath.Base(tmp), filepath.Base(old)); cleanupErr != nil {
			return errors.Join(err, fmt.Errorf("rollback cleanup failed: %w", cleanupErr))
		}
		if rollbackErr := moveRootEntries(old, root); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr))
		}
		return err
	}
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	_ = os.RemoveAll(old)
	return nil
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

func moveRootEntries(src, dst string, skipNames ...string) error {
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
		if err := os.Rename(filepath.Join(src, name), filepath.Join(dst, name)); err != nil {
			return err
		}
	}
	return nil
}
