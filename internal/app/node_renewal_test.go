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
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/google/uuid"
)

func nodeRenewalFixture(t *testing.T) (*Service, config.Config, controlpki.Material, config.State) {
	t.Helper()
	cfg := controllerTestConfig(t)
	svc := New(cfg)
	if _, err := svc.Init(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-21 * 24 * time.Hour)
	material, pin, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 9443}, now)
	if err != nil {
		t.Fatal(err)
	}
	csr, key := renewalTestCSR(t)
	cert, err := controlpki.IssueNodeCertificate(material, csr, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	invitation := controlapi.Invitation{ControllerURL: "https://127.0.0.1:9443", CAPin: pin, CACertPEM: string(material.CACert)}
	approved := controlapi.EnrollmentStatus{Status: "approved", ContractVersion: 1, NodeID: uuid.NewString(), ControllerID: uuid.NewString(), BindingEpoch: 1, Certificate: &controlapi.IssuedCertificate{CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.DER})), ChainPEM: string(material.CACert), Serial: cert.Serial, NotBefore: cert.NotBefore, NotAfter: cert.NotAfter}}
	if err := svc.InstallNodeEnrollment(context.Background(), invitation, approved, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartManagedNodeBootContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := storage.New(cfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	return svc, cfg, material, state
}

func issueRenewalForTest(t *testing.T, material controlpki.Material, request NodeCertificateRenewal) controlapi.IssuedCertificate {
	t.Helper()
	issued, err := controlpki.IssueNodeCertificate(material, request.CSRDER, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return controlapi.IssuedCertificate{CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issued.DER})), ChainPEM: string(material.CACert), Serial: issued.Serial, NotBefore: issued.NotBefore, NotAfter: issued.NotAfter}
}

func TestNodeRenewalDurableRetryAndAtomicRecovery(t *testing.T) {
	for _, point := range []string{"prepared", "credentials", "committed"} {
		t.Run(point, func(t *testing.T) {
			svc, cfg, material, before := nodeRenewalFixture(t)
			store := storage.New(cfg.ConfigDir)
			svc.nodeRenewalStep = func(at string) error {
				if at == point {
					return errors.New("injected interruption")
				}
				return nil
			}
			request, err := svc.PrepareNodeCertificateRenewal(context.Background(), *before.NodeConnection)
			if point == "prepared" && err == nil {
				t.Fatal("preparation fault did not interrupt")
			}
			if point != "prepared" && err != nil {
				t.Fatal(err)
			}
			j, err := store.LoadNodeRenewalJournal()
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := store.LoadNodeRenewalCandidate(j.Generation)
			if err != nil {
				t.Fatal(err)
			}
			journalBytes, err := os.ReadFile(store.NodeRenewalJournalPath())
			if err != nil || bytes.Contains(journalBytes, candidate.PrivateKey) || bytes.Contains(journalBytes, candidate.CSR) {
				t.Fatal("journal contains private request material")
			}
			if point == "prepared" {
				request = NodeCertificateRenewal{Generation: j.Generation, CSRDER: candidate.CSR}
			}
			issued := issueRenewalForTest(t, material, request)
			if point != "prepared" {
				if err := svc.InstallNodeCertificateRenewal(context.Background(), *before.NodeConnection, request.Generation, issued); err == nil {
					t.Fatal("installation fault did not interrupt")
				}
			}
			restarted := New(cfg)
			if _, err := restarted.Init(); err != nil {
				t.Fatal("renewal blocked local startup")
			}
			state, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if point != "committed" {
				if !reflect.DeepEqual(before, state) {
					t.Fatal("uncommitted renewal changed state")
				}
				retry, err := restarted.PrepareNodeCertificateRenewal(context.Background(), *before.NodeConnection)
				if err != nil || retry.Generation != j.Generation || !bytes.Equal(retry.CSRDER, candidate.CSR) {
					t.Fatal("restart lost exact CSR")
				}
				if err := restarted.InstallNodeCertificateRenewal(context.Background(), *before.NodeConnection, retry.Generation, issued); err != nil {
					t.Fatal(err)
				}
			}
			after, err := store.Load()
			if err != nil || after.NodeConnection.CredentialGeneration != j.Generation || !reflect.DeepEqual(before.ManagedNode, after.ManagedNode) || !reflect.DeepEqual(before.Tunnels, after.Tunnels) || before.SessionSecret != after.SessionSecret || !reflect.DeepEqual(before.Warp, after.Warp) {
				t.Fatal("renewal changed local configuration or boot fencing")
			}
			if _, err := os.Lstat(store.NodeRenewalJournalPath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("committed renewal journal remained")
			}
			if _, err := store.LoadNodeIdentity(before.NodeConnection.CredentialGeneration); err != nil {
				t.Fatal("predecessor credentials removed")
			}
		})
	}
}

