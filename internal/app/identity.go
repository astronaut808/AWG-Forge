package app

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/google/uuid"
)

var (
	// ErrManagedNodeRestoreIdentityConflict indicates that restore would cross
	// a controller identity boundary without explicit local authorization.
	ErrManagedNodeRestoreIdentityConflict = errors.New("managed-node restore identity conflict")
	ErrBootSequenceExhausted              = errors.New("managed-node boot sequence exhausted")
	processBootID, processBootIDErr       = newBootID()
)

// ManagedNodeBoot identifies one control-agent process start. Sequence is
// persisted so the controller can reject a delayed presence from an older
// process even when network delivery is reordered.
type ManagedNodeBoot struct {
	BootID       string
	BootSequence uint64
	StateEpoch   string
}

func newBootID() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("generate process boot ID: %w", err)
	}
	return id.String(), nil
}

// StartManagedNodeBoot atomically allocates the persisted process-start
// sequence. Repeated calls on the same Service return the same identity; the
// control agent calls it once before its first presence request.
func (s *Service) StartManagedNodeBoot() (ManagedNodeBoot, error) {
	if processBootIDErr != nil {
		return ManagedNodeBoot{}, processBootIDErr
	}
	if err := s.lockStateMutation(); err != nil {
		return ManagedNodeBoot{}, err
	}
	defer s.unlockStateMutation()

	state, err := s.initLocked()
	if err != nil {
		return ManagedNodeBoot{}, err
	}
	if err := validateManagedNodeState(state.ManagedNode); err != nil {
		return ManagedNodeBoot{}, err
	}
	if s.managedBoot != nil {
		if s.managedBoot.StateEpoch != state.ManagedNode.StateEpoch {
			return ManagedNodeBoot{}, ErrStateEpochMismatch
		}
		return *s.managedBoot, nil
	}
	if state.ManagedNode.BootSequence == math.MaxUint64 {
		return ManagedNodeBoot{}, ErrBootSequenceExhausted
	}
	state.ManagedNode.BootSequence++
	state.UpdatedAt = time.Now().UTC()
	if err := s.store.Save(state); err != nil {
		return ManagedNodeBoot{}, err
	}
	boot := ManagedNodeBoot{
		BootID:       processBootID,
		BootSequence: state.ManagedNode.BootSequence,
		StateEpoch:   state.ManagedNode.StateEpoch,
	}
	s.managedBoot = &boot
	return boot, nil
}

// PrepareRestoredState fences controller identity across backup restores. An
// unchanged managed identity may be restored in place. Every other transition
// requires an explicit detach, which preserves local configuration while
// removing controller authority and replay metadata.
func PrepareRestoredState(current *config.State, restored config.State, detachManagedNode bool, now time.Time) (config.State, bool, error) {
	if restored.ManagedNode != nil {
		if err := validateManagedNodeState(restored.ManagedNode); err != nil {
			return config.State{}, false, err
		}
	}
	if current != nil && current.ManagedNode != nil {
		if err := validateManagedNodeState(current.ManagedNode); err != nil {
			return config.State{}, false, err
		}
	}

	currentManaged := current != nil && current.ManagedNode != nil
	restoredManaged := restored.ManagedNode != nil
	identitiesMatch := currentManaged && restoredManaged && reflect.DeepEqual(current.ManagedNode, restored.ManagedNode)
	if !currentManaged && !restoredManaged {
		return restored, false, nil
	}
	if identitiesMatch && !detachManagedNode {
		return restored, false, nil
	}
	if !detachManagedNode {
		return config.State{}, false, ErrManagedNodeRestoreIdentityConflict
	}

	restored.ManagedNode = nil
	if now.IsZero() {
		now = time.Now().UTC()
	}
	restored.UpdatedAt = now.UTC()
	return restored, true, nil
}

// ValidateManagedNodeState validates persisted controller fencing metadata.
func ValidateManagedNodeState(managed *config.ManagedNodeState) error {
	return validateManagedNodeState(managed)
}
