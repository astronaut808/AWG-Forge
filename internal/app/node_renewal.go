package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
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

type NodeCertificateRenewal struct {
	Generation    string
	CurrentSerial string
	CSRDER        []byte
}

func nodeRenewalMatches(state config.State, j storage.NodeRenewalJournal) bool {
	return state.ManagedNode != nil && state.NodeConnection != nil && state.ManagedNode.ControllerID == j.ControllerID && state.ManagedNode.NodeID == j.NodeID && state.ManagedNode.BindingEpoch == j.BindingEpoch && state.ManagedNode.StateEpoch == j.StateEpoch && state.NodeConnection.ControllerURL == j.ControllerURL && state.NodeConnection.CAPin == j.CAPin
}

func validateRenewalCandidate(c storage.NodeRenewalCandidate) error {
	csr, _, err := controlpki.ParseNodeCSR(c.CSR)
	if err != nil {
		return errors.New("invalid renewal candidate")
	}
	block, rest := pem.Decode(c.PrivateKey)
	if block == nil || block.Type != "PRIVATE KEY" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return errors.New("invalid renewal candidate")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	private, ok := key.(ed25519.PrivateKey)
	if err != nil || !ok || !private.Public().(ed25519.PublicKey).Equal(csr.PublicKey) {
		return errors.New("invalid renewal candidate")
	}
	return nil
}

func (s *Service) nodeRenewalStateLocked() (config.State, error) {
	if err := s.store.CheckRestorePending(); err != nil {
		return config.State{}, err
	}
	for _, load := range []func() error{
		func() error { _, e := s.store.LoadNodeIdentityJournal(); return e },
		func() error { _, e := s.store.LoadPendingDesiredStateCommit(); return e },
		func() error { _, e := s.store.LoadControllerActivationJournal(); return e },
		func() error { _, e := s.store.LoadControlIdentityJournal(); return e },
	} {
		if err := load(); !errors.Is(err, os.ErrNotExist) {
			return config.State{}, errors.New("node transition is pending")
		}
	}
	if err := s.store.CheckNoControlServerRotation(); err != nil {
		return config.State{}, err
	}
	state, err := s.store.Load()
	if err != nil || validateStateMode(state) != nil || state.EffectiveMode() != config.ModeNode || state.NodeConnection == nil || state.ManagedNode == nil {
		return config.State{}, errors.New("node renewal is unavailable")
	}
	return state, nil
}

// recoverNodeRenewalLocked leaves a valid uncommitted request for the worker.
// A committed replacement is accepted historically even after expiry: recovery
// must not block local forwarding, while network clients still require validity.
func (s *Service) recoverNodeRenewalLocked() error {
	j, err := s.store.LoadNodeRenewalJournal()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("node renewal recovery is pending")
	}
	state, err := s.nodeRenewalStateLocked()
	if err != nil || !nodeRenewalMatches(state, j) {
		return errors.New("node renewal identity changed")
	}
	generation := state.NodeConnection.CredentialGeneration
	if generation != j.PredecessorGeneration && generation != j.Generation {
		return errors.New("node renewal generation changed")
	}
	candidate, err := s.store.LoadNodeRenewalCandidate(j.Generation)
	if err != nil || validateRenewalCandidate(candidate) != nil {
		return errors.New("node renewal candidate unavailable")
	}
	material, err := s.store.LoadNodeIdentity(generation)
	if err != nil || ValidateNodeIdentity(state.NodeConnection, material, true) != nil {
		return errors.New("node renewal identity unavailable")
	}
	if generation == j.PredecessorGeneration {
		return nil
	}
	if !bytes.Equal(material.PrivateKey, candidate.PrivateKey) {
		return errors.New("node renewal committed key differs")
	}
	if err := s.store.SyncStateDirectory(); err != nil {
		return err
	}
	return s.store.DeleteNodeRenewalJournal()
}

