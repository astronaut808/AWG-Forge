package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/controlserver"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/google/uuid"
)

// issueInitialNodeCertificate is deliberately private until an approved
// enrollment transaction can call it. There is no HTTP or startup caller.
func (s *Service) issueInitialNodeCertificate(ctx context.Context, nodeID string, csrDER []byte, now time.Time) ([]byte, error) {
	if id, err := uuid.Parse(nodeID); err != nil || id.String() != nodeID {
		return nil, errors.New("invalid node ID")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := s.lockStateMutation(); err != nil {
		return nil, err
	}
	defer s.unlockStateMutation()
	if err := s.store.CheckRestorePending(); err != nil {
		return nil, errors.New("controller restore is pending")
	}
	if _, err := s.store.LoadControlIdentityJournal(); err == nil {
		return nil, errors.New("control identity transition is pending")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("cannot inspect control identity transition")
	}
	state, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	if state.EffectiveMode() != config.ModeController || state.Controller == nil || state.Controller.Control == nil ||
		state.Controller.Control.Enabled || s.cfg.DatabaseMode != sqldb.ModeSQLite {
		return nil, errors.New("control identity is not prepared")
	}
	control := state.Controller.Control
	if err := s.validateControlIdentityLocked(control, now, false); err != nil {
		return nil, errors.New("control identity is unusable")
	}
	info, err := os.Lstat(s.cfg.DatabasePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, errors.New("controller database is unavailable")
	}
	material, err := s.store.LoadControlIdentity(control.CAGeneration, control.ServerGeneration)
	if err != nil {
		return nil, errors.New("control identity is unavailable")
	}
	issued, err := controlpki.IssueNodeCertificate(material, csrDER, now)
	if err != nil {
		return nil, err
	}
	db, err := sqldb.Open(ctx, s.cfg)
	if err != nil {
		return nil, errors.New("controller database is unavailable")
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(ctx); err != nil {
		return nil, errors.New("controller database migration failed")
	}
	initialized, err := db.ControllerAuthInitialized(ctx)
	if err != nil || !initialized {
		return nil, errors.New("controller authentication is unavailable")
	}
	storedDER, err := db.RegisterInitialNodeCertificate(ctx, sqldb.NodeIdentity{
		ControllerID: state.Controller.ControllerID, NodeID: nodeID, BindingEpoch: 1,
	}, control.CAGeneration, issued, now)
	if err != nil {
		return nil, err
	}
	stored, err := x509.ParseCertificate(storedDER)
	caBlock, _ := pem.Decode(material.CACert)
	if caBlock == nil || stored == nil {
		return nil, errors.New("stored node certificate is unusable")
	}
	ca, caErr := x509.ParseCertificate(caBlock.Bytes)
	csr, _, csrErr := controlpki.ParseNodeCSR(csrDER)
	if err != nil || caErr != nil || csrErr != nil ||
		stored.CheckSignatureFrom(ca) != nil ||
		stored.PublicKeyAlgorithm != x509.Ed25519 ||
		!stored.NotBefore.Before(now) || !now.Before(stored.NotAfter) ||
		!bytes.Equal(stored.RawSubjectPublicKeyInfo, csr.RawSubjectPublicKeyInfo) {
		return nil, errors.New("stored node certificate is unusable")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: storedDER}), nil
}

// renewNodeCertificate is internal until a reviewed node route exists. The
// predecessor is the actual verified TLS peer leaf, never a body-supplied ID.
func (s *Service) renewNodeCertificate(ctx context.Context, predecessor *x509.Certificate, csrDER []byte, now time.Time) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := s.lockStateMutation(); err != nil {
		return nil, err
	}
	defer s.unlockStateMutation()
	if err := s.store.CheckRestorePending(); err != nil {
		return nil, errors.New("controller restore is pending")
	}
	if _, err := s.store.LoadControlIdentityJournal(); err == nil {
		return nil, errors.New("control identity transition is pending")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("cannot inspect control identity transition")
	}
	state, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	if state.EffectiveMode() != config.ModeController || state.Controller == nil ||
		state.Controller.Control == nil || state.Controller.Control.Enabled ||
		s.cfg.DatabaseMode != sqldb.ModeSQLite {
		return nil, errors.New("control identity is not prepared")
	}
	control := state.Controller.Control
	if err := s.validateControlIdentityLocked(control, now, false); err != nil {
		return nil, errors.New("control identity is unusable")
	}
	info, err := os.Lstat(s.cfg.DatabasePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, errors.New("controller database is unavailable")
	}
	material, err := s.store.LoadControlIdentity(control.CAGeneration, control.ServerGeneration)
	if err != nil {
		return nil, errors.New("control identity is unavailable")
	}
	db, err := sqldb.Open(ctx, s.cfg)
	if err != nil {
		return nil, errors.New("controller database is unavailable")
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(ctx); err != nil {
		return nil, errors.New("controller database migration failed")
	}
	initialized, err := db.ControllerAuthInitialized(ctx)
	if err != nil || !initialized {
		return nil, errors.New("controller authentication is unavailable")
	}
	authorizer, err := NewControlNodeAuthorizer(db, state.Controller.ControllerID, *control, material, func() time.Time { return now })
	if err != nil {
		return nil, err
	}
	if _, err := authorizer.Authorize(ctx, predecessor, "node.certificate-renewal"); err != nil {
		return nil, err
	}
	csr, csrHash, err := controlpki.ParseNodeCSR(csrDER)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(csr.RawSubjectPublicKeyInfo, predecessor.RawSubjectPublicKeyInfo) {
		return nil, errors.New("renewal requires a new node key")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keyHash := sha256.Sum256(csr.RawSubjectPublicKeyInfo)
	storedDER, found, err := db.FindRenewedNodeCertificate(ctx, state.Controller.ControllerID, control.CAGeneration, predecessor, csrHash, keyHash, now)
	if err != nil {
		return nil, err
	}
	if !found {
		issued, err := controlpki.IssueNodeCertificate(material, csrDER, now)
		if err != nil {
			return nil, err
		}
		storedDER, err = db.RenewNodeCertificate(ctx, state.Controller.ControllerID, control.CAGeneration, predecessor, issued, now)
		if err != nil {
			return nil, err
		}
	}
	stored, err := x509.ParseCertificate(storedDER)
	if err != nil || !bytes.Equal(stored.RawSubjectPublicKeyInfo, csr.RawSubjectPublicKeyInfo) ||
		stored.IsCA || stored.KeyUsage != x509.KeyUsageDigitalSignature ||
		len(stored.ExtKeyUsage) != 1 || stored.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth ||
		now.Before(stored.NotBefore) || !now.Before(stored.NotAfter) {
		return nil, errors.New("stored node certificate is unusable")
	}
	if _, err := stored.Verify(x509.VerifyOptions{Roots: authorizer.roots, CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, errors.New("stored node certificate is unusable")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: storedDER}), nil
}

// ControlNodeAuthorizer translates a verified client certificate into a typed
// node identity. A DB failure, revoked row, or stale binding denies admission.
type ControlNodeAuthorizer struct {
	db               *sqldb.DB
	roots            *x509.CertPool
	controllerID     string
	issuerGeneration string
	now              func() time.Time
}

func NewControlNodeAuthorizer(db *sqldb.DB, controllerID string, control config.ControlIdentityState, material controlpki.Material, now func() time.Time) (*ControlNodeAuthorizer, error) {
	if db == nil || now == nil {
		return nil, errors.New("node authorization is unavailable")
	}
	if id, err := uuid.Parse(controllerID); err != nil || id.String() != controllerID {
		return nil, errors.New("invalid controller identity")
	}
	if err := controlpki.Validate(material, controlpki.Endpoint{
		BindIP: control.BindIP, Advertised: control.Advertised, Port: control.Port,
	}, control.CAPin, now(), false); err != nil {
		return nil, errors.New("control identity is unusable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(material.CACert) {
		return nil, errors.New("control CA is unavailable")
	}
	return &ControlNodeAuthorizer{db: db, roots: roots, controllerID: controllerID, issuerGeneration: control.CAGeneration, now: now}, nil
}

func (auth *ControlNodeAuthorizer) Authorize(ctx context.Context, certificate *x509.Certificate, routeID string) (controlserver.NodeIdentity, error) {
	if auth == nil || auth.db == nil || certificate == nil || routeID == "" || auth.now == nil {
		return controlserver.NodeIdentity{}, sqldb.ErrNodeCertificateDenied
	}
	now := auth.now().UTC()
	if certificate.IsCA || certificate.KeyUsage != x509.KeyUsageDigitalSignature ||
		len(certificate.ExtKeyUsage) != 1 || certificate.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth ||
		len(certificate.UnknownExtKeyUsage) != 0 ||
		now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
		return controlserver.NodeIdentity{}, sqldb.ErrNodeCertificateDenied
	}
	if _, err := certificate.Verify(x509.VerifyOptions{Roots: auth.roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return controlserver.NodeIdentity{}, sqldb.ErrNodeCertificateDenied
	}
	identity, err := auth.db.FindActiveNodeCertificate(ctx, auth.controllerID, auth.issuerGeneration, certificate, now)
	if err != nil {
		return controlserver.NodeIdentity{}, err
	}
	return controlserver.NodeIdentity{ControllerID: identity.ControllerID, NodeID: identity.NodeID, BindingEpoch: identity.BindingEpoch}, nil
}
