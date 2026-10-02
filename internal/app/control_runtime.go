package app

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/controlserver"
	"github.com/astronaut808/awg-forge/internal/sqldb"
)

// ControlEnableReceipt is an opaque, process-local capability. It cannot be
// serialized into state or reused by a new Service after restart.
type ControlEnableReceipt struct{ nonce [32]byte }

type controlEnableAuthorization struct {
	receipt      *ControlEnableReceipt
	controllerID string
	identity     config.ControlIdentityState
	session      controlauth.Digest
	expiresAt    time.Time
}

// controlRuntimeOwner is owned by Service.mu. The serving goroutine writes only
// its result under resultMu and closes done after releasing the database.
type controlRuntimeOwner struct {
	runtime  *controlserver.Runtime
	db       *sqldb.DB
	cancel   context.CancelFunc
	done     chan struct{}
	resultMu sync.Mutex
	result   error
}

// PrepareControlEnable is a trusted internal backup adapter, not an HTTP
// operation. createAndVerify must create a NEW encrypted archive of exactly the
// supplied state and validate it before returning. It runs under the state
// mutation lock and must not call Init, Create, or another mutation operation.
// The backup package supplies this callback; enablement remains private until
// authenticated enrollment is implemented.
func (s *Service) PrepareControlEnable(ctx context.Context, token string, createAndVerify func(context.Context, config.State) error) (*ControlEnableReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.lockStateMutation(); err != nil {
		return nil, err
	}
	defer s.unlockStateMutation()
	// Any new attempt invalidates a previously prepared receipt.
	s.controlEnable = nil
	state, err := s.controlTransitionStateLocked()
	if err != nil {
		return nil, err
	}
	control := *state.Controller.Control
	if control.Enabled || createAndVerify == nil {
		return nil, errors.New("disabled control identity and verified backup are required")
	}
	if err := requireControlLoopback(control); err != nil {
		return nil, err
	}
	if err := s.validateControlIdentityLocked(&control, time.Time{}, false); err != nil {
		return nil, errors.New("control identity is unusable")
	}
	if _, _, err := s.controlRecentSession(ctx, token); err != nil {
		return nil, err
	}
	if err := createAndVerify(ctx, state); err != nil {
		return nil, errors.New("create and verify control enable backup failed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Backup work can outlast recent authentication. Validate again, against the
	// current database, before granting a capability.
	session, principal, err := s.controlRecentSession(ctx, token)
	if err != nil {
		return nil, err
	}
	receipt := &ControlEnableReceipt{}
	if _, err := rand.Read(receipt.nonce[:]); err != nil {
		return nil, errors.New("create control enable authorization failed")
	}
	expires := time.Now().Add(controlauth.DefaultRecentAuthTTL)
	if principal.ExpiresAt.Before(expires) {
		expires = principal.ExpiresAt
	}
	s.controlEnable = &controlEnableAuthorization{receipt: receipt, controllerID: state.Controller.ControllerID,
		identity: control, session: session, expiresAt: expires}
	return receipt, nil
}

func requireControlLoopback(control config.ControlIdentityState) error {
	bind, err := netip.ParseAddr(control.BindIP)
	if err != nil || !bind.IsLoopback() || bind.Is4In6() || bind.Zone() != "" {
		return errors.New("control lifecycle requires a literal loopback bind IP")
	}
	return nil
}

func (s *Service) controlTransitionStateLocked() (config.State, error) {
	if err := s.checkControlMutationJournalsLocked(); err != nil {
		return config.State{}, err
	}
	state, err := s.store.Load()
	if err != nil {
		return config.State{}, errors.New("load control identity failed")
	}
	if err := validateStateMode(state); err != nil {
		return config.State{}, err
	}
	if state.EffectiveMode() != config.ModeController || state.Controller == nil || state.Controller.Control == nil {
		return config.State{}, errors.New("control identity is not prepared")
	}
	if err := s.recoverControlIdentityLocked(state); err != nil {
		return config.State{}, errors.New("control preparation requires offline inspection")
	}
	if err := s.recoverControlServerRotationLocked(state, time.Time{}); err != nil {
		return config.State{}, errors.New("control rotation requires offline inspection")
	}
	if err := ValidateControlIdentityMetadata(state.Controller.Control, s.cfg.WebUIPort); err != nil {
		return config.State{}, err
	}
	return state, nil
}

func (s *Service) controlRecentSession(ctx context.Context, token string) (controlauth.Digest, controlauth.Principal, error) {
	var digest controlauth.Digest
	db, keys, err := s.openControlRegistry(ctx)
	if err != nil {
		return digest, controlauth.Principal{}, err
	}
	defer func() { _ = db.Close() }()
	auth, err := controlauth.NewService(db, keys, s.controllerAuthOptions)
	if err != nil {
		return digest, controlauth.Principal{}, errors.New("controller authorization is unavailable")
	}
	principal, err := auth.ValidateSession(ctx, token, time.Now().UTC())
	if err != nil {
		return digest, controlauth.Principal{}, errors.New("controller session is unavailable")
	}
	if !principal.RecentAuth {
		return digest, principal, controlauth.ErrRecentAuthRequired
	}
	digest, err = keys.SessionDigest(token)
	return digest, principal, err
}

func (s *Service) openControlRegistry(ctx context.Context) (*sqldb.DB, *controlauth.Keys, error) {
	if err := s.store.CheckRestorePending(); err != nil {
		return nil, nil, errors.New("controller restore is pending")
	}
	if s.cfg.DatabaseMode != sqldb.ModeSQLite {
		return nil, nil, errors.New("control registry requires SQLite")
	}
	info, err := os.Lstat(s.cfg.DatabasePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, nil, errors.New("control registry is unavailable")
	}
	keys, err := controlauth.LoadKeys(filepath.Join(s.cfg.ConfigDir, controlauth.KeyFileName))
	if err != nil {
		return nil, nil, errors.New("controller authorization is unavailable")
	}
	db, err := sqldb.Open(ctx, s.cfg)
	if err != nil {
		return nil, nil, errors.New("control registry is unavailable")
	}
	if err := db.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, nil, errors.New("control registry migration failed")
	}
	admin, err := db.FindControllerAdmin(ctx)
	if err != nil || !admin.DisabledAt.IsZero() {
		_ = db.Close()
		return nil, nil, errors.New("controller administrator is unavailable")
	}
	return db, keys, nil
}

// enableControlLoopback has no product caller. Non-loopback transport and
// public enablement remain gated on authenticated node enrollment.
func (s *Service) enableControlLoopback(ctx context.Context, token string, receipt *ControlEnableReceipt, routes []controlserver.Route) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.lockStateMutation(); err != nil {
		return err
	}
	defer s.unlockStateMutation()
	authorization := s.controlEnable
	if receipt == nil || authorization == nil || receipt != authorization.receipt {
		return errors.New("fresh verified control backup is required")
	}
	s.controlEnable = nil // Every matching attempt consumes the receipt, even on failure.
	state, err := s.controlTransitionStateLocked()
	if err != nil {
		return err
	}
	if state.Controller.ControllerID != authorization.controllerID || *state.Controller.Control != authorization.identity ||
		!time.Now().Before(authorization.expiresAt) {
		return errors.New("control backup authorization is stale")
	}
	session, _, err := s.controlRecentSession(ctx, token)
	if err != nil || session != authorization.session {
		return errors.New("control backup authorization session changed")
	}
	if err := s.stopControlOwnerLocked(); err != nil {
		return err
	}
	owner, err := s.newControlOwnerLocked(ctx, state, routes)
	if err != nil {
		return err
	}
	if err := s.controlLifecycleStep("after-bind"); err != nil {
		owner.discard()
		return err
	}
	control := *state.Controller.Control
	control.Enabled = true
	state.Controller.Control = &control
	if err := s.commitControlEnabledLocked(ctx, state); err != nil {
		owner.discard()
		return err
	}
	s.startControlOwnerLocked(owner)
	return nil
}