// PrepareNodeCertificateRenewal makes one exact signed CSR durable before a
// request may leave the node. It returns the same CSR after a lost response.
func (s *Service) PrepareNodeCertificateRenewal(ctx context.Context, expected config.NodeConnectionState) (NodeCertificateRenewal, error) {
	if err := s.lockControlRequest(ctx); err != nil {
		return NodeCertificateRenewal{}, err
	}
	defer s.unlockStateMutation()
	if err := s.recoverNodeRenewalLocked(); err != nil {
		return NodeCertificateRenewal{}, err
	}
	state, err := s.nodeRenewalStateLocked()
	if err != nil || *state.NodeConnection != expected {
		return NodeCertificateRenewal{}, errors.New("node binding changed")
	}
	material, err := s.store.LoadNodeIdentity(expected.CredentialGeneration)
	if err != nil || ValidateNodeIdentity(&expected, material, false) != nil {
		return NodeCertificateRenewal{}, errors.New("node identity unusable")
	}
	block, _ := pem.Decode(material.Certificate)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return NodeCertificateRenewal{}, errors.New("node identity unusable")
	}
	if time.Now().Before(cert.NotBefore.Add(cert.NotAfter.Sub(cert.NotBefore) * 2 / 3)) {
		return NodeCertificateRenewal{}, errors.New("node renewal window has not started")
	}
	j, err := s.store.LoadNodeRenewalJournal()
	if errors.Is(err, os.ErrNotExist) {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return NodeCertificateRenewal{}, errors.New("generate renewal key failed")
		}
		csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
		if err != nil {
			return NodeCertificateRenewal{}, errors.New("generate renewal request failed")
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return NodeCertificateRenewal{}, err
		}
		id, err := uuid.NewRandom()
		if err != nil {
			return NodeCertificateRenewal{}, err
		}
		j = storage.NodeRenewalJournal{PredecessorGeneration: expected.CredentialGeneration, Generation: strings.ReplaceAll(id.String(), "-", ""), ControllerID: state.ManagedNode.ControllerID, NodeID: state.ManagedNode.NodeID, BindingEpoch: state.ManagedNode.BindingEpoch, StateEpoch: state.ManagedNode.StateEpoch, ControllerURL: expected.ControllerURL, CAPin: expected.CAPin}
		candidate := storage.NodeRenewalCandidate{PrivateKey: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), CSR: csr}
		if err := s.store.SaveNodeRenewalCandidate(j.Generation, candidate); err != nil {
			return NodeCertificateRenewal{}, errors.New("persist renewal candidate failed")
		}
		if err := s.store.SaveNodeRenewalJournal(j); err != nil {
			return NodeCertificateRenewal{}, errors.New("persist renewal journal failed")
		}
		if err := s.nodeRenewalCheckpoint("prepared"); err != nil {
			return NodeCertificateRenewal{}, err
		}
	} else if err != nil {
		return NodeCertificateRenewal{}, errors.New("renewal journal unavailable")
	}
	if !nodeRenewalMatches(state, j) || j.PredecessorGeneration != expected.CredentialGeneration {
		return NodeCertificateRenewal{}, errors.New("node renewal identity changed")
	}
	candidate, err := s.store.LoadNodeRenewalCandidate(j.Generation)
	if err != nil || validateRenewalCandidate(candidate) != nil {
		return NodeCertificateRenewal{}, errors.New("renewal candidate unavailable")
	}
	csr, _, _ := controlpki.ParseNodeCSR(candidate.CSR)
	if bytes.Equal(csr.RawSubjectPublicKeyInfo, cert.RawSubjectPublicKeyInfo) {
		return NodeCertificateRenewal{}, errors.New("renewal key must differ")
	}
	// A previous publication may have succeeded before its directory sync
	// returned an error. Reestablish durability before sending the first retry.
	if err := s.store.SyncStateDirectory(); err != nil {
		return NodeCertificateRenewal{}, errors.New("sync renewal journal failed")
	}
	return NodeCertificateRenewal{Generation: j.Generation, CurrentSerial: cert.SerialNumber.String(), CSRDER: append([]byte(nil), candidate.CSR...)}, nil
}

