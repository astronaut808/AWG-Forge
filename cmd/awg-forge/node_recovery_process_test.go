package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/backup"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/pquerna/otp/totp"
)

// TestNodeRecoveryProcesses exercises the Linux-root offline commands through
// separate binaries. Invitation bodies and child-process output remain in the
// temporary test directory because they can contain enrollment credentials.
func TestNodeRecoveryProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("process integration test")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("node local recovery requires Linux root")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	binary := buildEnrollmentProcessBinary(ctx, t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	controllerDir, nodeDir := filepath.Join(root, "controller"), filepath.Join(root, "node")
	browserPort, controlPort := enrollmentFreePort(t), enrollmentFreePort(t)
	cfg := enrollmentProcessConfig(controllerDir, browserPort)
	activation := nodeRecoveryActivateController(ctx, t, cfg)
	controller := enrollmentStartProcess(ctx, t, binary, enrollmentEnv(controllerDir, browserPort), "serve")
	defer func() { controller.stop(t) }()
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", browserPort)
	enrollmentWaitHTTP(ctx, t, baseURL+"/api/state")
	client := enrollmentBrowserClient(activation.Authentication.Token, baseURL)
	var ignored struct{}
	enrollmentJSON(ctx, t, client, http.MethodPost, baseURL+"/api/controller/control/prepare", map[string]any{"bind_ip": "127.0.0.1", "advertised": "127.0.0.1", "port": controlPort}, &ignored)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/controller/control/backup", bytes.NewBufferString(`{"password":"node recovery process backup password"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("X-Control-Enable-Receipt") == "" {
		t.Fatal("controller control backup was not accepted")
	}
	enrollmentJSON(ctx, t, client, http.MethodPost, baseURL+"/api/controller/control/enable", map[string]any{"receipt": response.Header.Get("X-Control-Enable-Receipt"), "backup_saved": true}, &ignored)

	initialInvitation := nodeRecoveryInvitation(ctx, t, client, baseURL)
	initialPath := nodeRecoveryWriteInvitation(t, root, "initial.json", initialInvitation)
	initial := enrollmentStartProcess(ctx, t, binary, enrollmentEnv(nodeDir, enrollmentFreePort(t)), "node", "enroll", "--input-file", initialPath, "--name", "node")
	nodeRecoveryApprove(ctx, t, client, baseURL, initialInvitation, initial)
	if err := initial.wait(20 * time.Second); err != nil {
		t.Fatal("initial enrollment failed")
	}
	before, err := storage.New(nodeDir).Load()
	if err != nil || before.ManagedNode == nil || before.NodeConnection == nil {
		t.Fatalf("initial node state unavailable: %v", err)
	}

	// The serving process owns StateLock, so even root cannot start an offline
	// recovery session while it is alive.
	nodePort := enrollmentFreePort(t)
	nodeServe := enrollmentStartProcess(ctx, t, binary, enrollmentEnv(nodeDir, nodePort), "serve")
	defer func() { nodeServe.stop(t) }()
	firstSequence := enrollmentWaitPresence(ctx, t, cfg.DatabasePath, before.ManagedNode.NodeID)
	before, err = storage.New(nodeDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	blocked := enrollmentStartProcess(ctx, t, binary, enrollmentEnv(nodeDir, enrollmentFreePort(t)), "node", "detach", "--confirm-node-id", before.ManagedNode.NodeID, "--confirm-controller-id", before.ManagedNode.ControllerID)
	if err := blocked.wait(15 * time.Second); err == nil {
		t.Fatal("live server did not block root offline recovery")
	}
	afterBlocked, err := storage.New(nodeDir).Load()
	if err != nil || !reflect.DeepEqual(before, afterBlocked) {
		t.Fatalf("blocked recovery changed local binding: %v", err)
	}
	nodeServe.stop(t)

	// Cancellation after the operator has compared the code still occurs before
	// approval and cannot replace the captured local binding.
	canceledInvitation := nodeRecoveryInvitation(ctx, t, client, baseURL)
	canceledPath := nodeRecoveryWriteInvitation(t, root, "canceled.json", canceledInvitation)
	canceled := enrollmentStartProcess(ctx, t, binary, enrollmentEnv(nodeDir, enrollmentFreePort(t)), "node", "rebind", "--confirm-node-id", before.ManagedNode.NodeID, "--confirm-controller-id", before.ManagedNode.ControllerID, "--input-file", canceledPath, "--name", "node")
	nodeRecoveryAwaitReview(ctx, t, client, baseURL, canceledInvitation, canceled)
	canceled.stop(t)
	if err := canceled.wait(5 * time.Second); err == nil {
		t.Fatal("cancelled rebind succeeded")
	}
	afterCanceled, err := storage.New(nodeDir).Load()
	if err != nil || !reflect.DeepEqual(before, afterCanceled) {
		t.Fatalf("cancelled rebind changed old binding: %v", err)
	}

	// An invalid pinned invitation terminates before a network claim/approval
	// and retains the previous local authority exactly.
	failedInvitation := nodeRecoveryInvitation(ctx, t, client, baseURL)
	failedInvitation.CAPin = strings.Repeat("0", len(failedInvitation.CAPin))
	failedPath := nodeRecoveryWriteInvitation(t, root, "failed.json", failedInvitation)
	claimsBefore := enrollmentClaimCount(ctx, t, cfg.DatabasePath)
	failed := enrollmentStartProcess(ctx, t, binary, enrollmentEnv(nodeDir, enrollmentFreePort(t)), "node", "rebind", "--confirm-node-id", before.ManagedNode.NodeID, "--confirm-controller-id", before.ManagedNode.ControllerID, "--input-file", failedPath, "--name", "node")
	if err := failed.wait(20 * time.Second); err == nil {
		t.Fatal("invalid pinned invitation rebind succeeded")
	}
	if claimsAfter := enrollmentClaimCount(ctx, t, cfg.DatabasePath); claimsAfter != claimsBefore {
		t.Fatalf("failed rebind created a claim: before=%d after=%d", claimsBefore, claimsAfter)
	}
	afterFailed, err := storage.New(nodeDir).Load()
	if err != nil || !reflect.DeepEqual(before, afterFailed) {
		t.Fatalf("failed rebind changed old binding: %v", err)
	}

	// A prior registry revocation is immutable history. A fresh approved
	// invitation receives a different node and state epoch instead of reviving it.
	db, err := sqldb.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	oldIdentity := sqldb.NodeIdentity{ControllerID: before.ManagedNode.ControllerID, NodeID: before.ManagedNode.NodeID, BindingEpoch: before.ManagedNode.BindingEpoch}
	if err := db.RevokeNodeBinding(ctx, oldIdentity, time.Now().UTC()); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	rebindInvitation := nodeRecoveryInvitation(ctx, t, client, baseURL)
	rebindPath := nodeRecoveryWriteInvitation(t, root, "rebind.json", rebindInvitation)
	rebind := enrollmentStartProcess(ctx, t, binary, enrollmentEnv(nodeDir, enrollmentFreePort(t)), "node", "rebind", "--confirm-node-id", before.ManagedNode.NodeID, "--confirm-controller-id", before.ManagedNode.ControllerID, "--input-file", rebindPath, "--name", "replacement")
	nodeRecoveryApprove(ctx, t, client, baseURL, rebindInvitation, rebind)
	if err := rebind.wait(20 * time.Second); err != nil {
		t.Fatal("approved rebind failed")
	}
	after, err := storage.New(nodeDir).Load()
	if err != nil || after.ManagedNode == nil || after.NodeConnection == nil {
		t.Fatalf("rebound node state unavailable: %v", err)
	}
	if after.ManagedNode.NodeID == before.ManagedNode.NodeID || after.ManagedNode.StateEpoch == before.ManagedNode.StateEpoch {
		t.Fatal("rebind reused the old node identity or state epoch")
	}
	nodeRecoveryAssertPreservedConfiguration(t, before, after)
	nodeRecoveryAssertRevokedBinding(ctx, t, cfg.DatabasePath, oldIdentity)

	// A cold controller restore revokes its archived credentials. Root recovery
	// creates a new administrator, after which a newly issued invitation can
	// bind this stopped node again without reviving either prior node identity.
	archivePath := filepath.Join(root, "controller.afbackup")
	if _, err := backup.WriteFile(ctx, cfg, app.New(cfg), "node recovery process backup password", archivePath); err != nil {
		t.Fatal(err)
	}
	controller.stop(t)
	if err := backup.Restore(ctx, cfg, "node recovery process backup password", archivePath); err != nil {
		t.Fatal(err)
	}
	recoveryInput := nodeRecoveryWriteRootInput(t, root)
	recoverAdmin := enrollmentStartProcess(ctx, t, binary, enrollmentEnv(controllerDir, browserPort), "controller", "recover-admin", "--input-file", recoveryInput)
	if err := recoverAdmin.wait(20 * time.Second); err != nil {
		t.Fatal("controller administrator recovery failed")
	}
	recoveredSecret := nodeRecoveryRecoverySecret(t, recoverAdmin)
	controller = enrollmentStartProcess(ctx, t, binary, enrollmentEnv(controllerDir, browserPort), "serve")
	enrollmentWaitHTTP(ctx, t, baseURL+"/api/state")
	client = nodeRecoveryRecoveredClient(ctx, t, baseURL, recoveredSecret)
	nodeRecoveryEnableRestoredControl(ctx, t, client, baseURL)

	restoredInvitation := nodeRecoveryInvitation(ctx, t, client, baseURL)
	restoredPath := nodeRecoveryWriteInvitation(t, root, "restored-rebind.json", restoredInvitation)
	restoredRebind := enrollmentStartProcess(ctx, t, binary, enrollmentEnv(nodeDir, enrollmentFreePort(t)), "node", "rebind", "--confirm-node-id", after.ManagedNode.NodeID, "--confirm-controller-id", after.ManagedNode.ControllerID, "--input-file", restoredPath, "--name", "restored-replacement")
	nodeRecoveryApprove(ctx, t, client, baseURL, restoredInvitation, restoredRebind)
	if err := restoredRebind.wait(20 * time.Second); err != nil {
		t.Fatal("post-restore fresh rebind failed")
	}
	afterRestore, err := storage.New(nodeDir).Load()
	if err != nil || afterRestore.ManagedNode == nil || afterRestore.NodeConnection == nil {
		t.Fatalf("post-restore rebound node state unavailable: %v", err)
	}
	if afterRestore.ManagedNode.NodeID == after.ManagedNode.NodeID || afterRestore.ManagedNode.StateEpoch == after.ManagedNode.StateEpoch {
		t.Fatal("post-restore rebind reused the revoked node identity or state epoch")
	}
	nodeRecoveryAssertPreservedConfiguration(t, after, afterRestore)
	after = afterRestore

	// Restarting the rebound node produces fresh presence under the new identity.
	nodeServe = enrollmentStartProcess(ctx, t, binary, enrollmentEnv(nodeDir, nodePort), "serve")
	firstReboundSequence := enrollmentWaitPresence(ctx, t, cfg.DatabasePath, after.ManagedNode.NodeID)
	nodeServe.stop(t)
	nodeServe = enrollmentStartProcess(ctx, t, binary, enrollmentEnv(nodeDir, nodePort), "serve")
	if got := enrollmentWaitPresenceAfter(ctx, t, cfg.DatabasePath, after.ManagedNode.NodeID, firstReboundSequence); got != firstReboundSequence+1 {
		t.Fatalf("rebound node restart sequence = %d, want %d", got, firstReboundSequence+1)
	}
	if firstSequence == 0 { // Keep the initial live-presence assertion explicit.
		t.Fatal("initial node did not report presence")
	}
	nodeServe.stop(t)

	// Detach is also an offline root operation and retains every part of the
	// local configuration except the managed-node authority it removes.
	detach := enrollmentStartProcess(ctx, t, binary, enrollmentEnv(nodeDir, enrollmentFreePort(t)), "node", "detach", "--confirm-node-id", after.ManagedNode.NodeID, "--confirm-controller-id", after.ManagedNode.ControllerID)
	if err := detach.wait(20 * time.Second); err != nil {
		t.Fatal("offline detach failed")
	}
	detached, err := storage.New(nodeDir).Load()
	if err != nil || detached.EffectiveMode() != config.ModeStandalone || detached.ManagedNode != nil || detached.NodeConnection != nil {
		t.Fatalf("detach state unavailable or retained authority: %v", err)
	}
	nodeRecoveryAssertDetachedConfiguration(t, after, detached)
}

