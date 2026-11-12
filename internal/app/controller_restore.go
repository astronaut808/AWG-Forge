package app

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"path/filepath"
	"reflect"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/storage"
)

// ValidateControllerBackupSnapshot ties the public registry to the archived
// controller and CA without changing the snapshot or admitting a node.
func ValidateControllerBackupSnapshot(ctx context.Context, path string, state config.State, material controlpki.Material) error {
	if err := validateStateMode(state); err != nil || state.EffectiveMode() != config.ModeController || state.Controller == nil {
		return errors.New("invalid controller snapshot identity")
	}
	var ca *x509.Certificate
	var generation string
	if control := state.Controller.Control; control != nil {
		if err := ValidateControlIdentityMetadata(control, 0); err != nil {
			return errors.New("invalid controller snapshot control identity")
		}
		if err := controlpki.Validate(material, controlpki.Endpoint{BindIP: control.BindIP, Advertised: control.Advertised, Port: control.Port}, control.CAPin, time.Now().UTC(), true); err != nil {
			return errors.New("invalid controller snapshot PKI")
		}
		block, rest := pem.Decode(material.CACert)
		if block == nil || len(rest) != 0 {
			return errors.New("invalid controller snapshot issuer")
		}
		var err error
		ca, err = x509.ParseCertificate(block.Bytes)
		if err != nil {
			return errors.New("invalid controller snapshot issuer")
		}
		generation = control.CAGeneration
	}
	return sqldb.VerifyControllerRegistrySnapshot(ctx, path, state.Controller.ControllerID, generation, ca)
}

// ReconcileRestoredController invalidates all restored credentials before cold
// restore clears its startup gate. The caller holds both offline state locks;
// this helper must not reacquire them or run Init, which refuses the marker.
// It neither clears restore-pending nor changes state, generations or epochs.
func ReconcileRestoredController(ctx context.Context, cfg config.Config, expected config.State, now time.Time) error {
	return reconcileRestoredController(ctx, cfg, expected, now, nil)
}

func reconcileRestoredController(ctx context.Context, cfg config.Config, expected config.State, now time.Time, step func(string) error) error {
	store := storage.New(cfg.ConfigDir)
	if !errors.Is(store.CheckRestorePending(), storage.ErrRestorePending) {
		return errors.New("controller reconciliation requires the restore startup gate")
	}
	state, err := store.Load()
	if err != nil {
		return err
	}
	if state.EffectiveMode() != config.ModeController || expected.EffectiveMode() != config.ModeController ||
		state.Controller == nil || expected.Controller == nil || !reflect.DeepEqual(state.Controller, expected.Controller) {
		return errors.New("restored controller identity mismatch")
	}
	if cfg.DatabaseMode != sqldb.ModeSQLite || !filepath.IsAbs(cfg.DatabasePath) || now.IsZero() || now.UnixMilli() <= 0 {
		return errors.New("invalid controller reconciliation configuration")
	}
	if _, err := controlauth.LoadKeys(filepath.Join(cfg.ConfigDir, controlauth.KeyFileName)); err != nil {
		return errors.New("restored controller authentication keys are invalid")
	}
	var material controlpki.Material
	if control := state.Controller.Control; control != nil {
		if err := ValidateControlIdentityState(control, cfg.WebUIPort); err != nil {
			return errors.New("restored control identity state is invalid")
		}
		material, err = store.LoadControlIdentity(control.CAGeneration, control.ServerGeneration)
		if err != nil {
			return errors.New("restored control identity files are invalid")
		}
	}
	if err := ValidateControllerBackupSnapshot(ctx, cfg.DatabasePath, state, material); err != nil {
		return err
	}
	if err := controllerRestoreStep(step, "after-installed-validation"); err != nil {
		return err
	}
	db, err := sqldb.Open(ctx, cfg)
	if err != nil {
		return err
	}
	migrateCtx, cancel := context.WithTimeout(ctx, sqldb.MigrationTimeout(cfg.DatabaseQueryTimeout))
	err = db.Migrate(migrateCtx)
	cancel()
	if err == nil {
		err = controllerRestoreStep(step, "after-migration")
	}
	if err == nil {
		err = db.DisableControllerAuthAfterRestore(ctx, now.UTC())
	}
	if err := errors.Join(err, db.Close()); err != nil {
		return err
	}
	if err := controllerRestoreStep(step, "after-reset-commit"); err != nil {
		return err
	}
	// Reopen after commit/close: a successful transaction alone is not a
	// verification of the authority that startup will read from this path.
	if err := ValidateControllerBackupSnapshot(ctx, cfg.DatabasePath, state, material); err != nil {
		return err
	}
	if err := controllerRestoreStep(step, "after-reopened-validation"); err != nil {
		return err
	}
	db, err = sqldb.Open(ctx, cfg)
	if err != nil {
		return err
	}
	if err := errors.Join(db.VerifyControllerRestoreReset(ctx), db.Close()); err != nil {
		return err
	}
	return controllerRestoreStep(step, "after-denial-verification")
}

func controllerRestoreStep(step func(string) error, point string) error {
	if step != nil {
		return step(point)
	}
	return nil
}
