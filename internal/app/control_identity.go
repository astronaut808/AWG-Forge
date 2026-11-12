package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/google/uuid"
)

var controlPinRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type ControlIdentityRequest struct {
	BindIP     string
	Advertised string
	Port       int
	Now        time.Time
}

type ControlIdentityResult = config.ControlIdentityState

// PrepareControlIdentity is deliberately an application-only entry point.
// No startup path, handler, or listener calls it.
func (s *Service) PrepareControlIdentity(ctx context.Context, request ControlIdentityRequest) (result ControlIdentityResult, err error) {
	if err := s.lockStateMutation(); err != nil {
		return result, err
	}
	defer s.unlockStateMutation()
	return s.prepareControlIdentityLocked(ctx, request)
}

func (s *Service) prepareControlIdentityLocked(ctx context.Context, request ControlIdentityRequest) (result ControlIdentityResult, err error) {
	if err := s.store.CheckRestorePending(); err != nil {
		return result, err
	}
	if _, err := s.store.LoadPendingDesiredStateCommit(); err == nil {
		return result, errors.New("desired-state commit is pending")
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, errors.New("cannot inspect desired-state journal")
	}
	if _, err := s.store.LoadControllerActivationJournal(); err == nil {
		return result, errors.New("controller activation is pending")
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, errors.New("cannot inspect controller activation journal")
	}
	state, err := s.store.Load()
	if err != nil {
		return result, err
	}
	if err := validateStateMode(state); err != nil {
		return result, err
	}
	if state.EffectiveMode() != config.ModeController || state.Controller == nil {
		return result, errors.New("control identity requires an active controller")
	}
	if err := s.recoverControlIdentityLocked(state); err != nil {
		return result, errors.New("control identity preparation requires offline inspection")
	}
	if err := s.recoverControlServerRotationLocked(state, request.Now); err != nil {
		return result, errors.New("control server rotation requires offline inspection")
	}
	endpoint, err := controlpki.NormalizeEndpoint(request.BindIP, request.Advertised, request.Port, s.cfg.WebUIPort)
	if err != nil {
		return result, err
	}
	if state.Controller.Control != nil {
		control := state.Controller.Control
		if err := ValidateControlIdentityState(control, s.cfg.WebUIPort); err != nil {
			return result, err
		}
		if control.BindIP != endpoint.BindIP || control.Advertised != endpoint.Advertised || control.Port != endpoint.Port {
			return result, errors.New("control identity is already prepared for a different endpoint")
		}
		if err := s.validateControlIdentityLocked(control, request.Now, false); err != nil {
			return result, errors.New("committed control identity is unusable")
		}
		return *control, nil
	}
	if s.cfg.DatabaseMode != sqldb.ModeSQLite {
		return result, errors.New("control identity preparation requires SQLite")
	}
	db, err := sqldb.Open(ctx, s.cfg)
	if err != nil {
		return result, errors.New("controller authentication database is unavailable")
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(ctx); err != nil {
		return result, errors.New("controller authentication database is unavailable")
	}
	initialized, err := db.ControllerAuthInitialized(ctx)
	if err != nil || !initialized {
		return result, errors.New("controller authentication is not initialized")
	}
	if _, err := controlauth.LoadKeys(filepath.Join(s.cfg.ConfigDir, controlauth.KeyFileName)); err != nil {
		return result, errors.New("controller authentication key is unavailable")
	}
	now := request.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	caGeneration, err := newControlGeneration()
	if err != nil {
		return result, err
	}
	serverGeneration, err := newControlGeneration()
	if err != nil {
		return result, err
	}
	if err := s.store.CheckControlIdentityPath(caGeneration, serverGeneration); err != nil {
		return result, errors.New("control identity storage path is unsafe")
	}
	journal := storage.ControlIdentityJournal{ControllerID: state.Controller.ControllerID, CAGeneration: caGeneration, ServerGeneration: serverGeneration, StartedAt: now}
	if err := s.store.SaveControlIdentityJournal(journal); err != nil {
		return result, fmt.Errorf("record control identity preparation: %w", err)
	}
	committed := false
	stateSaveAttempt := false
	defer func() {
		if !committed && !stateSaveAttempt {
			current, loadErr := s.store.Load()
			if loadErr != nil {
				err = errors.Join(err, errors.New("control identity cleanup requires offline inspection"))
				return
			}
			if cleanupErr := s.recoverControlIdentityLocked(current); cleanupErr != nil {
				err = errors.Join(err, errors.New("control identity cleanup requires offline inspection"))
			}
		}
	}()
	if err := s.controlStep("journal"); err != nil {
		return result, err
	}
	material, pin, err := controlpki.Generate(endpoint, now)
	if err != nil {
		return result, errors.New("generate control identity failed")
	}
	if err := s.store.WriteControlIdentity(journal, material, s.controlIdentityStep); err != nil {
		return result, errors.New("write control identity failed")
	}
	control := &config.ControlIdentityState{Enabled: false, BindIP: endpoint.BindIP, Advertised: endpoint.Advertised, Port: endpoint.Port, CAGeneration: caGeneration, ServerGeneration: serverGeneration, CAPin: pin}
	if err := s.validateControlIdentityLocked(control, now, false); err != nil {
		return result, errors.New("verify written control identity failed")
	}
	if err := s.controlStep("before-state"); err != nil {
		return result, err
	}
	state.Controller.Control = control
	state.UpdatedAt = now.UTC()
	stateSaveAttempt = true
	if err := s.saveState(state); err != nil {
		return result, errors.New("commit control identity failed")
	}
	if err := s.store.SyncStateDirectory(); err != nil {
		return result, errors.New("sync committed control identity failed")
	}
	committed = true
	if err := s.controlStep("after-state"); err != nil {
		return result, err
	}
	if err := s.store.DeleteControlIdentityJournal(); err != nil {
		return result, errors.New("control identity committed but journal cleanup failed")
	}
	return *control, nil
}

func newControlGeneration() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func (s *Service) controlStep(name string) error {
	if s.controlIdentityStep != nil {
		return s.controlIdentityStep(name)
	}
	return nil
}

func ValidateControlIdentityState(control *config.ControlIdentityState, webPort int) error {
	if control == nil || control.Enabled {
		return errors.New("control identity must be prepared and disabled")
	}
	return ValidateControlIdentityMetadata(control, webPort)
}

// ValidateControlIdentityMetadata validates committed and archived identities.
// Enabled records retain consent for this exact endpoint. Restoring an archive
// must separately force disabled state before clearing the gate.
func ValidateControlIdentityMetadata(control *config.ControlIdentityState, webPort int) error {
	if control == nil {
		return errors.New("control identity is not prepared")
	}
	endpoint, err := controlpki.NormalizeEndpoint(control.BindIP, control.Advertised, control.Port, webPort)
	if err != nil || endpoint.BindIP != control.BindIP || endpoint.Advertised != control.Advertised {
		return errors.New("invalid control identity endpoint")
	}
	if _, err := storage.ControlIdentityRelativePaths(control.CAGeneration, control.ServerGeneration); err != nil || !controlPinRE.MatchString(control.CAPin) {
		return errors.New("invalid control identity generation or pin")
	}
	return nil
}

func (s *Service) validateControlIdentityLocked(control *config.ControlIdentityState, now time.Time, allowExpired bool) error {
	if err := ValidateControlIdentityMetadata(control, s.cfg.WebUIPort); err != nil {
		return err
	}
	material, err := s.store.LoadControlIdentity(control.CAGeneration, control.ServerGeneration)
	if err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return controlpki.Validate(material, controlpki.Endpoint{BindIP: control.BindIP, Advertised: control.Advertised, Port: control.Port}, control.CAPin, now, allowExpired)
}

func (s *Service) recoverControlIdentityLocked(state config.State) error {
	journal, err := s.store.LoadControlIdentityJournal()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.store.CheckNoControlServerRotation(); err != nil {
		return errors.New("overlapping control identity transitions")
	}
	if _, err := uuid.Parse(journal.ControllerID); err != nil || state.Controller == nil || state.Controller.ControllerID != journal.ControllerID {
		return errors.New("control identity journal controller mismatch")
	}
	if state.Controller.Control != nil {
		control := state.Controller.Control
		if control.CAGeneration != journal.CAGeneration || control.ServerGeneration != journal.ServerGeneration {
			return errors.New("control identity journal generation mismatch")
		}
		if err := s.validateControlIdentityLocked(control, time.Time{}, true); err != nil {
			return err
		}
		return s.store.DeleteControlIdentityJournal()
	}
	if err := s.store.RemoveJournalOwnedControlIdentity(journal); err != nil {
		return err
	}
	return s.store.DeleteControlIdentityJournal()
}
