package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlserver"
	"github.com/astronaut808/awg-forge/internal/sqldb"
)

// Browser authentication and same-origin admission are enforced by the browser
// boundary. Authority/storage transitions belong here under the existing lock.
func (s *Service) CreateNodeRefresh(ctx context.Context, node string, r controlapi.RefreshRequest) (controlapi.OperationStatus, error) {
	if !validControlUUID(node) || r.Validate() != nil {
		return controlapi.OperationStatus{}, sqldb.ErrOperationFenced
	}
	if err := s.lockControlRequest(ctx); err != nil {
		return controlapi.OperationStatus{}, err
	}
	defer s.unlockStateMutation()
	state, _, err := s.enrollmentStateLocked()
	if err != nil {
		return controlapi.OperationStatus{}, err
	}
	db, _, err := s.openControlRegistry(ctx)
	if err != nil {
		return controlapi.OperationStatus{}, err
	}
	defer func() { _ = db.Close() }()
	return db.CreateRefresh(ctx, state.Controller.ControllerID, state.Controller.Control.CAGeneration, node, r, time.Now().UTC())
}

func (s *Service) CreateNodeClientEnabled(ctx context.Context, node string, r controlapi.ClientEnabledRequest) (controlapi.OperationStatus, error) {
	if !validControlUUID(node) || r.Validate() != nil {
		return controlapi.OperationStatus{}, sqldb.ErrOperationFenced
	}
	if err := s.lockControlRequest(ctx); err != nil {
		return controlapi.OperationStatus{}, err
	}
	defer s.unlockStateMutation()
	state, _, err := s.enrollmentStateLocked()
	if err != nil {
		return controlapi.OperationStatus{}, err
	}
	db, _, err := s.openControlRegistry(ctx)
	if err != nil {
		return controlapi.OperationStatus{}, err
	}
	defer func() { _ = db.Close() }()
	return db.CreateClientEnabled(ctx, state.Controller.ControllerID, state.Controller.Control.CAGeneration, node, r, time.Now().UTC())
}
func (s *Service) NodeRefreshStatus(ctx context.Context, node, operation string) (controlapi.OperationStatus, error) {
	if !validControlUUID(node) || !validControlUUID(operation) {
		return controlapi.OperationStatus{}, sqldb.ErrOperationFenced
	}
	if err := s.lockControlRequest(ctx); err != nil {
		return controlapi.OperationStatus{}, err
	}
	defer s.unlockStateMutation()
	state, _, err := s.enrollmentStateLocked()
	if err != nil {
		return controlapi.OperationStatus{}, err
	}
	db, _, err := s.openControlRegistry(ctx)
	if err != nil {
		return controlapi.OperationStatus{}, err
	}
	defer func() { _ = db.Close() }()
	return db.RefreshStatus(ctx, state.Controller.ControllerID, node, operation, time.Now().UTC())
}
func readOperationJSON(w http.ResponseWriter, r *http.Request, value any) error {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, controlapi.MaxOperationBytes))
	if err != nil {
		return err
	}
	return controlapi.DecodeOperation(raw, value)
}
func refreshProblem(w http.ResponseWriter, err error) {
	status, code, title := 503, "control_unavailable", "Control unavailable"
	switch {
	case errors.Is(err, sqldb.ErrNodeCertificateDenied):
		status, code, title = 403, "node_denied", "Node denied"
	case errors.Is(err, sqldb.ErrOperationFenced):
		status, code, title = 409, "operation_fenced", "Operation fenced"
	case errors.Is(err, sqldb.ErrOperationLimit):
		status, code, title = 429, "operation_limit", "Refresh limit reached"
		w.Header().Set("Retry-After", "5")
	case errors.Is(err, sqldb.ErrOperationUnsupported):
		status, code, title = 409, "refresh_unsupported", "Refresh unsupported"
	case errors.Is(err, sqldb.ErrOperationOffline):
		status, code, title = 409, "node_unavailable", "Node unavailable"
	}
	controlJSON(w, status, map[string]any{"type": "about:blank", "title": title, "status": status, "code": code})
}
func (s *Service) withRefreshRegistry(r *http.Request, fn func(context.Context, *sqldb.DB, controlserver.NodeIdentity, string, string) error) error {
	id, ok := controlserver.IdentityFromContext(r.Context())
	if !ok || r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return sqldb.ErrOperationFenced
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.lockControlRequest(ctx); err != nil {
		return err
	}
	defer s.unlockStateMutation()
	state, _, err := s.enrollmentStateLocked()
	if err != nil {
		return err
	}
	if state.Controller.ControllerID != id.ControllerID {
		return sqldb.ErrOperationFenced
	}
	db, _, err := s.openControlRegistry(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if _, err = db.FindActiveNodeCertificate(ctx, id.ControllerID, state.Controller.Control.CAGeneration, r.TLS.PeerCertificates[0], time.Now()); err != nil {
		return err
	}
	return fn(ctx, db, id, state.Controller.Control.CAGeneration, r.TLS.PeerCertificates[0].SerialNumber.String())
}
func (s *Service) nodeOperationPollHTTP(w http.ResponseWriter, r *http.Request) {
	var p controlapi.OperationPoll
	if readOperationJSON(w, r, &p) != nil || p.Validate() != nil {
		controlJSON(w, 400, map[string]any{"type": "about:blank", "title": "Invalid poll", "status": 400, "code": "invalid_poll"})
		return
	}
	deadline := time.NewTimer(time.Duration(p.WaitSeconds) * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		var delivery *controlapi.OperationDelivery
		err := s.withRefreshRegistry(r, func(ctx context.Context, db *sqldb.DB, id controlserver.NodeIdentity, issuer, serial string) error {
			var err error
			delivery, err = db.LeaseRefresh(ctx, sqldb.NodeIdentity{ControllerID: id.ControllerID, NodeID: id.NodeID, BindingEpoch: id.BindingEpoch}, issuer, serial, p, time.Now().UTC())
			return err
		})
		if err != nil {
			refreshProblem(w, err)
			return
		}
		if delivery != nil {
			controlJSON(w, 200, delivery)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			w.WriteHeader(204)
			return
		case <-ticker.C:
		}
	}
}
func (s *Service) nodeOperationResultHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) != 7 || !validControlUUID(parts[5]) {
		http.NotFound(w, r)
		return
	}
	var receipt controlapi.OperationReceipt
	if readOperationJSON(w, r, &receipt) != nil || receipt.Validate() != nil {
		controlJSON(w, 400, map[string]any{"type": "about:blank", "title": "Invalid result", "status": 400, "code": "invalid_result"})
		return
	}
	err := s.withRefreshRegistry(r, func(ctx context.Context, db *sqldb.DB, id controlserver.NodeIdentity, issuer, serial string) error {
		return db.CompleteRefresh(ctx, sqldb.NodeIdentity{ControllerID: id.ControllerID, NodeID: id.NodeID, BindingEpoch: id.BindingEpoch}, issuer, serial, parts[5], receipt, time.Now().UTC())
	})
	if err != nil {
		refreshProblem(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Service) nodeClientResultAckHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) != 7 || !validControlUUID(parts[5]) {
		http.NotFound(w, r)
		return
	}
	var ack controlapi.ClientResultAck
	if readOperationJSON(w, r, &ack) != nil || ack.Validate() != nil {
		controlJSON(w, 400, map[string]any{"status": 400, "code": "invalid_ack"})
		return
	}
	err := s.withRefreshRegistry(r, func(ctx context.Context, db *sqldb.DB, id controlserver.NodeIdentity, issuer, serial string) error {
		return db.AcknowledgeClientResult(ctx, sqldb.NodeIdentity{ControllerID: id.ControllerID, NodeID: id.NodeID, BindingEpoch: id.BindingEpoch}, issuer, serial, parts[5], ack, time.Now().UTC())
	})
	if err != nil {
		refreshProblem(w, err)
		return
	}
	w.WriteHeader(204)
}
