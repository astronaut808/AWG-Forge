package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/storage"
)

// restoreController is called only with both offline locks held. A pending
// marker stays in place after any failure following BeginRestorePending; a
// partial restore must be inspected and recovered offline before serving.
func restoreController(ctx context.Context, cfg config.Config, password string, current config.State, backup validatedBackup) error {
	return restoreControllerWithFiles(ctx, cfg, password, current, backup, func(root string, files []restoreFile) error {
		return restoreFilesWithAudit(root, files, cfg.AuditLogPath)
	})
}

func restoreControllerWithFiles(ctx context.Context, cfg config.Config, password string, current config.State, backup validatedBackup, applyFiles func(string, []restoreFile) error) error {
	if cfg.DatabaseMode != sqldb.ModeSQLite || !filepath.IsAbs(cfg.DatabasePath) {
		return errors.New("controller restore requires an absolute SQLite database path")
	}
	if current.Controller == nil || backup.State.Controller == nil || current.Controller.ControllerID != backup.State.Controller.ControllerID {
		return errors.New("controller restore identity mismatch")
	}
	if err := checkRestoreStagingAbsent(cfg.ConfigDir); err != nil {
		return err
	}
	preRestore, err := preRestoreBackupFile(ctx, cfg, current, password)
	if err != nil {
		return fmt.Errorf("create encrypted pre-restore backup: %w", err)
	}
	files := append([]restoreFile(nil), backup.Files...)
	var snapshot []byte
	for i, file := range files {
		if file.Path == controllerSnapshotArchivePath {
			snapshot = file.Data
			files = append(files[:i], files[i+1:]...)
			break
		}
	}
	if len(snapshot) == 0 {
		return errors.New("controller database snapshot is missing")
	}
	configuredPath, external, err := controllerDatabaseRestorePath(cfg)
	if err != nil {
		return err
	}
	var externalStage string
	if external {
		externalStage, err = stageExternalDatabase(cfg.DatabasePath, snapshot)
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(externalStage) }()
	} else {
		files = append(files, restoreFile{Path: configuredPath, Data: snapshot})
	}
	preserve, err := auditRestorePreserveNames(cfg.ConfigDir, cfg.AuditLogPath)
	if err != nil {
		return err
	}
	if err := validatePreservedRootCollisions(files, preserve); err != nil {
		return err
	}
	if err := savePreRestoreBackup(cfg.ConfigDir, preRestore); err != nil {
		return fmt.Errorf("save encrypted pre-restore backup: %w", err)
	}
	store := storage.New(cfg.ConfigDir)
	if err := store.BeginRestorePending(current.Controller.ControllerID); err != nil {
		return err
	}
	if err := applyFiles(cfg.ConfigDir, files); err != nil {
		return fmt.Errorf("restore controller files; offline recovery required: %w", err)
	}
	if external {
		if err := installExternalDatabase(cfg.DatabasePath, externalStage); err != nil {
			return fmt.Errorf("install controller database; offline recovery required: %w", err)
		}
	}
	if err := verifyRestoredController(ctx, cfg, backup.State.Controller.ControllerID); err != nil {
		return fmt.Errorf("verify restored controller; offline recovery required: %w", err)
	}
	if err := syncRestoredFiles(cfg.ConfigDir, files); err != nil {
		return fmt.Errorf("sync restored controller; offline recovery required: %w", err)
	}
	if err := store.ClearRestorePending(); err != nil {
		return fmt.Errorf("clear restored controller gate; offline recovery required: %w", err)
	}
	return nil
}

func requireControllerArchiveOutsideConfig(root, path string) error {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)) {
		return errors.New("controller restore archive must be kept outside the configuration directory")
	}
	return nil
}

func checkRestoreStagingAbsent(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".restore-tmp-") || strings.HasPrefix(entry.Name(), ".restore-old-") {
			return errors.New("existing restore staging directory requires offline inspection before controller restore")
		}
		if entry.Name() == ".desired-state-commit.json" || entry.Name() == storage.ControllerActivationJournalFileName {
			return errors.New("pending state mutation journal requires recovery before controller restore")
		}
	}
	return nil
}

