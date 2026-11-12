package sqldb

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
)

func TestEnrollmentOnboardingStatusRequiresCurrentAuthenticatedPresence(t *testing.T) {
	ctx := context.Background()
	db := openControllerAuthTestDB(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	user := controllerAuthTestUser(now)
	if err := db.CreateControllerUser(ctx, user, nil); err != nil {
		t.Fatal(err)
	}
	const generation = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	invitation := EnrollmentInvitation{
		ID: "30000000-0000-4000-8000-000000000001", ControllerID: user.ID,
		NodeID: "30000000-0000-4000-8000-000000000002", SecretDigest: sha256.Sum256([]byte("invite")),
		CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	}
	if err := db.CreateEnrollmentInvitation(ctx, invitation); err != nil {
		t.Fatal(err)
	}
	waiting, err := db.EnrollmentOnboardingStatus(ctx, invitation.ID, user.ID, generation, now)
	if err != nil || waiting.Status != "waiting" || waiting.Connected || waiting.EnrollmentID != "" {
		t.Fatalf("waiting status = %#v, %v", waiting, err)
	}
	if _, err := db.EnrollmentOnboardingStatus(ctx, invitation.ID, "30000000-0000-4000-8000-000000000099", generation, now); err == nil {
		t.Fatal("controller mismatch accepted")
	}
	csr := testNodeCSR(t)
	claim := Enrollment{ID: "30000000-0000-4000-8000-000000000003", ControllerID: user.ID, RequestedName: "node", CSRDER: csr, CSRHash: sha256.Sum256(csr), ClaimDigest: sha256.Sum256([]byte("claim"))}
	pending, err := db.ClaimEnrollment(ctx, invitation.ID, invitation.SecretDigest, claim, now)
	if err != nil {
		t.Fatal(err)
	}
	material, _, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 8443}, now)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := controlpki.IssueNodeCertificate(material, csr, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ApproveEnrollment(ctx, pending.ID, pending.CSRHash, generation, issued, now); err != nil {
		t.Fatal(err)
	}
	before, err := db.EnrollmentOnboardingStatus(ctx, invitation.ID, user.ID, generation, now.Add(time.Second))
	if err != nil || before.Status != "approved" || before.Connected {
		t.Fatalf("status before presence = %#v, %v", before, err)
	}
	identity := NodeIdentity{ControllerID: user.ID, NodeID: invitation.NodeID, BindingEpoch: 1}
	if _, err := db.AcceptAuthenticatedNodePresence(ctx, identity, "30000000-0000-4000-8000-000000000004", "30000000-0000-4000-8000-000000000005", 1, generation, issued.Serial, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	connected, err := db.EnrollmentOnboardingStatus(ctx, invitation.ID, user.ID, generation, now.Add(2*time.Second))
	if err != nil || !connected.Connected || connected.BindingEpoch != 1 {
		t.Fatalf("connected status = %#v, %v", connected, err)
	}
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_enrollments SET controller_id = ? WHERE id = ?", "30000000-0000-4000-8000-000000000099", pending.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnrollmentOnboardingStatus(ctx, invitation.ID, user.ID, generation, now.Add(2*time.Second)); err == nil {
		t.Fatal("mismatched enrollment controller accepted")
	}
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_enrollments SET controller_id = ? WHERE id = ?", user.ID, pending.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_node_bindings SET binding_epoch = 2 WHERE node_id = ?", invitation.NodeID); err != nil {
		t.Fatal(err)
	}
	wrongEpoch, err := db.EnrollmentOnboardingStatus(ctx, invitation.ID, user.ID, generation, now.Add(2*time.Second))
	if err != nil || wrongEpoch.Connected {
		t.Fatalf("new binding appeared connected = %#v, %v", wrongEpoch, err)
	}
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_node_bindings SET binding_epoch = 1 WHERE node_id = ?", invitation.NodeID); err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeNodeCertificate(ctx, generation, issued.Serial, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	revoked, err := db.EnrollmentOnboardingStatus(ctx, invitation.ID, user.ID, generation, now.Add(4*time.Second))
	if err != nil || revoked.Connected {
		t.Fatalf("revoked status = %#v, %v", revoked, err)
	}
	expired, err := db.EnrollmentOnboardingStatus(ctx, invitation.ID, user.ID, generation, invitation.ExpiresAt)
	if err != nil || expired.Status != "expired" || expired.Connected {
		t.Fatalf("expired status = %#v, %v", expired, err)
	}
}
