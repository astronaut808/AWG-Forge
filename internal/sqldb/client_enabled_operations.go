package sqldb

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
)

func validateClientOutcome(o controlapi.OperationEnvelope, r controlapi.OperationReceipt) error {
	if r.Validate() != nil || r.IntentHash != o.IntentHash() || r.StateEpoch != o.StateEpoch || r.BindingEpoch != o.BindingEpoch || r.Result != nil {
		return ErrOperationFenced
	}
	switch r.Status {
	case "succeeded":
		if !r.MatchesClientSuccess(o) || r.Generation < r.CommittedGeneration() {
			return ErrOperationFenced
		}

	case "conflict":
		if r.Code != "generation_conflict" || r.Generation == o.Generation {
			return ErrOperationFenced
		}
	case "failed":
		if r.Code == "collection_failed" || r.ClientResult != nil || r.CreateResult != nil || r.DeleteResult != nil {
			return ErrOperationFenced
		}
	default:
		return ErrOperationFenced
	}
	return nil
}
func clientSemanticReceipt(r controlapi.OperationReceipt) []byte {
	raw, _ := json.Marshal(struct {
		Status, Code, IntentHash string
		Result                   *controlapi.ClientEnabledResult
		Create                   *controlapi.ClientCreateResult
		Delete                   *controlapi.ClientDeleteResult
	}{r.Status, r.Code, r.IntentHash, r.ClientResult, r.CreateResult, r.DeleteResult})
	return raw
}
func completeClientEnabled(ctx context.Context, tx *sql.Tx, row refreshRow, o controlapi.OperationEnvelope, r controlapi.OperationReceipt, now time.Time) error {
	if row.token == "" || validateClientOutcome(o, r) != nil {
		return ErrOperationFenced
	}
	var cap int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM control_node_presence p,json_each(p.capabilities_json) c WHERE p.node_id=? AND c.value=?`, row.node, controlapi.OperationCapability(o.Operation.Kind)).Scan(&cap); err != nil {
		return ErrOperationFenced
	}
	if terminalRefresh(row.status) {
		var old controlapi.OperationReceipt
		if controlapi.DecodeOperation(row.receipt, &old) != nil || !bytes.Equal(clientSemanticReceipt(old), clientSemanticReceipt(r)) {
			return ErrOperationFenced
		}
		return tx.Commit()
	}
	if row.status != "leased" {
		return ErrOperationFenced
	}
	raw, _ := json.Marshal(r)
	// Current boot/session is authenticated above. A durable immutable node
	// outcome survives its old transport lease and execution deadline.
	_, err := tx.ExecContext(ctx, `UPDATE control_refresh_operations SET status=?,code=?,receipt_json=?,completed_at_unix_ms=? WHERE operation_id=?`, r.Status, r.Code, raw, now.UnixMilli(), row.id)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (db *DB) AcknowledgeClientResult(ctx context.Context, id NodeIdentity, issuer, serial, operation string, a controlapi.ClientResultAck, now time.Time) error {
	if db == nil || db.sql == nil {
		return ErrDisabled
	}
	if a.Validate() != nil || !validUUID(operation) {
		return ErrOperationFenced
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = refreshAuthority(ctx, tx, id, issuer, serial, a.OperationPoll, now); err != nil {
		return err
	}
	var capability int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM control_node_presence p,json_each(p.capabilities_json) c WHERE p.node_id=? AND c.value IN (?,?,?)`, id.NodeID, controlapi.ClientEnabledCapability, controlapi.ClientCreateCapability, controlapi.ClientDeleteCapability).Scan(&capability); err != nil {
		return ErrOperationFenced
	}
	row, err := scanRefresh(tx.QueryRowContext(ctx, `SELECT `+refreshColumns+` FROM control_refresh_operations WHERE operation_id=? AND node_id=? AND controller_id=?`, operation, id.NodeID, id.ControllerID))
	if errors.Is(err, sql.ErrNoRows) {
		// The node has durably recorded PUT /result acknowledgement before it
		// sends this message. Retention may already have removed the row after
		// a lost ACK response; no retained row needs acknowledgement now.
		// This creates neither a result nor proof of a new state commit.
		return tx.Commit()
	}
	if err != nil {
		return ErrOperationFenced
	}
	o, err := row.intent()
	if err != nil || !controlapi.IsClientMutation(o.Operation.Kind) || o.IntentHash() != a.IntentHash || o.StateEpoch != a.StateEpoch || o.BindingEpoch != a.BindingEpoch || len(row.receipt) == 0 || !terminalRefresh(row.status) {
		return ErrOperationFenced
	}
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM control_node_presence p,json_each(p.capabilities_json) c WHERE p.node_id=? AND c.value=?`, id.NodeID, controlapi.OperationCapability(o.Operation.Kind)).Scan(&capability); err != nil {
		return ErrOperationFenced
	}

	_, err = tx.ExecContext(ctx, `UPDATE control_refresh_operations SET node_acknowledged_at_unix_ms=COALESCE(node_acknowledged_at_unix_ms,?) WHERE operation_id=?`, now.UnixMilli(), row.id)
	if err != nil {
		return err
	}
	return tx.Commit()
}
