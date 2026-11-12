package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/astronaut808/awg-forge/internal/controlserver"
	"github.com/astronaut808/awg-forge/internal/render"
	"github.com/astronaut808/awg-forge/internal/sqldb"
)

// Node-local rendering is a pure read under the existing mutation/boot lock.
// It never calls Init, marks delivery, repairs runtime, or writes history.
func (s *Service) RenderNodeClientConfig(ctx context.Context, expectedConnection config.NodeConnectionState, d controlapi.ClientConfigExportDelivery) ([]byte, error) {
	if d.Validate() != nil || !time.Now().Before(d.ExpiresAt) || d.ExpiresAt.After(time.Now().Add(controlapi.ClientConfigExportTTL)) {
		return nil, ErrClientConfigExportUnavailable
	}
	if err := s.lockControlRequest(ctx); err != nil {
		return nil, err
	}
	defer s.unlockStateMutation()
	if s.store.CheckNoNodeRecovery() != nil || s.store.CheckRestorePending() != nil {
		return nil, ErrClientConfigExportUnavailable
	}
	if _, err := s.store.LoadNodeIdentityJournal(); !errors.Is(err, os.ErrNotExist) {
		return nil, ErrClientConfigExportUnavailable
	}
	if _, err := s.store.LoadNodeRenewalJournal(); !errors.Is(err, os.ErrNotExist) {
		return nil, ErrClientConfigExportUnavailable
	}
	state, err := s.store.Load()
	if err != nil || state.EffectiveMode() != config.ModeNode || validateStateMode(state) != nil || !s.currentClientWorker(state) || state.NodeConnection == nil || *state.NodeConnection != expectedConnection || validateManagedNodeState(state.ManagedNode) != nil {
		return nil, ErrClientConfigExportUnavailable
	}
	p := d.Authority
	m := state.ManagedNode
	if m.StateEpoch != p.StateEpoch || m.BindingEpoch != p.BindingEpoch || m.DesiredGeneration != p.Generation || s.managedBoot.BootID != p.BootID || m.BootSequence != p.BootSequence {
		return nil, ErrClientConfigExportUnavailable
	}
	tunnel, client, ok := findClient(state, d.Request.ClientID)
	if !ok || tunnel.ID != d.Request.TunnelID || tunnel.ConfigRevision != d.Request.TunnelRevision {
		return nil, ErrClientConfigExportUnavailable
	}
	value, err := render.ClientConfig(state, tunnel, client)
	if err != nil || len(value) == 0 || len(value) > controlapi.MaxClientConfigBytes {
		return nil, ErrClientConfigExportUnavailable
	}
	return []byte(value), nil
}

func (s *Service) exportAuthorityLocked(ctx context.Context, node string, r controlapi.ClientConfigExportRequest) (sqldb.ClientConfigExportAuthority, error) {
	state, _, err := s.enrollmentStateLocked()
	if err != nil {
		return sqldb.ClientConfigExportAuthority{}, err
	}
	db, _, err := s.openControlRegistry(ctx)
	if err != nil {
		return sqldb.ClientConfigExportAuthority{}, err
	}
	defer func() { _ = db.Close() }()
	return db.ClientConfigExportAuthority(ctx, state.Controller.ControllerID, state.Controller.Control.CAGeneration, node, r, time.Now().UTC())
}

func (s *Service) PrepareClientConfigExport(ctx context.Context, token, node string, r controlapi.ClientConfigExportRequest) (controlapi.ClientConfigExport, error) {
	if !validControlUUID(node) || r.Validate() != nil {
		return controlapi.ClientConfigExport{}, ErrClientConfigExportUnavailable
	}
	if err := s.lockControlRequest(ctx); err != nil {
		return controlapi.ClientConfigExport{}, err
	}
	defer s.unlockStateMutation()
	digest, _, err := s.controlRecentSession(ctx, token)
	if err != nil {
		return controlapi.ClientConfigExport{}, err
	}
	authority, err := s.exportAuthorityLocked(ctx, node, r)
	if err != nil {
		return controlapi.ClientConfigExport{}, err
	}
	return s.clientConfigExports.create(node, r, digest, authority, time.Now().UTC())
}

// One consumer claims before waiting. No mutation lock is held while waiting.
// Cancellation/error after claim removes the job, including its secret buffer.
func (s *Service) DownloadClientConfigExport(ctx context.Context, token, node, id string) ([]byte, string, error) {
	job, err := s.claimClientConfigExport(ctx, token, node, id)
	if err != nil {
		return nil, "", err
	}
	defer s.clientConfigExports.cancel(job)
	select {
	case <-ctx.Done():
		return nil, "", ctx.Err()
	case <-job.done:
	}
	if err := s.lockControlRequest(ctx); err != nil {
		return nil, "", err
	}
	defer s.unlockStateMutation()
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	digest, _, err := s.controlRecentSession(ctx, token)
	if err != nil {
		return nil, "", err
	}
	authority, err := s.exportAuthorityLocked(ctx, node, job.request)
	if err != nil || authority != job.authority {
		return nil, "", ErrClientConfigExportUnavailable
	}
	value, err := s.clientConfigExports.consume(job, digest)
	return value, "client-" + job.request.ClientID + ".conf", err
}