func (s *Service) commitControlEnabledLocked(ctx context.Context, state config.State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.controlLifecycleStep("before-state-save"); err != nil {
		return err
	}
	// A failed Save may have renamed state.json. Never roll back an uncertain
	// commit or publish a socket on failure; restart rereads authoritative state.
	if err := s.saveState(state); err != nil {
		return errors.New("commit control enablement failed")
	}
	if err := s.controlLifecycleStep("after-state-save"); err != nil {
		return err
	}
	if err := s.store.SyncStateDirectory(); err != nil {
		return errors.New("sync control enablement failed")
	}
	if err := s.controlLifecycleStep("after-state-sync"); err != nil {
		return err
	}
	committed, err := s.store.Load()
	if err != nil || committed.Controller == nil || committed.Controller.ControllerID != state.Controller.ControllerID ||
		committed.Controller.Control == nil || *committed.Controller.Control != *state.Controller.Control {
		return errors.New("committed control enablement changed")
	}
	return ctx.Err()
}

// restartControlLoopback is an explicit private lifecycle entry, never called by
// Init or installer/startup paths. It retries only the committed identity.
func (s *Service) restartControlLoopback(ctx context.Context, routes []controlserver.Route) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.lockStateMutation(); err != nil {
		return err
	}
	defer s.unlockStateMutation()
	s.controlEnable = nil
	state, err := s.controlTransitionStateLocked()
	if err != nil {
		return err
	}
	if !state.Controller.Control.Enabled {
		return errors.New("control listener is disabled")
	}
	if err := s.stopControlOwnerLocked(); err != nil {
		return err
	}
	owner, err := s.newControlOwnerLocked(ctx, state, routes)
	if err != nil {
		return err
	}
	s.startControlOwnerLocked(owner)
	return nil
}