// InstallNodeCertificateRenewal changes only the credential generation after
// validating the key-bound replacement. Tunnels and boot identity stay intact.
func (s *Service) InstallNodeCertificateRenewal(ctx context.Context, expected config.NodeConnectionState, generation string, issued controlapi.IssuedCertificate) error {
	if err := s.lockControlRequest(ctx); err != nil {
		return err
	}
	defer s.unlockStateMutation()
	state, err := s.nodeRenewalStateLocked()
	if err != nil || *state.NodeConnection != expected {
		return errors.New("node binding changed")
	}
	j, err := s.store.LoadNodeRenewalJournal()
	if err != nil || !nodeRenewalMatches(state, j) || j.Generation != generation || j.PredecessorGeneration != expected.CredentialGeneration {
		return errors.New("node renewal identity changed")
	}
	candidate, err := s.store.LoadNodeRenewalCandidate(generation)
	if err != nil || validateRenewalCandidate(candidate) != nil {
		return errors.New("renewal candidate unavailable")
	}
	old, err := s.store.LoadNodeIdentity(expected.CredentialGeneration)
	if err != nil || ValidateNodeIdentity(&expected, old, true) != nil {
		return errors.New("node identity unavailable")
	}
	if strings.TrimSpace(issued.ChainPEM) != strings.TrimSpace(string(old.CACert)) {
		return errors.New("renewal CA changed")
	}
	material := storage.NodeIdentityMaterial{CACert: old.CACert, Certificate: []byte(issued.CertificatePEM), PrivateKey: candidate.PrivateKey}
	connection := expected
	connection.CredentialGeneration = generation
	if err := ValidateNodeIdentity(&connection, material, false); err != nil {
		return errors.New("invalid renewal certificate")
	}
	block, _ := pem.Decode(material.Certificate)
	cert, err := x509.ParseCertificate(block.Bytes)
	oldBlock, _ := pem.Decode(old.Certificate)
	predecessor, oldErr := x509.ParseCertificate(oldBlock.Bytes)
	if err != nil || oldErr != nil || cert.SerialNumber.String() != issued.Serial || !cert.NotBefore.Equal(issued.NotBefore) || !cert.NotAfter.Equal(issued.NotAfter) || cert.NotAfter.Before(predecessor.NotAfter) || bytes.Equal(cert.RawSubjectPublicKeyInfo, predecessor.RawSubjectPublicKeyInfo) {
		return errors.New("invalid renewal certificate metadata")
	}
	if err := s.store.PublishRenewedNodeIdentity(generation, material); err != nil {
		return errors.New("persist renewed identity failed")
	}
	verified, err := s.store.LoadNodeIdentity(generation)
	if err != nil || !reflect.DeepEqual(material, verified) {
		return errors.New("verify renewed identity failed")
	}
	if err := s.nodeRenewalCheckpoint("credentials"); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	state.NodeConnection = &connection
	state.UpdatedAt = time.Now().UTC()
	if err := s.saveState(state); err != nil {
		return errors.New("commit renewed identity failed")
	}
	committed, err := s.store.Load()
	if err != nil || !reflect.DeepEqual(state, committed) {
		return errors.New("verify renewed state failed")
	}
	if err := s.store.SyncStateDirectory(); err != nil {
		return err
	}
	if err := s.nodeRenewalCheckpoint("committed"); err != nil {
		return err
	}
	return s.store.DeleteNodeRenewalJournal()
}

func (s *Service) nodeRenewalCheckpoint(point string) error {
	if s.nodeRenewalStep != nil {
		return s.nodeRenewalStep(point)
	}
	return nil
}
