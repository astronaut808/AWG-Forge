package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/google/uuid"
)

var (
	ErrControllerAlreadyActive        = errors.New("controller mode is already active")
	ErrControllerActivationRequiresDB = errors.New("controller activation requires SQLite")
	ErrInvalidStateMode               = errors.New("invalid state mode")
)

type ControllerActivationRequest struct {
	Username         string
	Password         string
	TOTPSecret       string
	TOTPConfirmation string
	Now              time.Time
}

type ControllerActivationResult struct {
	ControllerID  string
	Username      string
	RecoveryCodes []string
}

func (s *Service) ActivateController(ctx context.Context, request ControllerActivationRequest) (ControllerActivationResult, error) {
	if err := s.lockStateMutation(); err != nil {
		return ControllerActivationResult{}, err
	}
	defer s.unlockStateMutation()

	state, err := s.initLocked()
	if err != nil {
		return ControllerActivationResult{}, err
	}
	if state.EffectiveMode() == config.ModeController {
		return ControllerActivationResult{}, ErrControllerAlreadyActive
	}
	if state.EffectiveMode() != config.ModeStandalone || state.ManagedNode != nil || state.Controller != nil {
		return ControllerActivationResult{}, fmt.Errorf("%w: controller activation requires standalone state", ErrInvalidStateMode)
	}
	if s.cfg.DatabaseMode != sqldb.ModeSQLite {
		return ControllerActivationResult{}, ErrControllerActivationRequiresDB
	}
	if request.Now.IsZero() {
		request.Now = time.Now().UTC()
	} else {
		request.Now = request.Now.UTC()
	}

	db, err := sqldb.Open(ctx, s.cfg)
	if err != nil {
		return ControllerActivationResult{}, fmt.Errorf("open controller database: %w", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(ctx); err != nil {
		return ControllerActivationResult{}, fmt.Errorf("migrate controller database: %w", err)
	}
	initialized, err := db.ControllerAuthInitialized(ctx)
	if err != nil {
		return ControllerActivationResult{}, fmt.Errorf("inspect controller authentication: %w", err)
	}
	if initialized {
		return ControllerActivationResult{}, errors.New("controller authentication exists without active controller mode")
	}

	keyPath := filepath.Join(s.cfg.ConfigDir, controlauth.KeyFileName)
	if _, err := os.Lstat(keyPath); err == nil {
		return ControllerActivationResult{}, errors.New("controller authentication key exists without active controller mode")
	} else if !errors.Is(err, os.ErrNotExist) {
		return ControllerActivationResult{}, fmt.Errorf("inspect controller authentication key: %w", err)
	}
	controllerID, err := uuid.NewRandom()
	if err != nil {
		return ControllerActivationResult{}, fmt.Errorf("generate controller ID: %w", err)
	}
	journal := storage.ControllerActivationJournal{
		ControllerID: controllerID.String(),
		StartedAt:    request.Now,
	}
	if err := s.store.SaveControllerActivationJournal(journal); err != nil {
		return ControllerActivationResult{}, fmt.Errorf("save controller activation journal: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), s.cfg.DatabaseQueryTimeout)
		defer cancel()
		if cleanupErr := s.rollbackControllerActivation(cleanupCtx, db); cleanupErr != nil {
			s.log("error", "controller.activation.rollback_failed", "controller activation rollback failed", nil, cleanupErr)
		}
	}()

	keys, err := controlauth.LoadOrCreateKeys(keyPath, nil)
	if err != nil {
		return ControllerActivationResult{}, fmt.Errorf("create controller authentication keys: %w", err)
	}
	auth, err := controlauth.NewService(db, keys, s.controllerAuthOptions)
	if err != nil {
		return ControllerActivationResult{}, fmt.Errorf("initialize controller authentication: %w", err)
	}
	enrollment, err := auth.EnrollAdmin(ctx, request.Username, request.Password, request.TOTPSecret, request.TOTPConfirmation, request.Now)
	if err != nil {
		return ControllerActivationResult{}, fmt.Errorf("enroll controller administrator: %w", err)
	}
	candidate := state
	candidate.Mode = config.ModeController
	candidate.Controller = &config.ControllerState{ControllerID: controllerID.String(), ActivatedAt: request.Now}
	candidate.UpdatedAt = request.Now
	if err := validateStateMode(candidate); err != nil {
		return ControllerActivationResult{}, err
	}
	if err := s.saveState(candidate); err != nil {
		return ControllerActivationResult{}, fmt.Errorf("commit controller mode: %w", err)
	}
	committed = true
	if err := s.store.DeleteControllerActivationJournal(); err != nil {
		s.log("warn", "controller.activation.journal_cleanup_failed", "controller activation committed but journal cleanup failed", nil, err)
	}
	s.log("info", "controller.activation.completed", "controller mode activated", map[string]any{"controller_id": controllerID.String()}, nil)
	return ControllerActivationResult{
		ControllerID:  controllerID.String(),
		Username:      enrollment.Username,
		RecoveryCodes: enrollment.RecoveryCodes,
	}, nil
}

func (s *Service) recoverControllerActivationLocked(state config.State) error {
	journal, err := s.store.LoadControllerActivationJournal()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load controller activation journal: %w", err)
	}
	if _, err := uuid.Parse(journal.ControllerID); err != nil || journal.StartedAt.IsZero() {
		return errors.New("invalid controller activation journal")
	}
	if state.EffectiveMode() == config.ModeController {
		if state.Controller == nil || state.Controller.ControllerID != journal.ControllerID {
			return errors.New("controller activation journal does not match active controller")
		}
		return s.store.DeleteControllerActivationJournal()
	}
	if state.EffectiveMode() != config.ModeStandalone {
		return errors.New("controller activation journal conflicts with state mode")
	}
	if s.cfg.DatabaseMode != sqldb.ModeSQLite {
		return ErrControllerActivationRequiresDB
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.DatabaseQueryTimeout)
	defer cancel()
	db, err := sqldb.Open(ctx, s.cfg)
	if err != nil {
		return fmt.Errorf("open controller database for activation recovery: %w", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate controller database for activation recovery: %w", err)
	}
	if err := s.rollbackControllerActivation(ctx, db); err != nil {
		return fmt.Errorf("recover controller activation: %w", err)
	}
	s.log("warn", "controller.activation.recovered", "incomplete controller activation rolled back", nil, nil)
	return nil
}

func (s *Service) rollbackControllerActivation(ctx context.Context, db *sqldb.DB) error {
	if db != nil {
		if err := db.ResetControllerAuth(ctx); err != nil {
			return fmt.Errorf("reset controller authentication: %w", err)
		}
	}
	if err := controlauth.RemoveKeyFile(filepath.Join(s.cfg.ConfigDir, controlauth.KeyFileName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove controller authentication key: %w", err)
	}
	if err := s.store.DeleteControllerActivationJournal(); err != nil {
		return fmt.Errorf("remove controller activation journal: %w", err)
	}
	return nil
}

func validateStateMode(state config.State) error {
	switch state.EffectiveMode() {
	case config.ModeStandalone:
		if state.Controller != nil || state.ManagedNode != nil {
			return fmt.Errorf("%w: standalone state contains controller metadata", ErrInvalidStateMode)
		}
	case config.ModeController:
		if state.Controller == nil || state.ManagedNode != nil || state.Controller.ActivatedAt.IsZero() {
			return fmt.Errorf("%w: incomplete controller state", ErrInvalidStateMode)
		}
		if _, err := uuid.Parse(state.Controller.ControllerID); err != nil {
			return fmt.Errorf("%w: invalid controller ID", ErrInvalidStateMode)
		}
	case config.ModeNode:
		if state.ManagedNode == nil || state.Controller != nil {
			return fmt.Errorf("%w: incomplete node state", ErrInvalidStateMode)
		}
		if err := validateManagedNodeState(state.ManagedNode); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: unsupported mode %q", ErrInvalidStateMode, state.Mode)
	}
	return nil
}
