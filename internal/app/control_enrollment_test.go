package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/astronaut808/awg-forge/internal/config"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/google/uuid"
)

func TestEnrollmentProtocolApprovalReplayAndRevocation(t *testing.T) {
	f := newControlLifecycleFixture(t)
	ctx := context.Background()
	receipt, err := f.service.PrepareControlEnable(ctx, f.token, func(context.Context, config.State) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.EnableControl(ctx, f.token, receipt, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.service.ShutdownControl() })
	invitation, err := f.service.CreateEnrollmentInvitation(ctx, f.token)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig := f.tls.Clone()
	tlsConfig.Certificates = nil
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	csr, key := renewalTestCSR(t)
	request := controlapi.ClaimRequest{RequestedName: "node", CSRPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr})), BootID: uuid.NewString(), ApplicationVersion: "test", ContractVersions: []int{1}, Capabilities: []string{"presence"}}
	claimPath := "/control/v1/enrollments/" + invitation.InvitationID + "/claim"
	var claim controlapi.ClaimAccepted
	enrollmentHTTP(t, client, invitation.ControllerURL, http.MethodPost, claimPath, "", request, 401, nil)
	enrollmentHTTP(t, client, invitation.ControllerURL, http.MethodPost, claimPath, invitation.Secret, request, 202, &claim)
	var retry controlapi.ClaimAccepted
	enrollmentHTTP(t, client, invitation.ControllerURL, http.MethodPost, claimPath, invitation.Secret, request, 202, &retry)
	if claim != retry {
		t.Fatal("exact CSR retry changed enrollment capability")
	}
	secondCSR, _ := renewalTestCSR(t)
	competing := request
	competing.CSRPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: secondCSR}))
	enrollmentHTTP(t, client, invitation.ControllerURL, http.MethodPost, claimPath, invitation.Secret, competing, 409, nil)
	if err := f.service.DecideEnrollment(ctx, f.token, claim.EnrollmentID, "WRNG-CODE", true); !errors.Is(err, sqldb.ErrEnrollmentDenied) {
		t.Fatal("approval accepted mismatched comparison code")
	}
	review, err := f.service.ReviewEnrollment(ctx, f.token, invitation.InvitationID)
	if err != nil || review.VerificationCode != claim.VerificationCode || review.Status != "pending" {
		t.Fatal("review did not bind the claimed CSR")
	}
	if err := f.service.DecideEnrollment(ctx, f.token, claim.EnrollmentID, claim.VerificationCode, true); err != nil {
		t.Fatal(err)
	}
	var approved controlapi.EnrollmentStatus
	statusPath := "/control/v1/enrollments/" + claim.EnrollmentID
	enrollmentHTTP(t, client, invitation.ControllerURL, http.MethodGet, statusPath, invitation.Secret, nil, 401, nil)
	enrollmentHTTP(t, client, invitation.ControllerURL, http.MethodGet, statusPath, claim.ClaimToken, nil, 200, &approved)
	if approved.Status != "approved" || approved.Certificate == nil || approved.ContractVersion != 1 {
		t.Fatal("approved status incomplete")
	}
	certificate := renewalTestCertificate(t, []byte(approved.Certificate.CertificatePEM))
	nodeTLS := tlsConfig.Clone()
	nodeTLS.Certificates = []tls.Certificate{{Certificate: [][]byte{certificate.Raw}, PrivateKey: key}}
	nodeTransport := &http.Transport{TLSClientConfig: nodeTLS}
	nodeClient := &http.Client{Transport: nodeTransport, Timeout: 5 * time.Second}
	defer nodeClient.CloseIdleConnections()
	presence := controlapi.Presence{BootID: uuid.NewString(), BootSequence: 1, ApplicationVersion: "test", ContractVersion: 1, StateEpoch: uuid.NewString(), BindingEpoch: 1, Capabilities: []string{"presence"}, ObservedAt: time.Now().UTC()}
	enrollmentHTTP(t, client, invitation.ControllerURL, http.MethodPut, "/control/v1/node/presence", claim.ClaimToken, presence, 403, nil)
	var accepted controlapi.PresenceAccepted
	enrollmentHTTP(t, nodeClient, invitation.ControllerURL, http.MethodPut, "/control/v1/node/presence", "", presence, 200, &accepted)
	if accepted.ControllerID != approved.ControllerID || accepted.SessionID == "" {
		t.Fatal("presence identity missing")
	}
	replacement := presence
	replacement.BootID = uuid.NewString()
	enrollmentHTTP(t, nodeClient, invitation.ControllerURL, http.MethodPut, "/control/v1/node/presence", "", replacement, 409, nil)
	db, err := sqldb.Open(ctx, f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.RevokeNodeBinding(ctx, sqldb.NodeIdentity{ControllerID: approved.ControllerID, NodeID: approved.NodeID, BindingEpoch: 1}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Reuse the same transport/keep-alive connection after revocation.
	enrollmentHTTP(t, nodeClient, invitation.ControllerURL, http.MethodPut, "/control/v1/node/presence", "", presence, 403, nil)
	enrollmentHTTP(t, client, invitation.ControllerURL, http.MethodGet, statusPath, claim.ClaimToken, nil, 401, nil)
	for _, path := range []string{claimPath + "/", strings.Replace(claimPath, "/claim", "%2fclaim", 1), "/control/v1/enrollments/not-a-uuid/claim", "/control/v1/node/presence?token=x"} {
		enrollmentHTTP(t, client, invitation.ControllerURL, http.MethodPost, path, invitation.Secret, request, 404, nil)
	}
	// No plaintext bootstrap secret or derived claim token is persisted.
	raw, err := os.ReadFile(f.cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(invitation.Secret)) || bytes.Contains(raw, []byte(claim.ClaimToken)) {
		t.Fatal("bootstrap credential persisted in plaintext")
	}
}

func enrollmentHTTP(t *testing.T, client *http.Client, base, method, path, token string, input any, status int, output any) {
	t.Helper()
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequest(method, base+path, body)
	if err != nil {
		t.Fatal("create control request")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("control request failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != status {
		t.Fatalf("control response: got %d, want %d", response.StatusCode, status)
	}
	if output != nil {
		if err := json.NewDecoder(response.Body).Decode(output); err != nil {
			t.Fatal("decode control response")
		}
	}
}

func TestControlRequestLockCancellation(t *testing.T) {
	cfg := controllerTestConfig(t)
	service := New(cfg)
	lock, err := storage.AcquireStateMutationLock(cfg.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := service.lockControlRequest(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock cancellation: %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("lock ignored cancellation")
	}
	if !service.mu.TryLock() {
		t.Fatal("canceled lock retained app mutex")
	}
	service.mu.Unlock()
}
