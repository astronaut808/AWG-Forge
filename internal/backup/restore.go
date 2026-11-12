package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

func restoreFiles(root string, files []restoreFile, rename func(string, string) error, removeAll func(string) error) (err error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	if err := os.Chmod(root, 0700); err != nil {
		return err
	}

	// Older releases can leave the only copy of a file in a recovery directory.
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".restore-old-") {
			return fmt.Errorf("recover previous restore data in %s before retrying", entry.Name())
		}
	}

	const stagingName = ".restore-pending"
	staging := filepath.Join(root, stagingName)
	if err := os.Mkdir(staging, 0700); err != nil {
		return fmt.Errorf("could not create restore staging directory: %w", err)
	}
	preserve, committed := false, false
	defer func() {
		if preserve {
			return
		}
		if cleanupErr := removeAll(staging); cleanupErr != nil {
			if committed {
				slog.Warn("restore completed but could not remove staging directory", "directory", staging, "error", cleanupErr)
				return
			}
			err = errors.Join(err, cleanupErr)
		}
	}()

	tmp := filepath.Join(staging, "new")
	old := filepath.Join(staging, "old")
	if err := os.MkdirAll(tmp, 0700); err != nil {
		return err
	}
	for _, file := range files {
		clean, err := cleanArchivePath(file.Path)
		if err != nil {
			return err
		}
		top, _, _ := strings.Cut(clean, "/")
		if top == stagingName || strings.HasPrefix(top, ".restore-old-") {
			return errors.New("archive uses a reserved restore path")
		}
		dst, err := safeRestorePath(tmp, clean)
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
	movedOld, moveErr := moveRootEntries(root, old, rename, stagingName)
	if moveErr != nil {
		if rollbackErr := undoMoves(old, root, movedOld, rename); rollbackErr != nil {
			preserve = true
			return errors.Join(moveErr, fmt.Errorf("rollback failed; previous data retained in %s: %w", old, rollbackErr))
		}
		return moveErr
	}
	movedNew, moveErr := moveRootEntries(tmp, root, rename, "")
	if moveErr != nil {
		if cleanupErr := undoMoves(root, tmp, movedNew, rename); cleanupErr != nil {
			preserve = true
			return errors.Join(moveErr, fmt.Errorf("rollback cleanup failed; previous data retained in %s: %w", old, cleanupErr))
		}
		if rollbackErr := undoMoves(old, root, movedOld, rename); rollbackErr != nil {
			preserve = true
			return errors.Join(moveErr, fmt.Errorf("rollback failed; previous data retained in %s: %w", old, rollbackErr))
		}
		return moveErr
	}
	committed = true
	return nil
}

func undoMoves(src, dst string, moved []string, rename func(string, string) error) error {
	var rollbackErr error
	for i := len(moved) - 1; i >= 0; i-- {
		name := moved[i]
		target := filepath.Join(dst, name)
		if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("could not safely restore %s", target))
		} else if err := rename(filepath.Join(src, name), target); err != nil {
			rollbackErr = errors.Join(rollbackErr, err)
		}
	}
	return rollbackErr
}

func moveRootEntries(src, dst string, rename func(string, string) error, skip string) ([]string, error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return nil, err
	}
	moved := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == skip {
			continue
		}
		if err := rename(filepath.Join(src, name), filepath.Join(dst, name)); err != nil {
			return moved, err
		}
		moved = append(moved, name)
	}
	return moved, nil
}
