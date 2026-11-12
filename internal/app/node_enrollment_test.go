package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/google/uuid"
)

func TestValidateNodeApprovalRejectsMismatchedPin(t *testing.T) {
	endpoint := controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 9443}
	material, pin, err := controlpki.Generate(endpoint, time.Now().UTC())
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
	issued, err := controlpki.IssueNodeCertificate(material, csr, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	status := controlapi.EnrollmentStatus{Status: "approved", NodeID: uuid.NewString(), ControllerID: uuid.NewString(), BindingEpoch: 1, ContractVersion: 1, Certificate: &controlapi.IssuedCertificate{CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issued.DER})), ChainPEM: string(material.CACert), Serial: issued.Serial, NotBefore: issued.NotBefore, NotAfter: issued.NotAfter}}
	status.ContractVersion = 1
	status.Certificate.ChainPEM = string(material.CACert)
	status.Certificate.Serial = issued.Serial
	status.Certificate.NotBefore = issued.NotBefore
	status.Certificate.NotAfter = issued.NotAfter
	invitation := controlapi.Invitation{ControllerURL: "https://127.0.0.1:9443", CAPin: pin, CACertPEM: string(material.CACert)}
	if err := validateNodeApproval(invitation, status, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
		t.Fatalf("valid approval = %v", err)
	}
	invitation.CAPin = "sha256:" + "0000000000000000000000000000000000000000000000000000000000000000"
	if err := validateNodeApproval(invitation, status, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err == nil {
		t.Fatal("mismatched pin accepted")
	}
}

func nodeApprovalFixture(t *testing.T) (controlapi.Invitation, controlapi.EnrollmentStatus, []byte) {
	t.Helper()
	return nodeApprovalFixtureAt(t, time.Now())
}

func nodeApprovalFixtureAt(t *testing.T, now time.Time) (controlapi.Invitation, controlapi.EnrollmentStatus, []byte) {
	t.Helper()
	material, pin, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 9443}, now)
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
	issued, err := controlpki.IssueNodeCertificate(material, csr, now)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return controlapi.Invitation{ControllerURL: "https://127.0.0.1:9443", CAPin: pin, CACertPEM: string(material.CACert)}, controlapi.EnrollmentStatus{Status: "approved", ContractVersion: 1, NodeID: uuid.NewString(), ControllerID: uuid.NewString(), BindingEpoch: 1, Certificate: &controlapi.IssuedCertificate{CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issued.DER})), ChainPEM: string(material.CACert), Serial: issued.Serial, NotBefore: issued.NotBefore, NotAfter: issued.NotAfter}}, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}
