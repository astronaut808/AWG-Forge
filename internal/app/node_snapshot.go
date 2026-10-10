package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlserver"
	"github.com/astronaut808/awg-forge/internal/sqldb"
)

func (s *Service) nodeSnapshotHTTP(w http.ResponseWriter, r *http.Request) {
	identity, ok := controlserver.IdentityFromContext(r.Context())
	if !ok {
		controlProblem(w, sqldb.ErrEnrollmentDenied)
		return
	}
	var request controlapi.Snapshot
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, controlapi.MaxSnapshotBytes))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&request)
	var extra any
	if err != nil || !errors.Is(decoder.Decode(&extra), io.EOF) || request.Validate() != nil {
		controlJSON(w, 400, map[string]any{"type": "about:blank", "title": "Invalid snapshot", "status": 400, "code": "invalid_snapshot"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.lockControlRequest(ctx); err != nil {
		controlProblem(w, err)
		return
	}
	defer s.unlockStateMutation()
	state, _, err := s.enrollmentStateLocked()
	if err != nil || state.Controller.ControllerID != identity.ControllerID {
		controlProblem(w, sqldb.ErrEnrollmentDenied)
		return
	}
	db, _, err := s.openControlRegistry(ctx)
	if err != nil {
		controlProblem(w, err)
		return
	}
	defer func() { _ = db.Close() }()
	now := time.Now().UTC()
	if _, err := db.FindActiveNodeCertificate(ctx, identity.ControllerID, state.Controller.Control.CAGeneration, r.TLS.PeerCertificates[0], now); err != nil {
		if errors.Is(err, sqldb.ErrNodeCertificateDenied) {
			controlJSON(w, 403, map[string]any{"type": "about:blank", "title": "Forbidden", "status": 403, "code": "node_denied"})
		} else {
			controlProblem(w, err)
		}
		return
	}
	err = db.AcceptNodeSnapshot(ctx, sqldb.NodeIdentity{ControllerID: identity.ControllerID, NodeID: identity.NodeID, BindingEpoch: identity.BindingEpoch}, state.Controller.Control.CAGeneration, r.TLS.PeerCertificates[0].SerialNumber.String(), request, now)
	if errors.Is(err, sqldb.ErrSnapshotFenced) {
		controlJSON(w, 409, map[string]any{"type": "about:blank", "title": "Snapshot fenced", "status": 409, "code": "snapshot_fenced"})
		return
	}
	if err != nil {
		controlProblem(w, err)
		return
	}
	controlJSON(w, 200, controlapi.SnapshotAccepted{Sequence: request.Sequence, ReceivedAt: now})
}

// NodeObservationState captures committed state without Init, rendering,
// history writes or default tunnel creation. Runtime collection happens after
// releasing the mutation lock; the snapshot identifies this captured generation.
func (s *Service) NodeObservationState(ctx context.Context) (config.State, error) {
	if err := s.lockControlRequest(ctx); err != nil {
		return config.State{}, err
	}
	defer s.unlockStateMutation()
	state, err := s.store.Load()
	if err != nil {
		return config.State{}, err
	}
	if state.EffectiveMode() != config.ModeNode || state.ManagedNode == nil || state.NodeConnection == nil || validateManagedNodeState(state.ManagedNode) != nil {
		return config.State{}, errors.New("node observation unavailable")
	}
	return state, nil
}

// Controller reads stay in the application boundary and hold the same lock as
// revocation/restore, so a read cannot race authority changes within this app.
func (s *Service) ControllerNodes(ctx context.Context) (controlapi.NodeInventory, error) {
	if err := s.lockControlRequest(ctx); err != nil {
		return controlapi.NodeInventory{}, err
	}
	defer s.unlockStateMutation()
	state, _, err := s.enrollmentStateLocked()
	if err != nil {
		return controlapi.NodeInventory{}, err
	}
	db, _, err := s.openControlRegistry(ctx)
	if err != nil {
		return controlapi.NodeInventory{}, err
	}
	defer func() { _ = db.Close() }()
	return db.NodeInventory(ctx, state.Controller.ControllerID, state.Controller.Control.CAGeneration, "", time.Now().UTC())
}
func (s *Service) ControllerNodeProjection(ctx context.Context, nodeID string) (controlapi.NodeProjection, error) {
	if !validControlUUID(nodeID) {
		return controlapi.NodeProjection{}, errors.New("invalid node identity")
	}
	if err := s.lockControlRequest(ctx); err != nil {
		return controlapi.NodeProjection{}, err
	}
	defer s.unlockStateMutation()
	state, _, err := s.enrollmentStateLocked()
	if err != nil {
		return controlapi.NodeProjection{}, err
	}
	db, _, err := s.openControlRegistry(ctx)
	if err != nil {
		return controlapi.NodeProjection{}, err
	}
	defer func() { _ = db.Close() }()
	return db.NodeProjection(ctx, state.Controller.ControllerID, state.Controller.Control.CAGeneration, nodeID, time.Now().UTC())
}
