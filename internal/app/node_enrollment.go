package app

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/netip"
	"net/url"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/google/uuid"
)

func ValidateNodeConnection(c *config.NodeConnectionState) error {
	if c == nil || !controlPinRE.MatchString(c.CAPin) {
		return errors.New("invalid node connection")
	}
	if _, err := storage.NodeIdentityRelativePaths(c.CredentialGeneration); err != nil {
		return errors.New("invalid node connection")
	}
	u, err := url.Parse(c.ControllerURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("invalid node connection")
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !ip.IsLoopback() || ip.Is4In6() || ip.Zone() != "" || u.Port() == "" {
		return errors.New("invalid node connection")
	}
	return nil
}

// PreflightNodeEnrollment intentionally avoids Init so it cannot create a tunnel.
func (s *Service) PreflightNodeEnrollment(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.lockControlRequest(ctx); err != nil {
		return err
	}
	defer s.unlockStateMutation()
	return s.preflightNodeEnrollmentLocked()
}
func (s *Service) preflightNodeEnrollmentLocked() error {
	if err := s.store.CheckRestorePending(); err != nil {
		return err
	}
	for _, check := range []func() error{func() error { _, e := s.store.LoadPendingDesiredStateCommit(); return e }, func() error { _, e := s.store.LoadControllerActivationJournal(); return e }, func() error { _, e := s.store.LoadControlIdentityJournal(); return e }, func() error { _, e := s.store.LoadNodeIdentityJournal(); return e }, func() error { _, e := s.store.LoadNodeRenewalJournal(); return e }} {
		if err := check(); err == nil {
			return errors.New("enrollment recovery is pending")
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("enrollment recovery is pending")
		}
	}
	if err := s.store.CheckNoControlServerRotation(); err != nil {
		return errors.New("enrollment recovery is pending")
	}
	state, err := s.store.Load()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if state.EffectiveMode() != config.ModeStandalone || state.Controller != nil || state.ManagedNode != nil || state.NodeConnection != nil {
		return errors.New("node enrollment is unavailable")
	}
	return nil
}

func (s *Service) recoverNodeEnrollmentLocked() error {
	state, loadErr := s.store.Load()
	if errors.Is(loadErr, os.ErrNotExist) {
		if _, err := s.store.LoadNodeIdentityJournal(); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return errors.New("node enrollment recovery is pending")
	}
	if loadErr != nil {
		return errors.New("node enrollment recovery is pending")
	}
	j, err := s.store.LoadNodeIdentityJournal()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || validateStateMode(state) != nil || state.EffectiveMode() != config.ModeNode || state.NodeConnection == nil || state.NodeConnection.CredentialGeneration != j.Generation {
		return errors.New("node enrollment recovery is pending")
	}
	m, err := s.store.LoadNodeIdentity(j.Generation)
	if err != nil || ValidateNodeIdentity(state.NodeConnection, m, true) != nil {
		return errors.New("node enrollment recovery is pending")
	}
	if err := s.store.SyncStateDirectory(); err != nil {
		return errors.New("sync node enrollment failed")
	}
	if err := s.store.DeleteNodeIdentityJournal(); err != nil {
		return err
	}
	return s.store.SyncStateDirectory()
}

func ValidateNodeIdentity(c *config.NodeConnectionState, m storage.NodeIdentityMaterial, allowExpired bool) error {
	if err := ValidateNodeConnection(c); err != nil {
		return err
	}
	caBlock, caRest := pem.Decode(m.CACert)
	certBlock, certRest := pem.Decode(m.Certificate)
	keyBlock, keyRest := pem.Decode(m.PrivateKey)
	if caBlock == nil || certBlock == nil || keyBlock == nil || caBlock.Type != "CERTIFICATE" || certBlock.Type != "CERTIFICATE" || keyBlock.Type != "PRIVATE KEY" || len(caBlock.Headers) > 0 || len(certBlock.Headers) > 0 || len(keyBlock.Headers) > 0 || len(strings.TrimSpace(string(caRest))) != 0 || len(strings.TrimSpace(string(certRest))) != 0 || len(strings.TrimSpace(string(keyRest))) != 0 {
		return errors.New("invalid node identity")
	}
	ca, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil || !ca.IsCA || !ca.BasicConstraintsValid || ca.CheckSignatureFrom(ca) != nil || ca.KeyUsage&x509.KeyUsageCertSign == 0 || controlpki.Pin(ca) != c.CAPin {
		return errors.New("invalid node identity")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil || cert.IsCA || !cert.BasicConstraintsValid || cert.PublicKeyAlgorithm != x509.Ed25519 || len(cert.DNSNames) > 0 || len(cert.IPAddresses) > 0 || len(cert.URIs) > 0 || len(cert.EmailAddresses) > 0 || len(cert.UnknownExtKeyUsage) > 0 || cert.NotAfter.Sub(cert.NotBefore) > controlpki.NodeCertificateTTL+5*time.Minute || cert.KeyUsage != x509.KeyUsageDigitalSignature || len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		return errors.New("invalid node identity")
	}
	key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	private, ok := key.(ed25519.PrivateKey)
	if err != nil || !ok || !private.Public().(ed25519.PublicKey).Equal(cert.PublicKey) {
		return errors.New("invalid node identity")
	}
	when := time.Now()
	if allowExpired {
		when = cert.NotBefore.Add(time.Second)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: when, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return errors.New("invalid node identity")
	}
	return nil
}

// InstallNodeEnrollment is the local commit point after a controller approved
// a CSR. It deliberately does not render, apply, or otherwise alter tunnels.
func (s *Service) InstallNodeEnrollment(ctx context.Context, invitation controlapi.Invitation, approved controlapi.EnrollmentStatus, privateKeyPEM []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.lockControlRequest(ctx); err != nil {
		return err
	}
	defer s.unlockStateMutation()
	if err := s.preflightNodeEnrollmentLocked(); err != nil {
		return err
	}
	state, err := s.store.Load()
	fresh := errors.Is(err, os.ErrNotExist)
	if err != nil && !fresh {
		return err
	}
	if !fresh && (state.EffectiveMode() == config.ModeController || state.ManagedNode != nil || state.NodeConnection != nil) {
		return errors.New("node enrollment is already bound")
	}
	if err := validateNodeApproval(invitation, approved, privateKeyPEM); err != nil {
		return err
	}
	generation := uuid.NewString()
	generation = strings.ReplaceAll(generation, "-", "")
	if err := s.store.SaveNodeIdentityJournal(storage.NodeIdentityJournal{Generation: generation}); err != nil {
		return errors.New("prepare node enrollment failed")
	}
	material := storage.NodeIdentityMaterial{CACert: []byte(invitation.CACertPEM), Certificate: []byte(approved.Certificate.CertificatePEM), PrivateKey: privateKeyPEM}
	if err := s.store.SaveNodeIdentity(generation, material); err != nil {
		return errors.New("store node credentials failed")
	}
	if _, err := s.store.LoadNodeIdentity(generation); err != nil {
		return errors.New("verify node credentials failed")
	}
	now := time.Now().UTC()
	nodeID, _ := uuid.Parse(approved.NodeID)
	epoch := uuid.NewString()
	if fresh {
		secret, err := s.sessionSecretValue()
		if err != nil {
			return err
		}
		state = config.State{SchemaVersion: config.CurrentStateSchemaVersion, SessionSecret: secret, ServerHost: s.cfg.ServerHost, ExternalInterface: s.cfg.ExternalInterface, Warp: config.Warp{InterfaceName: "warp0", MTU: 1280, PersistentKeepalive: 25}, Tunnels: []config.Tunnel{}, CreatedAt: now}
	}
	state.Mode = config.ModeNode
	state.ManagedNode = &config.ManagedNodeState{NodeID: nodeID.String(), ControllerID: approved.ControllerID, StateEpoch: epoch, BindingEpoch: approved.BindingEpoch}
	state.NodeConnection = &config.NodeConnectionState{ControllerURL: invitation.ControllerURL, CAPin: invitation.CAPin, CredentialGeneration: generation}
	if err := ValidateNodeConnection(state.NodeConnection); err != nil {
		return err
	}
	state.UpdatedAt = now
	if err := validateStateMode(state); err != nil {
		return err
	}
	if err := s.store.Save(state); err != nil {
		return errors.New("commit node enrollment failed")
	}
	committed, err := s.store.Load()
	if err != nil || committed.NodeConnection == nil || committed.ManagedNode == nil || !reflect.DeepEqual(state, committed) {
		return errors.New("verify node enrollment failed")
	}
	if err := s.store.SyncStateDirectory(); err != nil {
		return errors.New("sync node enrollment failed")
	}
	if err := s.store.DeleteNodeIdentityJournal(); err != nil {
		return errors.New("finalize node enrollment failed")
	}
	return s.store.SyncStateDirectory()
}

func validateNodeApproval(invitation controlapi.Invitation, approved controlapi.EnrollmentStatus, privateKeyPEM []byte) error {
	if approved.Status != "approved" || approved.Certificate == nil || approved.BindingEpoch != 1 || approved.ContractVersion != 1 || !validControlUUID(approved.NodeID) || !validControlUUID(approved.ControllerID) {
		return errors.New("invalid enrollment approval")
	}
	if _, err := uuid.Parse(approved.NodeID); err != nil {
		return errors.New("invalid enrollment approval")
	}
	if _, err := uuid.Parse(approved.ControllerID); err != nil {
		return errors.New("invalid enrollment approval")
	}
	u, err := url.Parse(invitation.ControllerURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("invalid enrollment invitation")
	}
	caBlock, caRest := pem.Decode([]byte(invitation.CACertPEM))
	certBlock, certRest := pem.Decode([]byte(approved.Certificate.CertificatePEM))
	keyBlock, _ := pem.Decode(privateKeyPEM)
	if caBlock == nil || certBlock == nil || keyBlock == nil || caBlock.Type != "CERTIFICATE" || certBlock.Type != "CERTIFICATE" || keyBlock.Type != "PRIVATE KEY" || len(caBlock.Headers) > 0 || len(certBlock.Headers) > 0 || len(keyBlock.Headers) > 0 || len(strings.TrimSpace(string(caRest))) != 0 || len(strings.TrimSpace(string(certRest))) != 0 {
		return errors.New("invalid enrollment approval")
	}
	ca, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil || controlpki.Pin(ca) != invitation.CAPin || !ca.IsCA || !ca.BasicConstraintsValid || ca.CheckSignatureFrom(ca) != nil || ca.KeyUsage&x509.KeyUsageCertSign == 0 {
		return errors.New("invalid enrollment approval")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return errors.New("invalid enrollment approval")
	}
	key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return errors.New("invalid enrollment approval")
	}
	private, ok := key.(ed25519.PrivateKey)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	_, verifyErr := cert.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: time.Now(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	if !ok || cert.IsCA || !cert.BasicConstraintsValid || cert.PublicKeyAlgorithm != x509.Ed25519 || cert.KeyUsage != x509.KeyUsageDigitalSignature || !cert.NotBefore.Before(time.Now()) || !time.Now().Before(cert.NotAfter) || verifyErr != nil || len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth || !private.Public().(ed25519.PublicKey).Equal(cert.PublicKey) || cert.SerialNumber.String() != approved.Certificate.Serial || !cert.NotBefore.Equal(approved.Certificate.NotBefore) || !cert.NotAfter.Equal(approved.Certificate.NotAfter) || strings.TrimSpace(approved.Certificate.ChainPEM) != strings.TrimSpace(invitation.CACertPEM) {
		return errors.New("invalid enrollment approval")
	}
	return ValidateNodeIdentity(&config.NodeConnectionState{ControllerURL: invitation.ControllerURL, CAPin: invitation.CAPin, CredentialGeneration: strings.Repeat("a", 32)}, storage.NodeIdentityMaterial{CACert: []byte(invitation.CACertPEM), Certificate: []byte(approved.Certificate.CertificatePEM), PrivateKey: privateKeyPEM}, false)
}

// NodeAgentState reads committed configuration without initializing or repairing
// it, and participates in cancellable mutation serialization.
func (s *Service) NodeAgentState(ctx context.Context) (config.State, error) {
	if err := s.lockControlRequest(ctx); err != nil {
		return config.State{}, err
	}
	defer s.unlockStateMutation()
	if err := s.store.CheckRestorePending(); err != nil {
		return config.State{}, err
	}
	if _, err := s.store.LoadNodeIdentityJournal(); !errors.Is(err, os.ErrNotExist) {
		return config.State{}, errors.New("node enrollment recovery is pending")
	}
	if err := s.recoverNodeRenewalLocked(); err != nil {
		return config.State{}, err
	}
	state, err := s.store.Load()
	if err != nil {
		return config.State{}, err
	}
	if err := validateStateMode(state); err != nil {
		return config.State{}, err
	}
	return state, nil
}
