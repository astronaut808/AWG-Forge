package sqldb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
)

func TestEnrollmentClaimApprovalAndStatus(t *testing.T) {
	ctx := context.Background()
	db := openControllerAuthTestDB(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := db.CreateControllerUser(ctx, controllerAuthTestUser(now), nil); err != nil {
		t.Fatal(err)
	}
	invitation := EnrollmentInvitation{
		ID: "10000000-0000-4000-8000-000000000001", ControllerID: controllerAuthTestUser(now).ID,
		NodeID: "10000000-0000-4000-8000-000000000002", SecretDigest: sha256.Sum256([]byte("invite")),
		CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	}
	if err := db.CreateEnrollmentInvitation(ctx, invitation); err != nil {
		t.Fatal(err)
	}
	csr := testNodeCSR(t)
	claim := Enrollment{ID: "10000000-0000-4000-8000-000000000003", ControllerID: invitation.ControllerID, RequestedName: "node", CSRDER: csr, CSRHash: sha256.Sum256(csr), ClaimDigest: sha256.Sum256([]byte("claim"))}
	pending, err := db.ClaimEnrollment(ctx, invitation.ID, invitation.SecretDigest, claim, now)
	if err != nil || pending.Status != "pending" || pending.NodeID != invitation.NodeID {
		t.Fatalf("claim = %#v, %v", pending, err)
	}
	retry, err := db.ClaimEnrollment(ctx, invitation.ID, invitation.SecretDigest, claim, now)
	if err != nil || retry.ID != pending.ID {
		t.Fatalf("retry = %#v, %v", retry, err)
	}
	competing := claim
	competing.ID = "10000000-0000-4000-8000-000000000004"
	competing.ClaimDigest = sha256.Sum256([]byte("other"))
	if _, err := db.ClaimEnrollment(ctx, invitation.ID, invitation.SecretDigest, competing, now); !errors.Is(err, ErrEnrollmentConflict) {
		t.Fatalf("competing claim = %v", err)
	}
	material, _, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 8443}, now)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := controlpki.IssueNodeCertificate(material, csr, now)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := db.ApproveEnrollment(ctx, pending.ID, pending.CSRHash, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", certificate, now)
	if err != nil || approved.Status != "approved" || !bytes.Equal(approved.CertificateDER, certificate.DER) {
		t.Fatalf("approve = %#v, %v", approved, err)
	}
	fresh, err := controlpki.IssueNodeCertificate(material, csr, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	retryApproved, err := db.ApproveEnrollment(ctx, pending.ID, pending.CSRHash, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", fresh, now.Add(time.Second))
	if err != nil || !bytes.Equal(retryApproved.CertificateDER, certificate.DER) {
		t.Fatalf("approved retry = %#v, %v", retryApproved, err)
	}
	status, err := db.EnrollmentStatus(ctx, pending.ID, pending.ClaimDigest, now.Add(time.Second))
	if err != nil || !bytes.Equal(status.CertificateDER, certificate.DER) {
		t.Fatalf("status = %#v, %v", status, err)
	}
	if _, err := db.EnrollmentStatus(ctx, pending.ID, sha256.Sum256([]byte("wrong")), now); !errors.Is(err, ErrEnrollmentDenied) {
		t.Fatalf("wrong proof = %v", err)
	}
	if _, err := db.ApproveEnrollment(ctx, pending.ID, pending.CSRHash, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", fresh, invitation.ExpiresAt); !errors.Is(err, ErrEnrollmentDenied) {
		t.Fatalf("expired approved retry = %v", err)
	}
}

func TestEnrollmentExpiresAndRestoreDeletesCredentials(t *testing.T) {
	ctx := context.Background()
	db := openControllerAuthTestDB(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := db.CreateControllerUser(ctx, controllerAuthTestUser(now), nil); err != nil {
		t.Fatal(err)
	}
	invitation := EnrollmentInvitation{ID: "20000000-0000-4000-8000-000000000001", ControllerID: controllerAuthTestUser(now).ID, NodeID: "20000000-0000-4000-8000-000000000002", SecretDigest: sha256.Sum256([]byte("invite")), CreatedAt: now.Add(-10 * time.Minute), ExpiresAt: now}
	if err := db.CreateEnrollmentInvitation(ctx, invitation); err != nil {
		t.Fatal(err)
	}
	csr := testNodeCSR(t)
	claim := Enrollment{ID: "20000000-0000-4000-8000-000000000003", ControllerID: invitation.ControllerID, NodeID: invitation.NodeID, RequestedName: "node", CSRDER: csr, CSRHash: sha256.Sum256(csr), ClaimDigest: sha256.Sum256([]byte("claim"))}
	if _, err := db.ClaimEnrollment(ctx, invitation.ID, invitation.SecretDigest, claim, now); !errors.Is(err, ErrEnrollmentExpired) {
		t.Fatalf("expired claim = %v", err)
	}
	if err := db.DisableControllerAuthAfterRestore(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, db, "SELECT count(*) FROM control_enrollment_invitations"); got != 0 {
		t.Fatalf("invitations after restore = %d", got)
	}
}
