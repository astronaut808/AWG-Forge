package sqldb

import (
	"context"
	"errors"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"testing"
	"time"
)

func TestClientCRUDAdmissionDeliveryResultAckAndRestoreValidator(t *testing.T) {
	for _, kind := range []string{"client.create", "client.delete"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			db, issuer, id, serial, s, now, enabled := clientEnabledFixture(t)
			caps := `["snapshot.v1","snapshot.refresh.v1","client.create.v1","client.delete.v1"]`
			if _, err := db.sql.ExecContext(ctx, "UPDATE control_node_presence SET capabilities_json=?", caps); err != nil {
				t.Fatal(err)
			}
			p := controlapi.ClientEnabledPayload{TunnelID: enabled.TunnelID, ClientID: enabled.ClientID}
			if kind == "client.create" {
				p.ClientID = "0123456789abcdee"
				p.Name = "remote phone"
			} else {
				v := true
				p.Confirmed = &v
			}
			request := enabled.RefreshRequest
			create := func(p controlapi.ClientEnabledPayload) (controlapi.OperationStatus, error) {
				return db.CreateClientMutation(ctx, id.ControllerID, issuer, id.NodeID, request, kind, p, now)
			}
			v, err := create(p)
			if err != nil || v.Kind != kind {
				t.Fatalf("admission %v", err)
			}
			replay, err := create(p)
			if err != nil || replay.OperationID != v.OperationID {
				t.Fatal("admission replay failed")
			}
			bad := p
			bad.TunnelID = "foreign"
			if _, err := create(bad); !errors.Is(err, ErrOperationFenced) {
				t.Fatal("changed intent not fenced")
			}
			poll := refreshPoll(s)
			d, err := db.LeaseRefresh(ctx, id, issuer, serial, poll, now)
			if err != nil || d == nil || d.Operation.Operation.Kind != kind {
				t.Fatalf("lease %v", err)
			}
			late := now.Add(controlapi.OperationTTL + time.Second)
			if _, err := db.sql.ExecContext(ctx, "UPDATE control_node_presence SET expires_at_unix_ms=?", late.Add(time.Minute).UnixMilli()); err != nil {
				t.Fatal(err)
			}
			reconcile, err := db.LeaseRefresh(ctx, id, issuer, serial, poll, late)
			if err != nil || reconcile == nil || !reconcile.ReconciliationOnly || reconcile.Validate() != nil {
				t.Fatalf("reconciliation %v", err)
			}
			poll.Generation = request.Generation + 1
			r := controlapi.OperationReceipt{OperationPoll: poll, LeaseToken: d.LeaseToken, Status: "succeeded", Code: controlapi.ClientCommittedCode(kind), IntentHash: d.Operation.IntentHash()}
			if kind == "client.create" {
				r.CreateResult = &controlapi.ClientCreateResult{Generation: poll.Generation, TunnelID: p.TunnelID, ClientID: p.ClientID, Name: p.Name}
			} else {
				r.DeleteResult = &controlapi.ClientDeleteResult{Generation: poll.Generation, TunnelID: p.TunnelID, ClientID: p.ClientID, Deleted: true}
			}
			wrong := r
			wrong.Code = "client_enabled_committed"
			if err := db.CompleteRefresh(ctx, id, issuer, serial, request.OperationID, wrong, late); !errors.Is(err, ErrOperationFenced) {
				t.Fatal("wrong kind accepted")
			}
			if err := db.CompleteRefresh(ctx, id, issuer, serial, request.OperationID, r, late); err != nil {
				t.Fatal(err)
			}
			if err := db.CompleteRefresh(ctx, id, issuer, serial, request.OperationID, r, late); err != nil {
				t.Fatal("terminal replay", err)
			}
			row, err := scanRefresh(db.sql.QueryRowContext(ctx, "SELECT "+refreshColumns+" FROM control_refresh_operations WHERE operation_id=?", request.OperationID))
			if err != nil || validateRefreshRow(row, id.ControllerID) != nil {
				t.Fatal("saved schema13 validator", err)
			}
			if err := db.AcknowledgeClientResult(ctx, id, issuer, serial, request.OperationID, controlapi.ClientResultAck{OperationPoll: poll, IntentHash: d.Operation.IntentHash()}, late); err != nil {
				t.Fatal("ack", err)
			}
			status, err := db.RefreshStatus(ctx, id.ControllerID, id.NodeID, request.OperationID, late)
			if err != nil || status.Status != "succeeded" || (kind == "client.create" && status.CreateResult == nil) || (kind == "client.delete" && status.DeleteResult == nil) {
				t.Fatal("safe view", err)
			}
		})
	}
}
func TestClientCRUDAdmissionMissingCollisionUnsupportedNoPhantom(t *testing.T) {
	ctx := context.Background()
	db, issuer, id, _, _, now, r := clientEnabledFixture(t)
	old := r
	old.TunnelID = "missing"
	if _, err := db.CreateClientEnabled(ctx, id.ControllerID, issuer, id.NodeID, old, now); !errors.Is(err, ErrOperationFenced) {
		t.Fatal("old enabled missing code changed", err)
	}

	p := controlapi.ClientEnabledPayload{TunnelID: r.TunnelID, ClientID: r.ClientID, Name: "phone"}
	if _, err := db.CreateClientMutation(ctx, id.ControllerID, issuer, id.NodeID, r.RefreshRequest, "client.create", p, now); !errors.Is(err, ErrOperationUnsupported) {
		t.Fatal("old capability admitted", err)
	}
	_, _ = db.sql.ExecContext(ctx, `UPDATE control_node_presence SET capabilities_json='["snapshot.v1","snapshot.refresh.v1","client.create.v1","client.delete.v1"]'`)
	if _, err := db.CreateClientMutation(ctx, id.ControllerID, issuer, id.NodeID, r.RefreshRequest, "client.create", p, now); !errors.Is(err, ErrOperationResourceConflict) {
		t.Fatal("collision adopted", err)
	}
	p.TunnelID = "missing"
	if _, err := db.CreateClientMutation(ctx, id.ControllerID, issuer, id.NodeID, r.RefreshRequest, "client.create", p, now); !errors.Is(err, ErrOperationTunnelMissing) {
		t.Fatal("missing tunnel", err)
	}
	p.TunnelID = r.TunnelID
	p.ClientID = "missing"
	p.Name = ""
	v := true
	p.Confirmed = &v
	if _, err := db.CreateClientMutation(ctx, id.ControllerID, issuer, id.NodeID, r.RefreshRequest, "client.delete", p, now); !errors.Is(err, ErrOperationClientMissing) {
		t.Fatal("missing client", err)
	}
	var count int
	_ = db.sql.QueryRowContext(ctx, "SELECT count(*) FROM control_refresh_operations").Scan(&count)
	if count != 0 {
		t.Fatal("phantom operations")
	}
}
