package sqldb

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/google/uuid"
)

func clientEnabledFixture(t *testing.T) (*DB, string, NodeIdentity, string, controlapi.Snapshot, time.Time, controlapi.ClientEnabledRequest) {
	t.Helper()
	db, _, issuer, id, serial, snapshot, now := refreshFixture(t)
	snapshot.Desired.Tunnels = []controlapi.SnapshotTunnel{{ID: "0123456789abcdef", Name: "node.example", Interface: "awg0.1", Profile: "awg_2_0", Enabled: true, ListenPort: 51820, Clients: []controlapi.SnapshotClient{{ID: "fedcba9876543210", Name: "phone", Enabled: true, Address: "10.20.0.2"}}}}
	snapshot.Observations.Tunnels = []controlapi.TunnelObservation{{ID: "0123456789abcdef", Clients: []controlapi.ClientObservation{{ID: "fedcba9876543210"}}}}
	if _, err := db.sql.ExecContext(context.Background(), "UPDATE control_node_projections SET desired_json=? WHERE node_id=?", mustJSON(t, snapshot.Desired), id.NodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(context.Background(), `UPDATE control_node_presence SET capabilities_json='["snapshot.v1","snapshot.refresh.v1","client.enabled.set.v1"]'`); err != nil {
		t.Fatal(err)
	}
	enabled := false
	req := controlapi.ClientEnabledRequest{RefreshRequest: refreshIntent(snapshot), ClientEnabledPayload: controlapi.ClientEnabledPayload{TunnelID: "0123456789abcdef", ClientID: "fedcba9876543210", Enabled: &enabled}}
	return db, issuer, id, serial, snapshot, now, req
}

func TestClientEnabledCurrentAuthorityCompletesExpiredLeaseAndRequiresAckForRetention(t *testing.T) {
	ctx := context.Background()
	db, issuer, id, serial, s, now, req := clientEnabledFixture(t)
	created, err := db.CreateClientEnabled(ctx, id.ControllerID, issuer, id.NodeID, req, now)
	if err != nil || created.Kind != "client.enabled.set" {
		t.Fatalf("create = %#v, %v", created, err)
	}
	poll := refreshPoll(s)
	delivery, err := db.LeaseRefresh(ctx, id, issuer, serial, poll, now)
	if err != nil || delivery == nil {
		t.Fatalf("lease = %#v, %v", delivery, err)
	}
	// A later boot/session may return the stable outcome after the transport
	// lease and original deadline, carrying its current local generation.
	late := now.Add(controlapi.OperationTTL + time.Second)
	presence := controlapi.Presence{BootID: uuid.NewString(), BootSequence: 2, StateEpoch: s.StateEpoch, BindingEpoch: 1, DesiredGeneration: req.Generation + 1, ApplicationVersion: "test", ContractVersion: 1, Capabilities: []string{"presence", "snapshot.v1", controlapi.RefreshCapability, controlapi.ClientEnabledCapability}}
	session, err := db.RecordNodePresence(ctx, id, issuer, serial, presence, late)
	if err != nil {
		t.Fatal(err)
	}
	poll.SessionID, poll.BootID, poll.BootSequence, poll.Generation = session, presence.BootID, presence.BootSequence, presence.DesiredGeneration
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_node_presence SET expires_at_unix_ms=?", late.Add(controlapi.OperationRetention+time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	result := controlapi.OperationReceipt{OperationPoll: poll, LeaseToken: delivery.LeaseToken, Status: "succeeded", Code: "client_enabled_committed", IntentHash: delivery.Operation.IntentHash(), ClientResult: &controlapi.ClientEnabledResult{Generation: req.Generation + 1, TunnelID: "0123456789abcdef", ClientID: "fedcba9876543210", Enabled: false}}
	if err := db.CompleteRefresh(ctx, id, issuer, serial, req.OperationID, result, late); err != nil {
		t.Fatalf("late durable result = %v", err)
	}
	status, err := db.RefreshStatus(ctx, id.ControllerID, id.NodeID, req.OperationID, late)
	if err != nil || status.Status != "succeeded" || status.ClientResult == nil {
		t.Fatalf("status = %#v, %v", status, err)
	}
	// Terminal client results remain retained until an authenticated node ACK.
	maintain := func(at time.Time) {
		tx, err := db.sql.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = maintainRefresh(ctx, tx, at); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	retainedAt := late.Add(controlapi.OperationRetention + time.Second)
	maintain(retainedAt)
	var count int
	if err := db.sql.QueryRowContext(ctx, "SELECT count(*) FROM control_refresh_operations WHERE operation_id=?", req.OperationID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("unacknowledged result pruned: count=%d err=%v", count, err)
	}
	ack := controlapi.ClientResultAck{OperationPoll: poll, IntentHash: delivery.Operation.IntentHash()}
	if err := db.AcknowledgeClientResult(ctx, id, issuer, serial, req.OperationID, ack, late); err != nil {
		t.Fatalf("ack = %v", err)
	}
	maintain(retainedAt)
	if err := db.sql.QueryRowContext(ctx, "SELECT count(*) FROM control_refresh_operations WHERE operation_id=?", req.OperationID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("acknowledged result retained: count=%d err=%v", count, err)
	}
}

func TestClientEnabledRejectsChangedPayloadAndExposesReconciliation(t *testing.T) {
	ctx := context.Background()
	db, issuer, id, serial, s, now, req := clientEnabledFixture(t)
	if _, err := db.CreateClientEnabled(ctx, id.ControllerID, issuer, id.NodeID, req, now); err != nil {
		t.Fatal(err)
	}
	changed := req
	enabled := true
	changed.Enabled = &enabled
	if _, err := db.CreateClientEnabled(ctx, id.ControllerID, issuer, id.NodeID, changed, now); !errors.Is(err, ErrOperationFenced) {
		t.Fatalf("changed payload accepted: %v", err)
	}
	poll := refreshPoll(s)
	if _, err := db.LeaseRefresh(ctx, id, issuer, serial, poll, now); err != nil {
		t.Fatal(err)
	}
	late := now.Add(controlapi.OperationTTL + time.Second)
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_node_presence SET expires_at_unix_ms=?", late.Add(time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	delivery, err := db.LeaseRefresh(ctx, id, issuer, serial, poll, late)
	if err != nil || delivery == nil || !delivery.ReconciliationOnly || delivery.Validate() != nil {
		t.Fatalf("expired client delivery = %#v, %v", delivery, err)
	}
}

func TestClientEnabledAckLostResponseAfterPruningStillConfirms(t *testing.T) {
	ctx := context.Background()
	db, issuer, id, serial, s, now, req := clientEnabledFixture(t)
	if _, err := db.CreateClientEnabled(ctx, id.ControllerID, issuer, id.NodeID, req, now); err != nil {
		t.Fatal(err)
	}
	poll := refreshPoll(s)
	d, err := db.LeaseRefresh(ctx, id, issuer, serial, poll, now)
	if err != nil {
		t.Fatal(err)
	}
	poll.Generation = req.Generation + 1
	receipt := controlapi.OperationReceipt{OperationPoll: poll, LeaseToken: d.LeaseToken, Status: "succeeded", Code: "client_enabled_committed", IntentHash: d.Operation.IntentHash(), ClientResult: &controlapi.ClientEnabledResult{Generation: req.Generation + 1, TunnelID: req.TunnelID, ClientID: req.ClientID, Enabled: false}}
	if err := db.CompleteRefresh(ctx, id, issuer, serial, req.OperationID, receipt, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ack := controlapi.ClientResultAck{OperationPoll: poll, IntentHash: d.Operation.IntentHash()}
	bad := ack
	bad.IntentHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := db.AcknowledgeClientResult(ctx, id, issuer, serial, req.OperationID, bad, now.Add(time.Second)); !errors.Is(err, ErrOperationFenced) {
		t.Fatalf("changed retained hash: %v", err)
	}
	if err := db.AcknowledgeClientResult(ctx, id, issuer, serial, req.OperationID, ack, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	late := now.Add(controlapi.OperationRetention + controlapi.OperationTTL + time.Second)
	if _, err := db.RefreshStatus(ctx, id.ControllerID, id.NodeID, req.OperationID, late); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected retained row pruned: %v", err)
	}
	if _, err := db.sql.ExecContext(ctx, "UPDATE control_node_presence SET expires_at_unix_ms=?", late.Add(time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := db.AcknowledgeClientResult(ctx, id, issuer, serial, req.OperationID, ack, late); err != nil {
		t.Fatalf("lost ACK never confirms after retention: %v", err)
	}
	stale := ack
	stale.SessionID = uuid.NewString()
	if err := db.AcknowledgeClientResult(ctx, id, issuer, serial, req.OperationID, stale, late); !errors.Is(err, ErrOperationFenced) {
		t.Fatalf("missing row bypassed authority: %v", err)
	}
}
