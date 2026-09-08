package app

import (
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/google/uuid"
)

var (
	// ErrManagedNodeRestoreIdentityConflict indicates that restore would cross
	// a controller identity boundary without explicit local authorization.
	ErrManagedNodeRestoreIdentityConflict = errors.New("managed-node restore identity conflict")
	processBootID, processBootIDErr       = newBootID()
)

func newBootID() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("generate process boot ID: %w", err)
	}
	return id.String(), nil
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