func nodeRecoveryActivateController(ctx context.Context, t *testing.T, cfg config.Config) app.ControllerActivationResult {
	t.Helper()
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("node-recovery-process-totp"))
	now := time.Now().UTC()
	code, err := totp.GenerateCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	activation, err := app.New(cfg).ActivateController(ctx, app.ControllerActivationRequest{Username: "admin", Password: "correct horse battery staple", TOTPSecret: secret, TOTPConfirmation: code, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return activation
}

func nodeRecoveryInvitation(ctx context.Context, t *testing.T, client *http.Client, baseURL string) controlapi.Invitation {
	t.Helper()
	var invitation controlapi.Invitation
	enrollmentJSON(ctx, t, client, http.MethodPost, baseURL+"/api/controller/enrollments/invite", map[string]any{}, &invitation)
	return invitation
}

func nodeRecoveryWriteInvitation(t *testing.T, root, name string, invitation controlapi.Invitation) string {
	t.Helper()
	path := filepath.Join(root, name)
	body, err := json.Marshal(invitation)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func nodeRecoveryWriteRootInput(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "controller-recovery.json")
	body, err := json.Marshal(map[string]string{"username": "restored-admin", "password": "new correct horse battery staple"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func nodeRecoveryRecoverySecret(t *testing.T, process *enrollmentProcess) string {
	t.Helper()
	const prefix = "TOTP secret: "
	for _, line := range strings.Split(process.output.String(), "\n") {
		if secret, found := strings.CutPrefix(line, prefix); found && secret != "" {
			return secret
		}
	}
	t.Fatal("controller recovery did not provide one-time administrator credentials")
	return ""
}

func nodeRecoveryRecoveredClient(ctx context.Context, t *testing.T, baseURL, secret string) *http.Client {
	t.Helper()
	code, err := totp.GenerateCode(secret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"username": "restored-admin", "password": "new correct horse battery staple", "code": code})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/controller/login", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", baseURL)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("recovered controller login status = %d", response.StatusCode)
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == "awg_forge_session" && cookie.Value != "" {
			return enrollmentBrowserClient(cookie.Value, baseURL)
		}
	}
	t.Fatal("recovered controller login did not set a session")
	return nil
}

func nodeRecoveryEnableRestoredControl(ctx context.Context, t *testing.T, client *http.Client, baseURL string) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/controller/control/backup", bytes.NewBufferString(`{"password":"post-restore control backup password"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	receipt := response.Header.Get("X-Control-Enable-Receipt")
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || receipt == "" {
		t.Fatal("post-restore controller control backup was not accepted")
	}
	var ignored struct{}
	enrollmentJSON(ctx, t, client, http.MethodPost, baseURL+"/api/controller/control/enable", map[string]any{"receipt": receipt, "backup_saved": true}, &ignored)
}

