package backup

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/google/uuid"
)

func TestNodeCredentialBackupAndDetach(t *testing.T) {
	cfg := testConfig(t)
	service := app.New(cfg)
	material, pin, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 9443}, time.Now())
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
	cert, err := controlpki.IssueNodeCertificate(material, csr, time.Now())
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
	archive, err := Create(context.Background(), cfg, service, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	validated, err := validateBackupData(context.Background(), testPassword, archive.Data)
	if err != nil {
		t.Fatal(err)
	}
	var nodeFiles int
	for _, file := range validated.Files {
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
