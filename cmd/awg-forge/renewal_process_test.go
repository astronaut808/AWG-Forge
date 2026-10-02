package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/base32"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/pquerna/otp/totp"
)

// TestRenewalProcesses proves that a separately started node process resumes a
// durable renewal candidate, switches its mTLS identity, and stays locally
// available after the controller revokes that identity.
func TestRenewalProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("process integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	binary := buildEnrollmentProcessBinary(ctx, t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	controllerDir, nodeDir := filepath.Join(root, "controller"), filepath.Join(root, "node")
	browserPort, controlPort, nodePort := enrollmentFreePort(t), enrollmentFreePort(t), enrollmentFreePort(t)
	controllerConfig := enrollmentProcessConfig(controllerDir, browserPort)
	controllerService := app.New(controllerConfig)
	now := time.Now().UTC()
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("renewal-process-totp"))
	code, err := totp.GenerateCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	activation, err := controllerService.ActivateController(ctx, app.ControllerActivationRequest{
		Username: "admin", Password: "correct horse battery staple", TOTPSecret: secret, TOTPConfirmation: code, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	control, err := controllerService.PrepareControlIdentity(ctx, app.ControlIdentityRequest{
		BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: controlPort, Now: now.Add(-21 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	controller := enrollmentStartProcess(ctx, t, binary, enrollmentEnv(controllerDir, browserPort), "serve")
	defer controller.stop(t)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", browserPort)
	enrollmentWaitHTTP(ctx, t, baseURL+"/api/state")
	browser := enrollmentBrowserClient(activation.Authentication.Token, baseURL)
	backupRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/controller/control/backup", bytes.NewBufferString(`{"password":"process backup password"}`))
	if err != nil {
		t.Fatal(err)
	}
	backupRequest.Header.Set("Content-Type", "application/json")
	backupResponse, err := browser.Do(backupRequest)
	if err != nil {
		t.Fatal(err)
	}
	encryptedBackup, readErr := io.ReadAll(io.LimitReader(backupResponse.Body, (64<<20)+1))
	closeErr := backupResponse.Body.Close()
	if readErr != nil || closeErr != nil || len(encryptedBackup) > 64<<20 {
		t.Fatal("control backup download failed")
	}
	if backupResponse.StatusCode != http.StatusOK {
		t.Fatalf("control backup status = %d", backupResponse.StatusCode)
	}
	receipt := backupResponse.Header.Get("X-Control-Enable-Receipt")
	if receipt == "" {
		t.Fatal("control backup receipt missing")
	}
	if err := os.WriteFile(filepath.Join(root, "controller.afbackup"), encryptedBackup, 0600); err != nil {
		t.Fatal(err)
	}
	var ignored struct{}
	enrollmentJSON(ctx, t, browser, http.MethodPost, baseURL+"/api/controller/control/enable", map[string]any{"receipt": receipt, "backup_saved": true}, &ignored)

	material, err := storage.New(controllerDir).LoadControlIdentity(control.CAGeneration, control.ServerGeneration)
	if err != nil {
		t.Fatal(err)
	}
	oldCSR, oldKey := renewalProcessCSR(t)
	oldIssued, err := controlpki.IssueNodeCertificate(material, oldCSR, now.Add(-21*24*time.Hour+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	controllerState, err := controllerService.State()
	if err != nil || controllerState.Controller == nil {
		t.Fatalf("controller state unavailable: %v", err)
	}
	nodeID := "11111111-1111-4111-8111-111111111111"
	db, err := sqldb.Open(ctx, controllerConfig)
	if err != nil {
		t.Fatal(err)
	}
	identity := sqldb.NodeIdentity{ControllerID: controllerState.Controller.ControllerID, NodeID: nodeID, BindingEpoch: 1}
	if _, err := db.RegisterInitialNodeCertificate(ctx, identity, control.CAGeneration, oldIssued, now.Add(-21*24*time.Hour+time.Second)); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	nodeConfig := enrollmentProcessConfig(nodeDir, nodePort)
	nodeService := app.New(nodeConfig)
	if _, err := nodeService.Init(); err != nil {
		t.Fatal(err)
	}
	oldKeyDER, err := x509.MarshalPKCS8PrivateKey(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	invitation := controlapi.Invitation{ControllerURL: "https://127.0.0.1:" + fmt.Sprint(controlPort), CAPin: control.CAPin, CACertPEM: string(material.CACert)}
	approved := controlapi.EnrollmentStatus{Status: "approved", ContractVersion: 1, NodeID: nodeID, ControllerID: identity.ControllerID, BindingEpoch: 1, Certificate: &controlapi.IssuedCertificate{
		CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: oldIssued.DER})), ChainPEM: string(material.CACert), Serial: oldIssued.Serial, NotBefore: oldIssued.NotBefore, NotAfter: oldIssued.NotAfter,
	}}
	if err := nodeService.InstallNodeEnrollment(ctx, invitation, approved, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: oldKeyDER})); err != nil {
		t.Fatal(err)
	}
	before, err := nodeService.NodeAgentState(ctx)
	if err != nil || before.NodeConnection == nil || before.ManagedNode == nil {
		t.Fatalf("node enrollment state unavailable: %v", err)
	}
	prepared, err := nodeService.PrepareNodeCertificateRenewal(ctx, *before.NodeConnection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.New(nodeDir).LoadNodeRenewalJournal(); err != nil {
		t.Fatalf("durable renewal candidate unavailable: %v", err)
	}

	nodeEnv := append(enrollmentEnv(nodeDir, nodePort), "EXTERNAL_INTERFACE=lo")
	node := enrollmentStartProcess(ctx, t, binary, nodeEnv, "serve")
	defer node.stop(t)
	renewed := renewalProcessWaitGeneration(ctx, t, nodeDir, prepared.Generation)
	if renewed.ManagedNode.BootSequence != before.ManagedNode.BootSequence+1 {
		t.Fatalf("renewal boot sequence = %d, want %d", renewed.ManagedNode.BootSequence, before.ManagedNode.BootSequence+1)
	}
	if renewed.SessionSecret != before.SessionSecret || !renewalProcessSameTunnelConfig(before, renewed) || !reflect.DeepEqual(before.Warp, renewed.Warp) {
		t.Fatal("renewal changed local tunnel configuration or session state")
	}
	expectedManaged := *before.ManagedNode
	expectedManaged.BootSequence++
	if !reflect.DeepEqual(expectedManaged, *renewed.ManagedNode) {
		t.Fatal("renewal changed node authority or desired-state fencing")
	}
	newIdentity, err := storage.New(nodeDir).LoadNodeIdentity(prepared.Generation)
	if err != nil {
		t.Fatal(err)
	}
	newCertificate := renewalProcessCertificate(t, newIdentity.Certificate)
	newCSR, _, err := controlpki.ParseNodeCSR(prepared.CSRDER)
	if err != nil || !bytes.Equal(newCertificate.RawSubjectPublicKeyInfo, newCSR.RawSubjectPublicKeyInfo) {
		t.Fatalf("renewed certificate does not use durable CSR: %v", err)
	}
	if got := renewalProcessCertificateCount(ctx, t, controllerConfig.DatabasePath, nodeID); got != 2 {
		t.Fatalf("node certificate count = %d, want 2", got)
	}
	if !renewalProcessActiveCertificate(ctx, t, controllerConfig.DatabasePath, nodeID, newCertificate.Raw) {
		t.Fatal("renewed certificate is not the active controller credential")
	}
	firstExpiry := enrollmentPresenceExpiry(ctx, t, controllerConfig.DatabasePath, nodeID)
	enrollmentWaitPresenceRefresh(ctx, t, controllerConfig.DatabasePath, nodeID, firstExpiry)

	node.stop(t)
	node = enrollmentStartProcess(ctx, t, binary, nodeEnv, "serve")
	defer node.stop(t)
	if got := enrollmentWaitPresenceAfter(ctx, t, controllerConfig.DatabasePath, nodeID, renewed.ManagedNode.BootSequence); got != renewed.ManagedNode.BootSequence+1 {
		t.Fatalf("restarted node boot sequence = %d, want %d", got, renewed.ManagedNode.BootSequence+1)
	}
	afterRestart, err := storage.New(nodeDir).Load()
	if err != nil || afterRestart.NodeConnection == nil || afterRestart.NodeConnection.CredentialGeneration != prepared.Generation {
		t.Fatalf("restart changed renewed credentials: %v", err)
	}

	db, err = sqldb.Open(ctx, controllerConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeNodeBinding(ctx, identity, time.Now().UTC()); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := node.waitFor("node.agent.unavailable", 25*time.Second); err != nil {
		t.Fatal("revoked node worker did not stop")
	}
	enrollmentWaitHTTP(ctx, t, fmt.Sprintf("http://127.0.0.1:%d/api/state", nodePort))
}

func renewalProcessCSR(t *testing.T) ([]byte, ed25519.PrivateKey) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return csr, key
}

func renewalProcessWaitGeneration(ctx context.Context, t *testing.T, dir, generation string) config.State {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			t.Fatal("renewal process test deadline reached")
		}
		state, err := storage.New(dir).Load()
		if err == nil && state.NodeConnection != nil && state.NodeConnection.CredentialGeneration == generation {
			return state
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("node renewal was not committed")
	return config.State{}
}

func renewalProcessSameTunnelConfig(before, after config.State) bool {
	if len(before.Tunnels) != len(after.Tunnels) {
		return false
	}
	for i := range before.Tunnels {
		previous, current := before.Tunnels[i], after.Tunnels[i]
		// serve renders existing tunnels before starting the worker. Compare
		// all configuration fields while allowing these startup timestamps.
		previous.LastRenderAt, current.LastRenderAt = time.Time{}, time.Time{}
		previous.UpdatedAt, current.UpdatedAt = time.Time{}, time.Time{}
		if !reflect.DeepEqual(previous, current) {
			return false
		}
	}
	return true
}

func renewalProcessCertificate(t *testing.T, certificatePEM []byte) *x509.Certificate {
	t.Helper()
	block, rest := pem.Decode(certificatePEM)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		t.Fatal("invalid certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

func renewalProcessCertificateCount(ctx context.Context, t *testing.T, path, nodeID string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM control_node_certificates WHERE node_id = ?", nodeID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func renewalProcessActiveCertificate(ctx context.Context, t *testing.T, path, nodeID string, certificateDER []byte) bool {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM control_node_certificates
WHERE node_id = ? AND certificate_der = ? AND revoked_at_unix_ms IS NULL AND superseded_at_unix_ms IS NULL`, nodeID, certificateDER).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count == 1
}