func (s *Service) claimClientConfigExport(ctx context.Context, token, node, id string) (*clientConfigExportJob, error) {
	if !validControlUUID(node) || !validControlUUID(id) {
		return nil, ErrClientConfigExportUnavailable
	}
	if err := s.lockControlRequest(ctx); err != nil {
		return nil, err
	}
	defer s.unlockStateMutation()
	digest, _, err := s.controlRecentSession(ctx, token)
	if err != nil {
		return nil, err
	}
	job, err := s.clientConfigExports.snapshot(node, id)
	if err != nil || job.owner != digest {
		return nil, ErrClientConfigExportUnavailable
	}
	authority, err := s.exportAuthorityLocked(ctx, node, job.request)
	if err != nil || authority != job.authority {
		s.clientConfigExports.cancel(job)
		return nil, ErrClientConfigExportUnavailable
	}
	if err := s.clientConfigExports.claim(job, digest); err != nil {
		return nil, err
	}
	return job, nil
}

func (s *Service) CancelClientConfigExport(ctx context.Context, token, node, id string) error {
	if !validControlUUID(node) || !validControlUUID(id) {
		return ErrClientConfigExportUnavailable
	}
	if err := s.lockControlRequest(ctx); err != nil {
		return err
	}
	defer s.unlockStateMutation()
	digest, _, err := s.controlRecentSession(ctx, token)
	if err != nil {
		return err
	}
	job, err := s.clientConfigExports.snapshot(node, id)
	if err != nil {
		return nil
	}
	if job.owner != digest {
		return ErrClientConfigExportUnavailable
	}
	s.clientConfigExports.cancel(job)
	return nil
}

func (s *Service) exportOwnerActive(ctx context.Context, db *sqldb.DB, job *clientConfigExportJob) error {
	now := time.Now().UTC()
	session, err := db.FindControllerSession(ctx, job.owner, now)
	recentTTL := s.controllerAuthOptions.RecentAuthTTL
	if recentTTL <= 0 {
		recentTTL = controlauth.DefaultRecentAuthTTL
	}
	if err != nil || session.AuthenticatedAt.IsZero() || now.Before(session.AuthenticatedAt) || now.Sub(session.AuthenticatedAt) > recentTTL {
		return ErrClientConfigExportUnavailable
	}
	return nil
}

func (s *Service) validateNodeExport(ctx context.Context, db *sqldb.DB, identity controlserver.NodeIdentity, issuer, serial string, p controlapi.OperationPoll, job *clientConfigExportJob) error {
	if identity.NodeID != job.metadata.NodeID || identity.BindingEpoch != job.request.BindingEpoch || p != job.authority.Poll || serial != job.authority.Serial {
		return ErrClientConfigExportUnavailable
	}
	if err := s.exportOwnerActive(ctx, db, job); err != nil {
		return err
	}
	authority, err := db.ClientConfigExportAuthority(ctx, identity.ControllerID, issuer, identity.NodeID, job.request, time.Now().UTC())
	if err != nil || authority != job.authority {
		return ErrClientConfigExportUnavailable
	}
	return nil
}

func readClientConfigExportJSON(w http.ResponseWriter, r *http.Request, value any, limit int64) error {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	defer clear(raw)
	if err != nil {
		return ErrClientConfigExportUnavailable
	}
	return controlapi.DecodeClientConfigExport(raw, value)
}

func (s *Service) nodeClientConfigExportPollHTTP(w http.ResponseWriter, r *http.Request) {
	var p controlapi.OperationPoll
	if readClientConfigExportJSON(w, r, &p, controlapi.MaxOperationBytes) != nil || p.Validate() != nil || p.WaitSeconds != 0 {
		controlJSON(w, 400, map[string]any{"status": 400, "code": "invalid_export_poll"})
		return
	}
	var delivery *controlapi.ClientConfigExportDelivery
	err := s.withRefreshRegistry(r, func(ctx context.Context, db *sqldb.DB, identity controlserver.NodeIdentity, issuer, serial string) error {
		job := s.clientConfigExports.pending(identity.NodeID)
		if job == nil {
			return nil
		}
		if err := s.validateNodeExport(ctx, db, identity, issuer, serial, p, job); err != nil {
			s.clientConfigExports.cancel(job)
			return err
		}
		var err error
		delivery, err = s.clientConfigExports.deliver(job)
		return err
	})
	if err != nil {
		refreshProblem(w, sqldb.ErrOperationFenced)
		return
	}
	if delivery == nil {
		w.WriteHeader(204)
		return
	}
	controlJSON(w, 200, delivery)
}

func (s *Service) nodeClientConfigExportResultHTTP(w http.ResponseWriter, r *http.Request) {
	var result controlapi.ClientConfigExportResult
	if readClientConfigExportJSON(w, r, &result, controlapi.MaxClientConfigResultBytes) != nil || result.Validate() != nil {
		controlJSON(w, 400, map[string]any{"status": 400, "code": "invalid_export_result"})
		return
	}
	defer func() { result.Config = "" }()
	err := s.withRefreshRegistry(r, func(ctx context.Context, db *sqldb.DB, identity controlserver.NodeIdentity, issuer, serial string) error {
		job, err := s.clientConfigExports.snapshot(identity.NodeID, result.ExportID)
		if err != nil {
			return err
		}
		if err := s.validateNodeExport(ctx, db, identity, issuer, serial, result.Authority, job); err != nil {
			s.clientConfigExports.cancel(job)
			return err
		}
		return s.clientConfigExports.complete(job, result)
	})
	if err != nil {
		refreshProblem(w, sqldb.ErrOperationFenced)
		return
	}
	w.WriteHeader(204)
}