func (s *Service) newControlOwnerLocked(ctx context.Context, state config.State, routes []controlserver.Route) (*controlRuntimeOwner, error) {
	control := *state.Controller.Control
	if err := requireControlLoopback(control); err != nil {
		return nil, err
	}
	material, err := s.store.LoadControlIdentity(control.CAGeneration, control.ServerGeneration)
	if err != nil {
		return nil, errors.New("control identity files are unavailable")
	}
	db, _, err := s.openControlRegistry(ctx)
	if err != nil {
		return nil, err
	}
	authorizer, err := NewControlNodeAuthorizer(db, state.Controller.ControllerID, control, material, time.Now)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	runtime, err := controlserver.New(material, controlpki.Endpoint{BindIP: control.BindIP, Advertised: control.Advertised, Port: control.Port},
		control.CAPin, &controlEnabledAuthorizer{store: s, identity: control, controllerID: state.Controller.ControllerID, nodes: authorizer}, routes)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	owner := &controlRuntimeOwner{runtime: runtime, db: db, done: make(chan struct{})}
	if err := runtime.Bind(); err != nil {
		owner.discard()
		return nil, errors.New("bind control loopback listener failed")
	}
	return owner, nil
}

// Requests also observe disabled/pending state after another Service or an
// offline restore changes state. Browser cookies never authorize node routes.
type controlEnabledAuthorizer struct {
	store        *Service
	identity     config.ControlIdentityState
	controllerID string
	nodes        *ControlNodeAuthorizer
}

func (auth *controlEnabledAuthorizer) Authorize(ctx context.Context, cert *x509.Certificate, route string) (controlserver.NodeIdentity, error) {
	if err := auth.store.store.CheckRestorePending(); err != nil {
		return controlserver.NodeIdentity{}, errors.New("control admission is closed")
	}
	state, err := auth.store.store.Load()
	if err != nil || state.Controller == nil || state.Controller.ControllerID != auth.controllerID || state.Controller.Control == nil {
		return controlserver.NodeIdentity{}, errors.New("control admission is closed")
	}
	control := *state.Controller.Control
	// ServerGeneration changes through the owner, which publishes TLS and closes
	// predecessor connections. CA, endpoint and enabled authority cannot drift.
	control.ServerGeneration = auth.identity.ServerGeneration
	control.Enabled = false
	expected := auth.identity
	expected.Enabled = false
	if !state.Controller.Control.Enabled || control != expected {
		return controlserver.NodeIdentity{}, errors.New("control admission is closed")
	}
	return auth.nodes.Authorize(ctx, cert, route)
}

func (s *Service) startControlOwnerLocked(owner *controlRuntimeOwner) {
	// The operation context authorizes the transition, not the listener lifetime.
	// Explicit shutdown/disable owns cancellation after a successful commit.
	serveCtx, cancel := context.WithCancel(context.Background())
	owner.cancel = cancel
	s.controlOwner = owner
	step := s.controlRuntimeStep
	go func() {
		var err error
		if step != nil {
			err = step("before-serve")
		}
		if err == nil {
			err = owner.runtime.Serve(serveCtx)
		}
		owner.runtime.Close()
		cancel()
		err = errors.Join(err, owner.db.Close())
		owner.resultMu.Lock()
		owner.result = err
		owner.resultMu.Unlock()
		if err != nil {
			s.log("warn", "control.runtime.stopped", "control listener stopped; explicit retry is required", nil, nil)
		}
		close(owner.done)
	}()
}

func (owner *controlRuntimeOwner) discard() {
	owner.runtime.Close()
	_ = owner.db.Close()
}

func (s *Service) stopControlOwnerLocked() error {
	if s.controlOwner == nil {
		return nil
	}
	owner := s.controlOwner
	owner.runtime.Close()
	owner.cancel()
	timer := time.NewTimer(12 * time.Second)
	defer timer.Stop()
	select {
	case <-owner.done:
		s.controlOwner = nil
		return nil
	case <-timer.C:
		return errors.New("control listener shutdown did not finish")
	}
}

func (s *Service) disableControlLoopback(ctx context.Context, token string) error {
	if err := s.lockStateMutation(); err != nil {
		return err
	}
	defer s.unlockStateMutation()
	if _, _, err := s.controlRecentSession(ctx, token); err != nil {
		return err
	}
	s.controlEnable = nil
	// Close admission before touching persisted state, including on commit failure.
	if err := s.stopControlOwnerLocked(); err != nil {
		return err
	}
	state, err := s.controlTransitionStateLocked()
	if err != nil {
		return err
	}
	if !state.Controller.Control.Enabled {
		return nil
	}
	control := *state.Controller.Control
	control.Enabled = false
	state.Controller.Control = &control
	return s.commitControlEnabledLocked(ctx, state)
}

func (s *Service) shutdownControlLoopback() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.controlEnable = nil
	return s.stopControlOwnerLocked()
}

func (s *Service) rotateControlLoopbackLeaf(ctx context.Context, expected string, now time.Time) (config.ControlIdentityState, error) {
	s.mu.Lock()
	owner := s.controlOwner
	s.mu.Unlock()
	if owner == nil {
		return config.ControlIdentityState{}, errors.New("control listener has no runtime owner")
	}
	return s.rotateControlServerLeaf(ctx, expected, now, owner.runtime)
}

func (s *Service) controlLifecycleStep(point string) error {
	if s.controlRuntimeStep != nil {
		return s.controlRuntimeStep(point)
	}
	return nil
}