func controllerDatabaseRestorePath(cfg config.Config) (string, bool, error) {
	if cfg.DatabasePath == "" || !filepath.IsAbs(cfg.DatabasePath) {
		return "", false, errors.New("controller database path must be absolute")
	}
	canonicalRoot, err := filepath.EvalSymlinks(cfg.ConfigDir)
	if err != nil {
		return "", false, err
	}
	canonicalDatabase, err := filepath.EvalSymlinks(cfg.DatabasePath)
	if err != nil {
		return "", false, err
	}
	rel, err := filepath.Rel(canonicalRoot, canonicalDatabase)
	if err != nil {
		return "", false, err
	}
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		rawRel, rawErr := filepath.Rel(cfg.ConfigDir, cfg.DatabasePath)
		if rawErr == nil && rawRel != ".." && !strings.HasPrefix(rawRel, ".."+string(filepath.Separator)) {
			return "", false, errors.New("controller database path escapes the configuration directory through a symlink")
		}
		return "", true, nil
	}
	rawRel, err := filepath.Rel(cfg.ConfigDir, cfg.DatabasePath)
	if err == nil && rawRel != ".." && !strings.HasPrefix(rawRel, ".."+string(filepath.Separator)) && filepath.Clean(rawRel) != filepath.Clean(rel) {
		return "", false, errors.New("controller database path contains a symlink inside the configuration directory")
	}
	rel = filepath.ToSlash(rel)
	if rel == "state.json" || rel == controlauth.KeyFileName || rel == storage.RestorePendingFileName || strings.HasPrefix(rel, ".restore-") || strings.HasPrefix(rel, "backups/") {
		return "", false, errors.New("controller database path conflicts with reserved state files")
	}
	if _, err := safeRestorePath(cfg.ConfigDir, rel); err != nil {
		return "", false, err
	}
	return rel, false, nil
}

func stageExternalDatabase(path string, snapshot []byte) (string, error) {
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return "", errors.New("external controller database directory must be private and non-symlinked")
	}
	info, err = os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return "", errors.New("external controller database must be a private regular file")
	}
	stage, err := os.MkdirTemp(parent, ".awg-restore-db-")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(stage, "database.sqlite"), snapshot, 0600); err != nil {
		_ = os.RemoveAll(stage)
		return "", err
	}
	if err := syncRestoredFiles(stage, []restoreFile{{Path: "database.sqlite"}}); err != nil {
		_ = os.RemoveAll(stage)
		return "", err
	}
	return stage, nil
}

func installExternalDatabase(path, stage string) error {
	parent := filepath.Dir(path)
	old, err := os.MkdirTemp(parent, ".awg-restore-old-db-")
	if err != nil {
		return err
	}
	moved := []string{}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		source := path + suffix
		info, err := os.Lstat(source)
		if errors.Is(err, os.ErrNotExist) && suffix != "" {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return errors.New("controller database sidecar must be a regular file")
		}
		if err := os.Rename(source, filepath.Join(old, "database.sqlite"+suffix)); err != nil {
			return err
		}
		moved = append(moved, suffix)
	}
	if err := os.Rename(filepath.Join(stage, "database.sqlite"), path); err != nil {
		for i := len(moved) - 1; i >= 0; i-- {
			_ = os.Rename(filepath.Join(old, "database.sqlite"+moved[i]), path+moved[i])
		}
		return err
	}
	if err := syncDirectory(parent); err != nil {
		return err
	}
	return os.RemoveAll(old)
}

func verifyRestoredController(ctx context.Context, cfg config.Config, controllerID string) error {
	state, err := storage.New(cfg.ConfigDir).Load()
	if err != nil {
		return err
	}
	if state.EffectiveMode() != config.ModeController || state.Controller == nil || state.Controller.ControllerID != controllerID {
		return errors.New("restored controller identity mismatch")
	}
	if _, err := controlauth.LoadKeys(filepath.Join(cfg.ConfigDir, controlauth.KeyFileName)); err != nil {
		return err
	}
	if err := sqldb.VerifyControllerSnapshot(ctx, cfg.DatabasePath); err != nil {
		return err
	}
	db, err := sqldb.Open(ctx, cfg)
	if err != nil {
		return err
	}
	if err := db.Migrate(ctx); err != nil {
		_ = db.Close()
		return fmt.Errorf("migrate restored controller database: %w", err)
	}
	if err := db.DisableControllerAuthAfterRestore(ctx, time.Now().UTC()); err != nil {
		_ = db.Close()
		return err
	}
	return db.Close()
}

func syncRestoredFiles(root string, files []restoreFile) error {
	dirs := map[string]bool{root: true}
	for _, file := range files {
		path, err := safeRestorePath(root, file.Path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return errors.New("restored file must be a private regular file")
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		syncErr := f.Sync()
		closeErr := f.Close()
		if err := errors.Join(syncErr, closeErr); err != nil {
			return err
		}
		for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
			dirs[dir] = true
			if dir == root {
				break
			}
		}
	}
	ordered := make([]string, 0, len(dirs))
	for dir := range dirs {
		ordered = append(ordered, dir)
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, dir := range ordered {
		if err := syncDirectory(dir); err != nil {
			return err
		}
	}
	return nil
}

func syncDirectory(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	return errors.Join(syncErr, file.Close())
}
