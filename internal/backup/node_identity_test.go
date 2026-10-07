package backup

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/google/uuid"
)

func TestNodeCredentialBackupAndDetach(t *testing.T) {
	cfg := testConfig(t)
	service := app.New(cfg)
	issuedAt := time.Now().Add(-21 * 24 * time.Hour)
	material, pin, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 9443}, issuedAt)
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
	cert, err := controlpki.IssueNodeCertificate(material, csr, issuedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	invitation := controlapi.Invitation{ControllerURL: "https://127.0.0.1:9443", CAPin: pin, CACertPEM: string(material.CACert)}
	approved := controlapi.EnrollmentStatus{Status: "approved", ContractVersion: 1, NodeID: uuid.NewString(), ControllerID: uuid.NewString(), BindingEpoch: 1, Certificate: &controlapi.IssuedCertificate{CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.DER})), ChainPEM: string(material.CACert), Serial: cert.Serial, NotBefore: cert.NotBefore, NotAfter: cert.NotAfter}}
	if err := service.InstallNodeEnrollment(context.Background(), invitation, approved, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
		t.Fatal(err)
	}
	before, err := storage.New(cfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	renewal, err := service.PrepareNodeCertificateRenewal(context.Background(), *before.NodeConnection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), cfg, service, testPassword, Options{}); err == nil {
		t.Fatal("pending renewal was archived")
	}
	if _, err := createFromState(context.Background(), cfg, before, testPassword, Options{}); err == nil {
		t.Fatal("cached state bypassed renewal gate")
	}
	replacement, err := controlpki.IssueNodeCertificate(material, renewal.CSRDER, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	renewed := controlapi.IssuedCertificate{CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: replacement.DER})), ChainPEM: string(material.CACert), Serial: replacement.Serial, NotBefore: replacement.NotBefore, NotAfter: replacement.NotAfter}
	if err := service.InstallNodeCertificateRenewal(context.Background(), *before.NodeConnection, renewal.Generation, renewed); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, service, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	validated, err := validateBackupData(context.Background(), testPassword, archive.Data)
	if err != nil {
		t.Fatal(err)
	}
	if validated.State.NodeConnection.CredentialGeneration != renewal.Generation {
		t.Fatal("backup rolled credentials back")
	}
	var nodeFiles int
	for _, file := range validated.Files {
		if strings.Contains(file.Path, before.NodeConnection.CredentialGeneration) || strings.HasPrefix(file.Path, "node-renewal/") || file.Path == storage.NodeRenewalJournalFileName {
			t.Fatal("backup included predecessor or candidate material")
		}
		if len(file.Path) > 5 && file.Path[:5] == "node/" {
			nodeFiles++
		}
	}
	if nodeFiles != 3 {
		t.Fatalf("credential archive files: %d", nodeFiles)
	}
	malicious := append(append([]restoreFile(nil), validated.Files...), restoreFile{Path: "node/extra/key.pem", Data: der})
	if err := validateNodeArchive(malicious, validated.State); err == nil {
		t.Fatal("unreferenced private credentials accepted")
	}
	archivePath := filepath.Join(t.TempDir(), "node.afbackup")
	if err := os.WriteFile(archivePath, archive.Data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), cfg, testPassword, archivePath); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.New(cfg.ConfigDir).LoadNodeIdentity(validated.State.NodeConnection.CredentialGeneration); err != nil {
		t.Fatal("restored credentials unavailable")
	}
	current, err := storage.New(cfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := service.BeginNodeRecovery(context.Background(), current.ManagedNode.NodeID, current.ManagedNode.ControllerID)
	if err != nil {
		t.Fatal(err)
	}
	approved.NodeID = uuid.NewString()
	if err := recovery.InstallNodeEnrollment(context.Background(), invitation, approved, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
		t.Fatal(err)
	}
	if err := recovery.Close(); err != nil {
		t.Fatal(err)
	}
	rebound, err := storage.New(cfg.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), cfg, testPassword, archivePath); !errors.Is(err, app.ErrManagedNodeRestoreIdentityConflict) {
		t.Fatal("archived old binding restored over fresh enrollment")
	}
	unchanged, err := storage.New(cfg.ConfigDir).Load()
	if err != nil || !reflect.DeepEqual(rebound, unchanged) {
		t.Fatal("rejected old backup changed fresh binding")
	}
	if _, err := RestoreWithOptions(context.Background(), cfg, testPassword, archivePath, RestoreOptions{DetachManagedNode: true}); err != nil {
		t.Fatal(err)
	}
	detached, err := storage.New(cfg.ConfigDir).Load()
	if err != nil || detached.ManagedNode != nil || detached.NodeConnection != nil {
		t.Fatal("detach restored node authority")
	}
	if err := app.New(cfg).PreflightNodeEnrollment(context.Background()); err != nil {
		t.Fatal("detached configuration cannot enroll")
	}
}

func TestNodeRecoveryBackupRestoreFences(t *testing.T) {
	cfg := testConfig(t)
	svc := app.New(cfg)
	_, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, svc, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	store := storage.New(cfg.ConfigDir)
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	evidence := []byte("malformed evidence must remain")
	if err := os.WriteFile(store.NodeRecoveryJournalPath(), evidence, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), cfg, svc, testPassword, Options{}); err == nil {
		t.Fatal("pending recovery archived or recovered by backup")
	}
	if _, err := createFromState(context.Background(), cfg, state, testPassword, Options{}); err == nil {
		t.Fatal("cached state bypassed recovery fence")
	}
	if err := Restore(context.Background(), cfg, testPassword, writeTempArchive(t, archive.Data)); err == nil {
		t.Fatal("restore replaced pending recovery evidence")
	}
	got, err := os.ReadFile(store.NodeRecoveryJournalPath())
	if err != nil || string(got) != string(evidence) {
		t.Fatal("evidence changed")
	}
	after, err := store.Load()
	if err != nil || !reflect.DeepEqual(state, after) {
		t.Fatal("blocked backup/restore changed state")
	}
	files := []restoreFile{{Path: storage.NodeRecoveryJournalFileName, Data: evidence}}
	if err := validateNodeArchive(files, config.State{}); err == nil {
		t.Fatal("recovery journal admitted to archive")
	}
	if _, err := safeRestorePath(cfg.ConfigDir, storage.NodeRecoveryJournalFileName); err == nil {
		t.Fatal("recovery journal admitted by restore path")
	}
	validated, err := validateBackupData(context.Background(), testPassword, archive.Data)
	if err != nil {
		t.Fatal(err)
	}
	mutated := append(append([]restoreFile(nil), validated.Files...), files...)
	if _, err := validateBackupData(context.Background(), testPassword, rebuildControlArchive(t, validated.Metadata, mutated)); err == nil {
		t.Fatal("encrypted archive carried recovery evidence")
	}
}