func nodeRecoveryApprove(ctx context.Context, t *testing.T, client *http.Client, baseURL string, invitation controlapi.Invitation, process *enrollmentProcess) {
	t.Helper()
	review := nodeRecoveryAwaitReview(ctx, t, client, baseURL, invitation, process)
	var ignored struct{}
	enrollmentJSON(ctx, t, client, http.MethodPost, baseURL+"/api/controller/enrollments/decide", map[string]any{"enrollment_id": review.EnrollmentID, "verification_code": review.VerificationCode, "approve": true}, &ignored)
}

func nodeRecoveryAwaitReview(ctx context.Context, t *testing.T, client *http.Client, baseURL string, invitation controlapi.Invitation, process *enrollmentProcess) app.EnrollmentReview {
	t.Helper()
	var review app.EnrollmentReview
	deadline := time.Now().Add(20 * time.Second)
	for {
		err := enrollmentJSONError(ctx, client, http.MethodPost, baseURL+"/api/controller/enrollments/review", map[string]any{"invitation_id": invitation.InvitationID}, &review)
		if err == nil && review.Status == "pending" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pending enrollment was not observed")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := process.waitFor("Enrollment verification code: "+review.VerificationCode, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	return review
}

func nodeRecoveryAssertPreservedConfiguration(t *testing.T, before, after config.State) {
	t.Helper()
	after.Mode = before.Mode
	after.ManagedNode = before.ManagedNode
	after.NodeConnection = before.NodeConnection
	after.UpdatedAt = before.UpdatedAt
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rebind changed local configuration outside node authority")
	}
}

func nodeRecoveryAssertDetachedConfiguration(t *testing.T, before, after config.State) {
	t.Helper()
	after.Mode = before.Mode
	after.ManagedNode = before.ManagedNode
	after.NodeConnection = before.NodeConnection
	after.UpdatedAt = before.UpdatedAt
	if !reflect.DeepEqual(before, after) {
		t.Fatal("detach changed local configuration outside node authority")
	}
}

func nodeRecoveryAssertRevokedBinding(ctx context.Context, t *testing.T, path string, identity sqldb.NodeIdentity) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var revoked sql.NullInt64
	err = db.QueryRowContext(ctx, `SELECT revoked_at_unix_ms FROM control_node_bindings WHERE node_id = ? AND controller_id = ? AND binding_epoch = ?`, identity.NodeID, identity.ControllerID, identity.BindingEpoch).Scan(&revoked)
	if err != nil || !revoked.Valid {
		t.Fatalf("old registry revocation was not retained: %v", err)
	}
}
