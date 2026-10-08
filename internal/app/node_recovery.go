package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/google/uuid"
)

// NodeRecoverySession is an offline lease and captured local authorization.
// Only a Linux-root CLI adapter exposes it; no browser or controller route does.
type NodeRecoverySession struct {
	service    *Service
	lease      *storage.StateLock
	managed    config.ManagedNodeState
	connection config.NodeConnectionState
}

func (s *Service) BeginNodeRecovery(ctx context.Context, nodeID, controllerID string) (*NodeRecoverySession, error) {
	if !validControlUUID(nodeID) || !validControlUUID(controllerID) {
		return nil, errors.New("canonical node and controller confirmation required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.store.CheckNodeRecoveryDirectory(); err != nil {
		return nil, err
	}
	lease, err := storage.AcquireStateLock(s.cfg.ConfigDir)
	if err != nil {
		return nil, errors.New("node recovery requires a stopped server")
	}
	keep := false
	defer func() {
		if !keep {
			_ = lease.Close()
		}
	}()
	if err := s.lockControlRequest(ctx); err != nil {
		return nil, err
	}
	defer s.unlockStateMutation()
	state, err := s.nodeRecoveryStateLocked()
	if err != nil {
		return nil, err
	}
	if state.ManagedNode.NodeID != nodeID || state.ManagedNode.ControllerID != controllerID {
		return nil, errors.New("node recovery identity confirmation differs")
	}
	if s.nodeRecovery != nil {
		return nil, errors.New("node recovery already active")
	}
	r := &NodeRecoverySession{service: s, lease: lease, managed: *state.ManagedNode, connection: *state.NodeConnection}
	s.nodeRecovery = r
	keep = true
	return r, nil
}

func (r *NodeRecoverySession) Close() error {
	r.service.mu.Lock()
	defer r.service.mu.Unlock()
	if r.lease == nil {
		return nil
	}
	err := r.lease.Close()
	r.lease = nil
	if r.service.nodeRecovery == r {
		r.service.nodeRecovery = nil
	}
	return err
}

func (r *NodeRecoverySession) BootID() (string, error) { return r.service.BootID() }

func (r *NodeRecoverySession) stateLocked() (config.State, error) {
	if r.lease == nil || r.service.nodeRecovery != r {
		return config.State{}, errors.New("node recovery authorization ended")
	}
	state, err := r.service.nodeRecoveryStateLocked()
	if err != nil {
		return state, err
	}
	if !reflect.DeepEqual(*state.ManagedNode, r.managed) || *state.NodeConnection != r.connection {
		return config.State{}, errors.New("node recovery binding changed")
	}
	return state, nil
}

func (r *NodeRecoverySession) PreflightNodeEnrollment(ctx context.Context) error {
	if err := r.service.lockControlRequest(ctx); err != nil {
		return err
	}
	defer r.service.unlockStateMutation()
	_, err := r.stateLocked()
	return err
}

func (s *Service) nodeRecoveryStateLocked() (config.State, error) {
	if err := s.store.CheckRestorePending(); err != nil {
		return config.State{}, err
	}
	if err := s.store.CheckNoNodeRecovery(); err != nil {
		return config.State{}, err
	}
	if err := s.checkNodeRecoveryOverlapLocked(); err != nil {
		return config.State{}, err
	}
	state, err := s.store.Load()
	if err != nil || validateStateMode(state) != nil || state.EffectiveMode() != config.ModeNode || state.ManagedNode == nil || state.NodeConnection == nil || ValidateNodeConnection(state.NodeConnection) != nil || validateManagedNodeState(state.ManagedNode) != nil {
		return config.State{}, errors.New("existing managed node required")
	}
	if j, err := s.store.LoadNodeRenewalJournal(); !errors.Is(err, os.ErrNotExist) {
		if err != nil || !nodeRenewalMatches(state, j) || j.PredecessorGeneration != state.NodeConnection.CredentialGeneration {
			return config.State{}, errors.New("node renewal requires inspection or completion before recovery")
		}
		candidate, e := s.store.LoadNodeRenewalCandidate(j.Generation)
		if e != nil || validateRenewalCandidate(candidate) != nil {
			return config.State{}, errors.New("node renewal requires inspection before recovery")
		}
	}
	return state, nil
}

func (s *Service) checkNodeRecoveryOverlapLocked() error {
	for _, load := range []func() error{
		func() error { _, e := s.store.LoadPendingDesiredStateCommit(); return e },
		func() error { _, e := s.store.LoadControllerActivationJournal(); return e },
		func() error { _, e := s.store.LoadControlIdentityJournal(); return e },
		func() error { _, e := s.store.LoadNodeIdentityJournal(); return e },
	} {
		if err := load(); !errors.Is(err, os.ErrNotExist) {
			return errors.New("node recovery has an overlapping transition")
		}
	}
	if err := s.store.CheckNoControlServerRotation(); err != nil {
		return err
	}
	return nil
}

func (r *NodeRecoverySession) Detach(ctx context.Context) error {
	s := r.service
	if err := s.lockControlRequest(ctx); err != nil {
		return err
	}
	defer s.unlockStateMutation()
	old, err := r.stateLocked()
	if err != nil {
		return err
	}
	next := old
	next.Mode = config.ModeStandalone
	next.ManagedNode = nil
	next.NodeConnection = nil
	next.UpdatedAt = time.Now().UTC()
	return s.commitNodeRecoveryLocked(ctx, old, next, storage.NodeIdentityMaterial{})
}

// InstallNodeEnrollment completes a fresh approved binding without reopening
// an archived node identity. Decline/cancellation before commit preserves old state.
func (r *NodeRecoverySession) InstallNodeEnrollment(ctx context.Context, invitation controlapi.Invitation, approved controlapi.EnrollmentStatus, key []byte) error {
	s := r.service
	if err := s.lockControlRequest(ctx); err != nil {
		return err
	}
	defer s.unlockStateMutation()
	old, err := r.stateLocked()
	if err != nil {
		return err
	}
	if err := validateNodeApproval(invitation, approved, key); err != nil {
		return err
	}
	if approved.NodeID == old.ManagedNode.NodeID {
		return errors.New("rebind requires a fresh approved node identity")
	}
	generation, err := uuid.NewRandom()
	if err != nil {
		return err
	}
	epoch, err := uuid.NewRandom()
	if err != nil {
		return err
	}
	next := old
	next.ManagedNode = &config.ManagedNodeState{NodeID: approved.NodeID, ControllerID: approved.ControllerID, BindingEpoch: approved.BindingEpoch, StateEpoch: epoch.String()}
	next.NodeConnection = &config.NodeConnectionState{ControllerURL: invitation.ControllerURL, CAPin: invitation.CAPin, CredentialGeneration: strings.ReplaceAll(generation.String(), "-", "")}
	next.UpdatedAt = time.Now().UTC()
	m := storage.NodeIdentityMaterial{CACert: []byte(invitation.CACertPEM), Certificate: []byte(approved.Certificate.CertificatePEM), PrivateKey: key}
	return s.commitNodeRecoveryLocked(ctx, old, next, m)
}

func nodeRecoveryStateHash(value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func (s *Service) recoveryStep(point string) error {
	if s.nodeRecoveryStep != nil {
		return s.nodeRecoveryStep(point)
	}
	return nil
}

func (s *Service) commitNodeRecoveryLocked(ctx context.Context, old, next config.State, material storage.NodeIdentityMaterial) error {
	oldHash, err := nodeRecoveryStateHash(old)
	if err != nil {
		return err
	}
	newHash, err := nodeRecoveryStateHash(next)
	if err != nil {
		return err
	}
	j := storage.NodeRecoveryJournal{Version: 1, OldStateHash: oldHash, NewStateHash: newHash, OldGeneration: old.NodeConnection.CredentialGeneration}
	if next.NodeConnection != nil {
		j.NewGeneration = next.NodeConnection.CredentialGeneration
		if err := s.store.CheckNodeGenerationAbsent(j.NewGeneration); err != nil {
			return err
		}
	}
	if renewal, err := s.store.LoadNodeRenewalJournal(); err == nil {
		j.RenewalGeneration = renewal.Generation
		j.RenewalJournalHash, err = nodeRecoveryStateHash(renewal)
		if err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := s.store.SaveNodeRecoveryJournal(j, s.nodeRecoveryStep); err != nil {
		return errors.New("node recovery journal publication failed")
	}
	if j.NewGeneration != "" {
		if err := s.store.SaveNodeIdentity(j.NewGeneration, material); err != nil {
			return errors.New("stage fresh node identity failed")
		}
		if err := ValidateNodeIdentity(next.NodeConnection, material, false); err != nil {
			return err
		}
	}
	if err := s.recoveryStep("staged"); err != nil {
		return err
	}
	if err := validateStateMode(next); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.recoveryStep("before-state-save"); err != nil {
		return err
	}
	if err := s.saveState(next); err != nil {
		return errors.New("node recovery state commit uncertain; restart for inspection")
	}
	if err := s.recoveryStep("after-state-save"); err != nil {
		return err
	}
	if err := s.store.SyncStateDirectory(); err != nil {
		return err
	}
	committed, err := s.store.Load()
	if err != nil || !reflect.DeepEqual(committed, next) {
		return errors.New("node recovery state readback differs")
	}
	if err := s.recoveryStep("committed"); err != nil {
		return err
	}
	s.managedBoot = nil
	return s.recoverNodeRecoveryLocked()
}

// Recovery makes no state replacement. It proves old or new full state and
// either removes the staged candidate or retires recorded predecessor material.
func (s *Service) recoverNodeRecoveryLocked() error {
	j, err := s.store.LoadNodeRecoveryJournal()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("node local recovery requires inspection")
	}
	if err := s.store.CheckRestorePending(); err != nil {
		return err
	}
	if err := s.checkNodeRecoveryOverlapLocked(); err != nil {
		return err
	}
	renewal, renewalErr := s.store.LoadNodeRenewalJournal()
	if !errors.Is(renewalErr, os.ErrNotExist) {
		if renewalErr != nil || j.RenewalGeneration == "" {
			return errors.New("node recovery renewal evidence differs")
		}
		hash, err := nodeRecoveryStateHash(renewal)
		if err != nil || hash != j.RenewalJournalHash {
			return errors.New("node recovery renewal evidence differs")
		}
	}
	state, err := s.store.Load()
	if err != nil || validateStateMode(state) != nil {
		return errors.New("node recovery state unavailable")
	}
	hash, err := nodeRecoveryStateHash(state)
	if err != nil {
		return err
	}
	if hash != j.OldStateHash && hash != j.NewStateHash {
		return errors.New("node recovery state differs; evidence retained")
	}
	if err := s.store.SyncStateDirectory(); err != nil {
		return err
	}
	if hash == j.OldStateHash {
		if j.RenewalGeneration != "" && errors.Is(renewalErr, os.ErrNotExist) {
			return errors.New("node recovery predecessor renewal evidence missing")
		}
		if state.NodeConnection == nil || state.NodeConnection.CredentialGeneration != j.OldGeneration {
			return errors.New("node recovery predecessor differs")
		}
		if j.NewGeneration != "" {
			if err := s.store.RetireNodeGeneration(j.NewGeneration, false); err != nil {
				return err
			}
		}
	} else {
		if j.NewGeneration == "" {
			if state.EffectiveMode() != config.ModeStandalone || state.ManagedNode != nil || state.NodeConnection != nil {
				return errors.New("detached node state differs")
			}
		} else {
			if state.NodeConnection == nil || state.NodeConnection.CredentialGeneration != j.NewGeneration {
				return errors.New("rebound node generation differs")
			}
			m, err := s.store.LoadNodeIdentity(j.NewGeneration)
			if err != nil || ValidateNodeIdentity(state.NodeConnection, m, true) != nil {
				return errors.New("rebound node identity unavailable")
			}
		}
		if err := s.recoveryStep("before-retirement"); err != nil {
			return err
		}
		if err := s.store.RetireNodeGeneration(j.OldGeneration, false); err != nil {
			return err
		}
		if j.RenewalGeneration != "" {
			if err := s.store.RetireNodeGeneration(j.RenewalGeneration, true); err != nil {
				return err
			}
			if err := s.store.RetireNodeGeneration(j.RenewalGeneration, false); err != nil {
				return err
			}
			if renewal, err := s.store.LoadNodeRenewalJournal(); err == nil {
				if renewal.Generation != j.RenewalGeneration || renewal.PredecessorGeneration != j.OldGeneration {
					return errors.New("node recovery renewal evidence differs")
				}
				if err := s.store.DeleteNodeRenewalJournal(); err != nil {
					return err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		s.managedBoot = nil
	}
	if err := s.recoveryStep("before-journal-cleanup"); err != nil {
		return err
	}
	return s.store.DeleteNodeRecoveryJournal()
}
