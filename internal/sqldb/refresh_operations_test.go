package sqldb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/google/uuid"
)

func refreshFixture(t *testing.T) (*DB, controlpki.Material, string, NodeIdentity, string, controlapi.Snapshot, time.Time) {
	t.Helper()
	db, m, issuer, id, serial, s, now := projectionFixture(t)
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := controlapi.Presence{BootID: s.BootID, BootSequence: s.BootSequence, StateEpoch: s.StateEpoch, BindingEpoch: s.BindingEpoch, DesiredGeneration: s.DesiredGeneration, ApplicationVersion: "test", ContractVersion: 1, Capabilities: []string{"presence", "snapshot.v1", controlapi.RefreshCapability}}
	session, err := db.RecordNodePresence(context.Background(), id, issuer, serial, p, now)
	if err != nil {
		t.Fatal(err)
	}
	s.SessionID = session
	if err := db.AcceptNodeSnapshot(context.Background(), id, issuer, serial, s, now); err != nil {
		t.Fatal(err)
	}
	return db, m, issuer, id, serial, s, now
}
func refreshIntent(s controlapi.Snapshot) controlapi.RefreshRequest {
	return controlapi.RefreshRequest{OperationID: uuid.NewString(), IdempotencyKey: uuid.NewString(), StateEpoch: s.StateEpoch, BindingEpoch: s.BindingEpoch, Generation: s.DesiredGeneration}
}
func refreshPoll(s controlapi.Snapshot) controlapi.OperationPoll {
	return controlapi.OperationPoll{SessionID: s.SessionID, BootID: s.BootID, BootSequence: s.BootSequence, StateEpoch: s.StateEpoch, BindingEpoch: s.BindingEpoch, Generation: s.DesiredGeneration}
}
func TestRefreshDurableCorrelatedResultReplayAndProjectionAdvance(t *testing.T) {
	ctx := context.Background()
	db, material, issuer, id, serial, s, now := refreshFixture(t)
	req := refreshIntent(s)
	created, err := db.CreateRefresh(ctx, id.ControllerID, issuer, id.NodeID, req, now)
	if err != nil || created.Status != "queued" {
		t.Fatalf("create: %+v %v", created, err)
	}
	same, err := db.CreateRefresh(ctx, id.ControllerID, issuer, id.NodeID, req, now)
	if err != nil || same.OperationID != created.OperationID {
		t.Fatal("exact replay lost intent", err)
	}
	for _, changed := range []controlapi.RefreshRequest{{OperationID: req.OperationID, IdempotencyKey: uuid.NewString(), StateEpoch: req.StateEpoch, BindingEpoch: 1, Generation: req.Generation}, {OperationID: uuid.NewString(), IdempotencyKey: req.IdempotencyKey, StateEpoch: req.StateEpoch, BindingEpoch: 1, Generation: req.Generation}, {OperationID: req.OperationID, IdempotencyKey: req.IdempotencyKey, StateEpoch: req.StateEpoch, BindingEpoch: 1, Generation: req.Generation + 1}} {
		if _, err := db.CreateRefresh(ctx, id.ControllerID, issuer, id.NodeID, changed, now); !errors.Is(err, ErrOperationFenced) {
			t.Fatal("changed intent accepted", err)
		}
	}
	poll := refreshPoll(s)
	d, err := db.LeaseRefresh(ctx, id, issuer, serial, poll, now)
	if err != nil || d == nil {
		t.Fatal("lease", err)
	}
	again, err := db.LeaseRefresh(ctx, id, issuer, serial, poll, now.Add(time.Second))
	if err != nil || again.LeaseToken != d.LeaseToken {
		t.Fatal("duplicate lease changed", err)
	}
	receipt := controlapi.OperationReceipt{OperationPoll: poll, LeaseToken: d.LeaseToken, Status: "succeeded", Code: "snapshot_accepted", Result: &controlapi.RefreshResult{SnapshotAccepted: true, Sequence: s.Sequence + 1}}
	if err := db.CompleteRefresh(ctx, id, issuer, serial, req.OperationID, receipt, now.Add(time.Second)); !errors.Is(err, ErrOperationFenced) {
		t.Fatal("unproved success", err)
	}
	s.Sequence++
	s.ObservedAt = now.Add(time.Second)
	if err := db.AcceptNodeSnapshot(ctx, id, issuer, serial, s, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteRefresh(ctx, id, issuer, serial, req.OperationID, receipt, now.Add(time.Second)); !errors.Is(err, ErrOperationFenced) {
		t.Fatal("periodic snapshot proved result", err)
	}
	s.Sequence++
	receipt.Result.Sequence = s.Sequence
	s.Refresh = &controlapi.RefreshCorrelation{OperationID: req.OperationID, LeaseToken: d.LeaseToken}
	if err := db.AcceptNodeSnapshot(ctx, id, issuer, serial, s, now.Add(time.Second)); err != nil {
		t.Fatal("correlated upload", err)
	}
	// Later observations must not erase durable operation evidence.
	s.Refresh = nil
	s.Sequence++
	if err := db.AcceptNodeSnapshot(ctx, id, issuer, serial, s, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteRefresh(ctx, id, issuer, serial, req.OperationID, receipt, now.Add(2*time.Second)); err != nil {
		t.Fatal("complete", err)
	}
	if err := db.CompleteRefresh(ctx, id, issuer, serial, req.OperationID, receipt, now.Add(45*time.Second)); err != nil {
		t.Fatal("lost acknowledgement replay", err)
	}
	receipt.Result.Sequence++
	if err := db.CompleteRefresh(ctx, id, issuer, serial, req.OperationID, receipt, now.Add(3*time.Second)); !errors.Is(err, ErrOperationFenced) {
		t.Fatal("conflicting terminal replay", err)
	}
	if err := VerifyControllerRegistrySnapshot(ctx, snapshotPath(t, db), snapshotControllerID, issuer, snapshotCA(t, material)); err != nil {
		t.Fatal("valid schema12 archive", err)
	}
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_refresh_operations SET receipt_json=?", []byte(`{"raw_config":"canary"}`)); err != nil {
		t.Fatal(err)
	}
	if err := VerifyControllerRegistrySnapshot(ctx, snapshotPath(t, db), snapshotControllerID, issuer, snapshotCA(t, material)); err == nil {
		t.Fatal("invalid archive accepted")
	}
}
func TestRefreshLeaseReclaimGenerationAndSessionFences(t *testing.T) {
	ctx := context.Background()
	db, _, issuer, id, serial, s, now := refreshFixture(t)
	req := refreshIntent(s)
	if _, err := db.CreateRefresh(ctx, id.ControllerID, issuer, id.NodeID, req, now); err != nil {
		t.Fatal(err)
	}
	p := refreshPoll(s)
	d, err := db.LeaseRefresh(ctx, id, issuer, serial, p, now)
	if err != nil {
		t.Fatal(err)
	}
	next, err := db.LeaseRefresh(ctx, id, issuer, serial, p, now.Add(41*time.Second))
	if err != nil || d.LeaseToken == next.LeaseToken {
		t.Fatal("expired lease not reclaimed", err)
	}
	s.Refresh = &controlapi.RefreshCorrelation{OperationID: req.OperationID, LeaseToken: d.LeaseToken}
	s.Sequence++
	if err := db.AcceptNodeSnapshot(ctx, id, issuer, serial, s, now.Add(41*time.Second)); !errors.Is(err, ErrOperationFenced) {
		t.Fatal("old lease snapshot accepted", err)
	}
	// Local mutation while a lease exists must conflict, without needing SQLite
	// on the node or mutating controller desired projections from a receipt.
	conflict := controlapi.OperationReceipt{OperationPoll: p, LeaseToken: next.LeaseToken, Status: "conflict", Code: "generation_conflict"}
	conflict.Generation++
	if err := db.CompleteRefresh(ctx, id, issuer, serial, req.OperationID, conflict, now.Add(42*time.Second)); err != nil {
		t.Fatal("generation conflict", err)
	}
	// Boot supersession fences terminal replay as well as fresh dequeue.
	presence := controlapi.Presence{BootID: uuid.NewString(), BootSequence: 2, StateEpoch: s.StateEpoch, BindingEpoch: 1, ApplicationVersion: "test", ContractVersion: 1, Capabilities: []string{"snapshot.v1", controlapi.RefreshCapability}}
	if _, err := db.RecordNodePresence(ctx, id, issuer, serial, presence, now.Add(43*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteRefresh(ctx, id, issuer, serial, req.OperationID, conflict, now.Add(44*time.Second)); !errors.Is(err, ErrOperationFenced) {
		t.Fatal("old boot replay accepted", err)
	}
	if _, err := db.LeaseRefresh(ctx, id, issuer, serial, p, now.Add(44*time.Second)); !errors.Is(err, ErrOperationFenced) {
		t.Fatal("old boot poll accepted", err)
	}
}
func TestRefreshConcurrentAdmissionBoundsExpiryRestoreAndDBFailure(t *testing.T) {
	ctx := context.Background()
	db, _, issuer, id, serial, s, now := refreshFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for range 12 {
		wg.Go(func() {
			_, err := db.CreateRefresh(ctx, id.ControllerID, issuer, id.NodeID, refreshIntent(s), now)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrOperationLimit) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("admitted %d operations", success)
	}
	d, err := db.LeaseRefresh(ctx, id, issuer, serial, refreshPoll(s), now)
	if err != nil || d == nil {
		t.Fatal(err)
	}
	if _, err := db.LeaseRefresh(ctx, id, issuer, serial, refreshPoll(s), now.Add(controlapi.OperationTTL)); !errors.Is(err, ErrOperationFenced) {
		t.Fatal("expired session poll", err)
	}
	v, err := db.RefreshStatus(ctx, id.ControllerID, id.NodeID, d.Operation.OperationID, now.Add(controlapi.OperationTTL))
	if err != nil || v.Status != "expired" {
		t.Fatal("expiry", v, err)
	}
	if _, err := db.RefreshStatus(ctx, id.ControllerID, uuid.NewString(), d.Operation.OperationID, now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign node result", err)
	}
	if err := db.DisableControllerAuthAfterRestore(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.VerifyControllerRestoreReset(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RefreshStatus(ctx, id.ControllerID, id.NodeID, d.Operation.OperationID, now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("restore revived operation", err)
	}
	if _, err := db.sql.ExecContext(ctx, "ALTER TABLE control_refresh_operations RENAME TO unavailable_operations"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateRefresh(ctx, id.ControllerID, issuer, id.NodeID, refreshIntent(s), now); err == nil {
		t.Fatal("DB failure accepted work")
	}
}
func TestRefreshInvalidInputUnsupportedAndCommitFailureLeaveNoRows(t *testing.T) {
	ctx := context.Background()
	db, _, issuer, id, _, s, now := refreshFixture(t)
	invalid := refreshIntent(s)
	invalid.OperationID = "../foreign"
	if _, err := db.CreateRefresh(ctx, id.ControllerID, issuer, id.NodeID, invalid, now); err == nil {
		t.Fatal("invalid request accepted")
	}
	if _, err := db.sql.ExecContext(ctx, `UPDATE control_node_presence SET capabilities_json='["snapshot.v1"]'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateRefresh(ctx, id.ControllerID, issuer, id.NodeID, refreshIntent(s), now); !errors.Is(err, ErrOperationUnsupported) {
		t.Fatal("legacy node queued", err)
	}
	caps, _ := json.Marshal([]string{"snapshot.v1", controlapi.RefreshCapability})
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_node_presence SET capabilities_json=?", string(caps)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `CREATE TABLE refresh_commit_failure (node_id TEXT REFERENCES control_node_bindings(node_id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail_refresh AFTER INSERT ON control_refresh_operations BEGIN INSERT INTO refresh_commit_failure VALUES ('missing'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateRefresh(ctx, id.ControllerID, issuer, id.NodeID, refreshIntent(s), now); err == nil {
		t.Fatal("commit failure acknowledged")
	}
	var count int
	if err := db.sql.QueryRowContext(ctx, "SELECT count(*) FROM control_refresh_operations").Scan(&count); err != nil || count != 0 {
		t.Fatal("phantom work", count, err)
	}
}

func TestRefreshTerminalReplayCannotExtendRetentionWithLiveAuthority(t *testing.T) {
	ctx := context.Background()
	db, _, issuer, id, serial, s, now := refreshFixture(t)
	req := refreshIntent(s)
	if _, err := db.CreateRefresh(ctx, id.ControllerID, issuer, id.NodeID, req, now); err != nil {
		t.Fatal(err)
	}
	p := refreshPoll(s)
	d, err := db.LeaseRefresh(ctx, id, issuer, serial, p, now)
	if err != nil {
		t.Fatal(err)
	}
	s.Sequence++
	s.Refresh = &controlapi.RefreshCorrelation{OperationID: req.OperationID, LeaseToken: d.LeaseToken}
	if err := db.AcceptNodeSnapshot(ctx, id, issuer, serial, s, now); err != nil {
		t.Fatal(err)
	}
	receipt := controlapi.OperationReceipt{OperationPoll: p, LeaseToken: d.LeaseToken, Status: "succeeded", Code: "snapshot_accepted", Result: &controlapi.RefreshResult{SnapshotAccepted: true, Sequence: s.Sequence}}
	if err := db.CompleteRefresh(ctx, id, issuer, serial, req.OperationID, receipt, now); err != nil {
		t.Fatal(err)
	}
	late := now.Add(controlapi.OperationTTL + controlapi.OperationRetention + time.Second)
	// Simulate continually renewed same session without changing receipt authority.
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_node_presence SET expires_at_unix_ms=? WHERE node_id=?", late.Add(time.Minute).UnixMilli(), id.NodeID); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteRefresh(ctx, id, issuer, serial, req.OperationID, receipt, late); !errors.Is(err, ErrOperationFenced) {
		t.Fatal("terminal replay extended beyond retention", err)
	}
}

func TestRefreshArchiveRejectsQueuedLeaseEvidence(t *testing.T) {
	ctx := context.Background()
	db, material, issuer, id, serial, s, now := refreshFixture(t)
	req := refreshIntent(s)
	if _, err := db.CreateRefresh(ctx, id.ControllerID, issuer, id.NodeID, req, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LeaseRefresh(ctx, id, issuer, serial, refreshPoll(s), now); err != nil {
		t.Fatal(err)
	}
	if err := VerifyControllerRegistrySnapshot(ctx, snapshotPath(t, db), snapshotControllerID, issuer, snapshotCA(t, material)); err != nil {
		t.Fatal("valid leased archive", err)
	}
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_refresh_operations SET status='queued'"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyControllerRegistrySnapshot(ctx, snapshotPath(t, db), snapshotControllerID, issuer, snapshotCA(t, material)); err == nil {
		t.Fatal("impossible queued lease accepted")
	}
}

func TestRefreshRetainedNodeQuotaRejectsThenPrunesWithoutEvictingLiveReplay(t *testing.T) {
	ctx := context.Background()
	db, _, issuer, id, _, s, now := refreshFixture(t)
	issued := now.Add(-5 * time.Minute)
	for range controlapi.MaxNodeOperations {
		request := refreshIntent(s)
		envelope := controlapi.OperationEnvelope{RefreshRequest: request, IssuedAt: issued, ExpiresAt: issued.Add(controlapi.OperationTTL), Operation: controlapi.RefreshOperation{Kind: "snapshot.refresh", SchemaVersion: 1}}
		raw, _ := json.Marshal(envelope)
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO control_refresh_operations(operation_id,controller_id,node_id,idempotency_key,envelope_json,status,code,issued_at_unix_ms,expires_at_unix_ms,completed_at_unix_ms) VALUES(?,?,?,?,?,'expired','operation_expired',?,?,?)`, request.OperationID, id.ControllerID, id.NodeID, request.IdempotencyKey, raw, issued.UnixMilli(), envelope.ExpiresAt.UnixMilli(), now.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.CreateRefresh(ctx, id.ControllerID, issuer, id.NodeID, refreshIntent(s), now); !errors.Is(err, ErrOperationLimit) {
		t.Fatal("retained quota silently evicted replay", err)
	}
	var count int
	if err := db.sql.QueryRowContext(ctx, "SELECT count(*) FROM control_refresh_operations").Scan(&count); err != nil || count != controlapi.MaxNodeOperations {
		t.Fatal(count, err)
	}
	later := now.Add(controlapi.OperationRetention)
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_node_presence SET expires_at_unix_ms=?", later.Add(time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateRefresh(ctx, id.ControllerID, issuer, id.NodeID, refreshIntent(s), later); err != nil {
		t.Fatal("retention failed to release quota", err)
	}
	if err := db.sql.QueryRowContext(ctx, "SELECT count(*) FROM control_refresh_operations").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