func TestNodeRenewalRejectsForeignResponseAndChangedBinding(t *testing.T) {
	svc, cfg, material, state := nodeRenewalFixture(t)
	request, err := svc.PrepareNodeCertificateRenewal(context.Background(), *state.NodeConnection)
	if err != nil {
		t.Fatal(err)
	}
	issued := issueRenewalForTest(t, material, request)
	for _, field := range []string{"serial", "chain", "key", "generation"} {
		t.Run(field, func(t *testing.T) {
			candidate := issued
			generation := request.Generation
			switch field {
			case "serial":
				candidate.Serial = "0"
			case "chain":
				candidate.ChainPEM = "invalid"
			case "key":
				candidate.CertificatePEM = string(mustNodeMaterial(t, cfg, state).Certificate)
			case "generation":
				generation = "00000000000000000000000000000000"
			}
			if err := svc.InstallNodeCertificateRenewal(context.Background(), *state.NodeConnection, generation, candidate); err == nil {
				t.Fatal("foreign response installed")
			}
		})
	}
	store := storage.New(cfg.ConfigDir)
	after, err := store.Load()
	if err != nil || !reflect.DeepEqual(state, after) {
		t.Fatal("invalid response changed state")
	}
	after.ManagedNode.BindingEpoch++
	if err := store.Save(after); err != nil {
		t.Fatal(err)
	}
	if err := svc.InstallNodeCertificateRenewal(context.Background(), *state.NodeConnection, request.Generation, issued); err == nil {
		t.Fatal("stale authority installed")
	}
	if _, err := svc.Init(); err != nil {
		t.Fatal("changed authority blocked local startup")
	}
}

func mustNodeMaterial(t *testing.T, cfg config.Config, state config.State) storage.NodeIdentityMaterial {
	t.Helper()
	m, err := storage.New(cfg.ConfigDir).LoadNodeIdentity(state.NodeConnection.CredentialGeneration)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestNodeRenewalCancellationAndSaveFailurePreserveState(t *testing.T) {
	svc, cfg, material, before := nodeRenewalFixture(t)
	lock, err := storage.AcquireStateMutationLock(cfg.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	_, err = svc.PrepareNodeCertificateRenewal(ctx, *before.NodeConnection)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("renewal lock did not cancel: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := svc.PrepareNodeCertificateRenewal(context.Background(), *before.NodeConnection)
	if err != nil {
		t.Fatal(err)
	}
	issued := issueRenewalForTest(t, material, request)
	svc.saveState = func(config.State) error { return errors.New("injected save failure") }
	if err := svc.InstallNodeCertificateRenewal(context.Background(), *before.NodeConnection, request.Generation, issued); err == nil {
		t.Fatal("save failure ignored")
	}
	after, err := storage.New(cfg.ConfigDir).Load()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed commit changed state")
	}
}

func TestExpiredCommittedRenewalDoesNotBlockLocalStartup(t *testing.T) {
	cfg := controllerTestConfig(t)
	svc := New(cfg)
	state, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	material, pin, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 9443}, time.Now().Add(-50*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := controlpki.IssueNodeCertificate(material, csr, time.Now().Add(-40*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	generation := "22222222222222222222222222222222"
	state.Mode = config.ModeNode
	state.ManagedNode = &config.ManagedNodeState{ControllerID: uuid.NewString(), NodeID: uuid.NewString(), StateEpoch: uuid.NewString(), BindingEpoch: 1}
	state.NodeConnection = &config.NodeConnectionState{ControllerURL: "https://127.0.0.1:9443", CAPin: pin, CredentialGeneration: generation}
	store := storage.New(cfg.ConfigDir)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := store.SaveNodeRenewalCandidate(generation, storage.NodeRenewalCandidate{PrivateKey: keyPEM, CSR: csr}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNodeIdentity(generation, storage.NodeIdentityMaterial{CACert: material.CACert, PrivateKey: keyPEM, Certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.DER})}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNodeRenewalJournal(storage.NodeRenewalJournal{PredecessorGeneration: "11111111111111111111111111111111", Generation: generation, ControllerID: state.ManagedNode.ControllerID, NodeID: state.ManagedNode.NodeID, BindingEpoch: 1, StateEpoch: state.ManagedNode.StateEpoch, ControllerURL: state.NodeConnection.ControllerURL, CAPin: pin}); err != nil {
		t.Fatal(err)
	}
	after, err := New(cfg).Init()
	if err != nil || !reflect.DeepEqual(state.Tunnels, after.Tunnels) {
		t.Fatal("expired renewal interrupted local forwarding startup")
	}
	if _, err := os.Lstat(store.NodeRenewalJournalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("committed renewal not recovered")
	}
}
