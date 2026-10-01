package app

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/controlserver"
	"github.com/astronaut808/awg-forge/internal/storage"
)

var errStaleControlServerGeneration = errors.New("active control server generation changed")

// rotateControlServerLeaf has no production caller. An expected generation
// fences retries after an uncertain commit. Enabled state requires the private
// application runtime owner; disabled-state tests may use an isolated runtime.
func (s *Service) rotateControlServerLeaf(ctx context.Context, expected string, now time.Time, live *controlserver.Runtime) (result config.ControlIdentityState, err error) {
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := storage.ValidateControlGeneration(expected); err != nil {
		return result, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := s.lockStateMutation(); err != nil {
		return result, err
	}
	defer s.unlockStateMutation()
	if err := s.checkControlMutationJournalsLocked(); err != nil {
		return result, err
	}
	state, err := s.store.Load()
	if err != nil {
		return result, err
	}
	if err := validateStateMode(state); err != nil {
		return result, err
	}
	if state.EffectiveMode() != config.ModeController || state.Controller == nil || state.Controller.Control == nil {
		return result, errors.New("control identity is not prepared")
	}
	if err := s.recoverControlIdentityLocked(state); err != nil {
		return result, errors.New("control identity preparation requires offline inspection")
	}
	if err := s.recoverControlServerRotationLocked(state, now); err != nil {
		return result, errors.New("control server rotation requires offline inspection")
	}
	control := *state.Controller.Control
	if err := ValidateControlIdentityMetadata(&control, s.cfg.WebUIPort); err != nil {
		return result, err
	}
	if control.Enabled && (live == nil || s.controlOwner == nil || live != s.controlOwner.runtime) {
		return result, errors.New("enabled control rotation requires its runtime owner")
	}
	if control.ServerGeneration != expected {
		return result, errStaleControlServerGeneration
	}
	endpoint := controlpki.Endpoint{BindIP: control.BindIP, Advertised: control.Advertised, Port: control.Port}
	old, err := s.store.LoadControlIdentity(control.CAGeneration, control.ServerGeneration)
	if err != nil {
		return result, errors.New("committed control identity is unavailable")
	}
	if err := controlpki.ValidateForServerRotation(old, endpoint, control.CAPin, now); err != nil {
		return result, errors.New("committed control identity cannot rotate")
	}
	generation, err := newControlGeneration()
	if err != nil {
		return result, err
	}
	if err := s.store.CheckControlServerGeneration(generation); err != nil {
		return result, errors.New("control server generation is unsafe")
	}
	journal := storage.ControlServerRotationJournal{Version: 1, ControllerID: state.Controller.ControllerID, CAGeneration: control.CAGeneration, OldServerGeneration: expected, NewServerGeneration: generation, StartedAt: now}
	if err := s.store.SaveControlServerRotationJournal(journal, s.controlRotationStep); err != nil {
		return result, errors.New("record control server rotation failed")
	}
	candidate, err := controlpki.RotateServerLeaf(old, endpoint, control.CAPin, now)
	if err != nil {
		return result, errors.New("generate control server leaf failed")
	}
	if err := s.store.WriteControlServerGeneration(journal, candidate, s.controlRotationStep); err != nil {
		return result, errors.New("write control server generation failed")
	}
	if err := s.rotationStep("before-staged-validation"); err != nil {
		return result, err
	}
	control.ServerGeneration = generation
	if err := s.validateControlIdentityLocked(&control, now, false); err != nil {
		return result, errors.New("staged control server identity is invalid")
	}
	if err := s.rotationStep("after-staged-validation"); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := s.rotationStep("before-state-save"); err != nil {
		return result, err
	}
	// Save may rename successfully and then fail: from here onward never serve
	// the predecessor on error and never roll state back. Recovery rereads state.
	defer func() {
		if err != nil && live != nil {
			live.Close()
		}
	}()
	state.Controller.Control = &control
	if err := s.saveState(state); err != nil {
		return result, errors.New("commit control server generation failed")
	}
	if err := s.rotationStep("after-state-save"); err != nil {
		return result, err
	}
	if err := s.rotationStep("before-state-sync"); err != nil {
		return result, err
	}
	if err := s.store.SyncStateDirectory(); err != nil {
		return result, errors.New("sync control server generation failed")
	}
	if err := s.rotationStep("after-state-sync"); err != nil {
		return result, err
	}
	if err := s.rotationStep("before-committed-validation"); err != nil {
		return result, err
	}
	// Validate exactly the committed generation, not an in-memory stage.
	committed, err := s.store.Load()
	if err != nil {
		return result, errors.New("load committed control server identity failed")
	}
	if committed.Controller == nil || committed.Controller.ControllerID != journal.ControllerID || committed.Controller.Control == nil || *committed.Controller.Control != control {
		return result, errors.New("committed control server identity changed")
	}
	if err := s.validateControlIdentityLocked(&control, now, false); err != nil {
		return result, errors.New("committed control server identity is invalid")
	}
	if err := s.rotationStep("after-committed-validation"); err != nil {
		return result, err
	}
	if live != nil {
		if err := s.rotationStep("before-runtime-publication"); err != nil {
			return result, err
		}
		material, err := s.store.LoadControlIdentity(control.CAGeneration, generation)
		if err != nil {
			return result, errors.New("committed control server identity is unavailable")
		}
		if err := live.ReloadCommitted(material); err != nil {
			return result, errors.New("committed control server runtime publication failed")
		}
		if err := s.rotationStep("after-runtime-publication"); err != nil {
			return result, err
		}
	}
	if err := s.store.RemoveControlServerGeneration(journal, expected, s.controlRotationStep); err != nil {
		return result, errors.New("retire previous control server generation failed")
	}
	if err := s.store.DeleteControlServerRotationJournal(s.controlRotationStep); err != nil {
		return result, errors.New("control server rotation journal cleanup failed")
	}
	return control, nil
}

func (s *Service) rotationStep(name string) error {
	if s.controlRotationStep != nil {
		return s.controlRotationStep(name)
	}
	return nil
}

func (s *Service) checkControlMutationJournalsLocked() error {
	if err := s.store.CheckRestorePending(); err != nil {
		return errors.New("controller restore is pending")
	}
	if _, err := s.store.LoadPendingDesiredStateCommit(); !errors.Is(err, os.ErrNotExist) {
		return errors.New("desired-state commit requires recovery")
	}
	if _, err := s.store.LoadControllerActivationJournal(); !errors.Is(err, os.ErrNotExist) {
		return errors.New("controller activation requires recovery")
	}
	return nil
}

// recoverControlServerRotationLocked only reconciles journal-owned generations.
// Corruption keeps the journal and denies control work, independently of tunnels.
func (s *Service) recoverControlServerRotationLocked(state config.State, now time.Time) error {
	journal, err := s.store.LoadControlServerRotationJournal()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.checkControlMutationJournalsLocked(); err != nil {
		return err
	}
	if _, err := s.store.LoadControlIdentityJournal(); !errors.Is(err, os.ErrNotExist) {
		return errors.New("overlapping control identity transitions")
	}
	if state.EffectiveMode() != config.ModeController || state.Controller == nil || state.Controller.ControllerID != journal.ControllerID || state.Controller.Control == nil {
		return errors.New("control rotation journal controller mismatch")
	}
	control := state.Controller.Control
	if control.CAGeneration != journal.CAGeneration {
		return errors.New("control rotation journal CA mismatch")
	}
	if err := s.validateControlIdentityLocked(control, now, true); err != nil {
		return err
	}
	var remove string
	switch control.ServerGeneration {
	case journal.OldServerGeneration:
		remove = journal.NewServerGeneration
	case journal.NewServerGeneration:
		remove = journal.OldServerGeneration
	default:
		return errors.New("control rotation journal generation mismatch")
	}
	// Re-sync a possibly uncertain rename before retiring anything.
	if err := s.store.SyncStateDirectory(); err != nil {
		return err
	}
	if err := s.store.RemoveControlServerGeneration(journal, remove, nil); err != nil {
		return err
	}
	return s.store.DeleteControlServerRotationJournal(nil)
}