func TestNodeEnrollmentPreservesExistingConfigurationAndFreshNodeHasNoTunnel(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing", true: "fresh"}[fresh], func(t *testing.T) {
			cfg := controllerTestConfig(t)
			service := New(cfg)
			var before config.State
			if !fresh {
				var err error
				before, err = service.Init()
				if err != nil {
					t.Fatal(err)
				}
			}
			invitation, approved, key := nodeApprovalFixture(t)
			if err := service.InstallNodeEnrollment(context.Background(), invitation, approved, key); err != nil {
				t.Fatal(err)
			}
			after, err := service.Init()
			if err != nil {
				t.Fatal(err)
			}
			if fresh && len(after.Tunnels) != 0 {
				t.Fatal("fresh node created a tunnel")
			}
			if !fresh && (!reflect.DeepEqual(before.Tunnels, after.Tunnels) || before.SessionSecret != after.SessionSecret || !reflect.DeepEqual(before.Warp, after.Warp)) {
				t.Fatal("enrollment changed local configuration")
			}
			if after.EffectiveMode() != config.ModeNode || after.NodeConnection == nil {
				t.Fatal("enrollment authority missing")
			}
			detached, _, err := PrepareRestoredState(&after, after, true, time.Now())
			if err != nil || detached.NodeConnection != nil || detached.ManagedNode != nil {
				t.Fatal("detach retained node authority")
			}
			if _, err := os.Lstat(storage.New(cfg.ConfigDir).NodeIdentityJournalPath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("completed enrollment retained journal")
			}
			if err := storage.New(cfg.ConfigDir).SaveNodeIdentityJournal(storage.NodeIdentityJournal{Generation: after.NodeConnection.CredentialGeneration}); err != nil {
				t.Fatal(err)
			}
			if _, err := service.Init(); err != nil {
				t.Fatal("committed enrollment journal did not recover")
			}
		})
	}
}
func TestNodeEnrollmentIncompleteCredentialsFenceStartup(t *testing.T) {
	cfg := controllerTestConfig(t)
	service := New(cfg)
	_, err := service.Init()
	if err != nil {
		t.Fatal(err)
	}
	before, err := storage.New(cfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	invitation, approved, key := nodeApprovalFixture(t)
	// A hostile node directory prevents credential installation after durable journal publication.
	if err := os.Symlink(cfg.ConfigDir, cfg.ConfigDir+"/node"); err != nil {
		t.Fatal(err)
	}
	if err := service.InstallNodeEnrollment(context.Background(), invitation, approved, key); err == nil {
		t.Fatal("unsafe node directory accepted")
	}
	stored, err := storage.New(cfg.ConfigDir).Load()
	if err != nil || !reflect.DeepEqual(before, stored) {
		t.Fatal("failed enrollment changed state")
	}
	if _, err := service.Init(); err == nil {
		t.Fatal("incomplete journal allowed startup")
	}
	journal, err := storage.New(cfg.ConfigDir).LoadNodeIdentityJournal()
	if err != nil || len(journal.Generation) != 32 || strings.Contains(journal.Generation, string(key)) {
		t.Fatal("failure journal missing or unsafe")
	}
}

func TestCommittedExpiredNodeJournalPreservesLocalStartup(t *testing.T) {
	cfg := controllerTestConfig(t)
	service := New(cfg)
	before, err := service.Init()
	if err != nil {
		t.Fatal(err)
	}
	invitation, approved, key := nodeApprovalFixtureAt(t, time.Now().Add(-45*24*time.Hour))
	generation := strings.Repeat("a", 32)
	store := storage.New(cfg.ConfigDir)
	material := storage.NodeIdentityMaterial{CACert: []byte(invitation.CACertPEM), Certificate: []byte(approved.Certificate.CertificatePEM), PrivateKey: key}
	connection := &config.NodeConnectionState{ControllerURL: invitation.ControllerURL, CAPin: invitation.CAPin, CredentialGeneration: generation}
	if err := ValidateNodeIdentity(connection, material, false); err == nil {
		t.Fatal("expired credentials authorize a connection")
	}
	if err := store.SaveNodeIdentity(generation, material); err != nil {
		t.Fatal(err)
	}
	before.Mode = config.ModeNode
	before.ManagedNode = &config.ManagedNodeState{ControllerID: approved.ControllerID, NodeID: approved.NodeID, BindingEpoch: 1, StateEpoch: uuid.NewString()}
	before.NodeConnection = connection
	if err := store.Save(before); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNodeIdentityJournal(storage.NodeIdentityJournal{Generation: generation}); err != nil {
		t.Fatal(err)
	}
	after, err := New(cfg).Init()
	if err != nil {
		t.Fatalf("expired committed identity blocked local startup: %v", err)
	}
	if !reflect.DeepEqual(before.Tunnels, after.Tunnels) || before.SessionSecret != after.SessionSecret {
		t.Fatal("local configuration changed")
	}
	if _, err := os.Lstat(store.NodeIdentityJournalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("committed journal not recovered")
	}
}

func TestNodeWorkerOperationsCancelWhileMutationLockIsHeld(t *testing.T) {
	cfg := controllerTestConfig(t)
	service := New(cfg)
	invitation, approved, key := nodeApprovalFixture(t)
	if err := service.InstallNodeEnrollment(context.Background(), invitation, approved, key); err != nil {
		t.Fatal(err)
	}
	store := storage.New(cfg.ConfigDir)
	before, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	lock, err := storage.AcquireStateMutationLock(cfg.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	for _, operation := range []string{"state", "boot"} {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		started := time.Now()
		if operation == "state" {
			_, err = service.NodeAgentState(ctx)
		} else {
			_, err = service.StartManagedNodeBootContext(ctx)
		}
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
			t.Fatalf("%s ignored cancellation: %v", operation, err)
		}
		if !service.mu.TryLock() {
			t.Fatal("cancellation retained service mutex")
		}
		service.mu.Unlock()
	}
	after, err := store.Load()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("canceled worker changed committed state")
	}
}
