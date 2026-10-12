package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/sqldb"
)

// ClientOperationOutcome is protected, bounded, secret-free node-local delivery
// history. SQLite remains optional. Success authority is its atomic state receipt.
type ClientOperationOutcome struct {
	ControllerID   string                          `json:"controller_id"`
	NodeID         string                          `json:"node_id"`
	Operation      controlapi.OperationEnvelope    `json:"operation"`
	Status         string                          `json:"status"`
	Code           string                          `json:"code"`
	Result         *controlapi.ClientEnabledResult `json:"result,omitempty"`
	CreateResult   *controlapi.ClientCreateResult  `json:"create_result,omitempty"`
	DeleteResult   *controlapi.ClientDeleteResult  `json:"delete_result,omitempty"`
	AcknowledgedAt *time.Time                      `json:"acknowledged_at,omitempty"`
	Confirmed      bool                            `json:"confirmed"`
}

// Decode the persisted typed intent with the same field-presence rules as wire
// input. In particular absent false booleans cannot become successful receipts.
func (o *ClientOperationOutcome) UnmarshalJSON(raw []byte) error {
	type plain ClientOperationOutcome
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ErrInvalidDesiredMutation
	}
	var operation controlapi.OperationEnvelope
	if controlapi.DecodeOperation(fields["operation"], &operation) != nil {
		return ErrInvalidDesiredMutation
	}
	for field, last := range map[string]string{"result": "enabled", "create_result": "name", "delete_result": "deleted"} {
		if body, ok := fields[field]; ok {
			var result map[string]json.RawMessage
			if json.Unmarshal(body, &result) != nil || len(result) != 4 {
				return ErrInvalidDesiredMutation
			}
			for _, key := range []string{"committed_desired_generation", "tunnel_id", "client_id", last} {
				if v, ok := result[key]; !ok || string(v) == "null" {
					return ErrInvalidDesiredMutation
				}
			}
		}
	}
	var decoded plain
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	// The storage decoder's DisallowUnknownFields cannot inspect a custom
	// unmarshaler, so enforce the fixed top-level allowlist here as well.
	allowed := map[string]bool{"controller_id": true, "node_id": true, "operation": true, "status": true, "code": true, "result": true, "create_result": true, "delete_result": true, "acknowledged_at": true, "confirmed": true}
	for k := range fields {
		if !allowed[k] {
			return ErrInvalidDesiredMutation
		}
	}
	*o = ClientOperationOutcome(decoded)
	return nil
}

const maxClientOutcomes = 256

func (s *Service) clientOutcomesLocked(state config.State) ([]ClientOperationOutcome, error) {
	var rows []ClientOperationOutcome
	err := s.store.LoadClientOperations(&rows)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(rows) > maxClientOutcomes {
		return nil, ErrOperationReceiptCapacity
	}
	seen := map[string]bool{}
	keys := map[string]bool{}
	for _, r := range rows {
		if !validControlUUID(r.ControllerID) || !validControlUUID(r.NodeID) || r.Operation.Validate() != nil || !controlapi.IsClientMutation(r.Operation.Operation.Kind) || seen[r.Operation.OperationID] || keys[r.Operation.IdempotencyKey] {
			return nil, ErrInvalidDesiredMutation
		}
		seen[r.Operation.OperationID] = true
		keys[r.Operation.IdempotencyKey] = true
		switch r.Status {
		case "accepted":
			if r.Code != "" || r.Result != nil || r.CreateResult != nil || r.DeleteResult != nil || r.AcknowledgedAt != nil || r.Confirmed {
				return nil, ErrInvalidDesiredMutation
			}
		case "succeeded":
			if !r.receipt().MatchesClientSuccess(r.Operation) {
				return nil, ErrInvalidDesiredMutation
			}
			if matchingClientAuthority(state, r) && !r.Confirmed {
				receipt, ok := successfulReceipt(state.ManagedNode.SuccessfulReceipts, r.Operation.OperationID)
				if !ok || receipt.IntentHash != r.Operation.IntentHash() || receipt.IdempotencyKeyHash != hashIdempotencyKey(r.Operation.IdempotencyKey) || receipt.DesiredGeneration != r.receipt().CommittedGeneration() {
					return nil, ErrInvalidDesiredMutation
				}
			}
		case "conflict":
			if r.Code != "generation_conflict" || r.Result != nil || r.CreateResult != nil || r.DeleteResult != nil {
				return nil, ErrInvalidDesiredMutation
			}
		case "failed":
			if r.Result != nil || r.CreateResult != nil || r.DeleteResult != nil || r.Code != "execution_expired" && r.Code != "interrupted_rolled_back" && r.Code != "apply_failed" && r.Code != "client_unavailable" && r.Code != "quota_exceeded" && r.Code != "receipt_limit" && r.Code != "tunnel_unavailable" && r.Code != "resource_conflict" && r.Code != "address_unavailable" {
				return nil, ErrInvalidDesiredMutation
			}
		default:
			return nil, ErrInvalidDesiredMutation
		}
		if r.Confirmed && r.AcknowledgedAt == nil {
			return nil, ErrInvalidDesiredMutation
		}
		if r.AcknowledgedAt != nil && (r.AcknowledgedAt.IsZero() || r.AcknowledgedAt.Before(r.Operation.IssuedAt)) {
			return nil, ErrInvalidDesiredMutation
		}
	}
	return rows, nil
}

func matchingClientAuthority(state config.State, r ClientOperationOutcome) bool {
	m := state.ManagedNode
	return m != nil && state.EffectiveMode() == config.ModeNode && m.NodeID == r.NodeID && m.ControllerID == r.ControllerID && m.StateEpoch == r.Operation.StateEpoch && m.BindingEpoch == r.Operation.BindingEpoch
}

func (s *Service) currentClientWorker(state config.State) bool {
	return state.ManagedNode != nil && s.managedBoot != nil && state.ManagedNode.StateEpoch == s.managedBoot.StateEpoch && state.ManagedNode.BootSequence == s.managedBoot.BootSequence
}

func (s *Service) pruneClientOutcomesLocked(state *config.State, rows []ClientOperationOutcome, now time.Time) ([]ClientOperationOutcome, error) {
	kept := make([]ClientOperationOutcome, 0, len(rows))
	prune := map[string]string{}
	for _, r := range rows {
		if r.Confirmed && r.AcknowledgedAt != nil && now.After(r.Operation.ExpiresAt.Add(controlapi.OperationRetention)) {
			prune[r.Operation.OperationID] = r.Operation.IntentHash()
			continue
		}
		kept = append(kept, r)
	}
	if len(prune) == 0 {
		return rows, nil
	}
	receipts := make([]config.DesiredStateReceipt, 0, len(state.ManagedNode.SuccessfulReceipts))
	for _, r := range state.ManagedNode.SuccessfulReceipts {
		if prune[r.OperationID] != r.IntentHash || r.IntentHash == "" || r.AcknowledgedAt == nil {
			receipts = append(receipts, r)
		}
	}
	state.ManagedNode.SuccessfulReceipts = receipts
	if err := s.store.Save(*state); err != nil {
		return nil, err
	}
	if err := s.store.SyncStateDirectory(); err != nil {
		return nil, err
	}
	if err := s.store.SaveClientOperations(kept); err != nil {
		return nil, err
	}
	return kept, nil
}

// ExecuteClientEnabled uses the existing cross-process mutation transaction.
// A persisted accepted attempt without a state receipt is never blindly applied
// again: init first reconciles runtime, then a fixed interrupted outcome is kept.
func (s *Service) ExecuteClientEnabled(ctx context.Context, o controlapi.OperationEnvelope) (ClientOperationOutcome, error) {
	if o.Operation.Kind != "client.enabled.set" {
		return ClientOperationOutcome{}, ErrInvalidDesiredMutation
	}
	return s.ExecuteClientOperation(ctx, o)
}

func (s *Service) ExecuteClientOperation(ctx context.Context, o controlapi.OperationEnvelope) (ClientOperationOutcome, error) {
	if o.Validate() != nil || !controlapi.IsClientMutation(o.Operation.Kind) {
		return ClientOperationOutcome{}, ErrInvalidDesiredMutation
	}
	if err := s.lockControlRequest(ctx); err != nil {
		return ClientOperationOutcome{}, err
	}
	defer s.unlockStateMutation()
	preflight, err := s.store.Load()
	if err != nil || preflight.ManagedNode == nil || preflight.EffectiveMode() != config.ModeNode {
		return ClientOperationOutcome{}, ErrNodeNotManaged
	}
	if !s.currentClientWorker(preflight) {
		return ClientOperationOutcome{}, ErrOperationReplayConflict
	}
	state, err := s.initLocked()
	if err != nil {
		return ClientOperationOutcome{}, err
	}
	if err = validateManagedNodeState(state.ManagedNode); err != nil {
		return ClientOperationOutcome{}, err
	}
	m := state.ManagedNode
	out := ClientOperationOutcome{ControllerID: m.ControllerID, NodeID: m.NodeID, Operation: o, Status: "accepted"}
	if !matchingClientAuthority(state, out) {
		return out, ErrOperationReplayConflict
	}
	rows, err := s.clientOutcomesLocked(state)
	if err != nil {
		return out, err
	}
	rows, err = s.pruneClientOutcomesLocked(&state, rows, time.Now())
	if err != nil {
		return out, err
	}
	found := -1
	for i, r := range rows {
		if r.Operation.OperationID == o.OperationID || r.Operation.IdempotencyKey == o.IdempotencyKey {
			if r.Operation.IntentHash() != o.IntentHash() || !matchingClientAuthority(state, r) {
				return out, ErrOperationReplayConflict
			}
			found = i
			out = r
			break
		}
	}
	if receipt, ok := successfulReceipt(state.ManagedNode.SuccessfulReceipts, o.OperationID); ok {
		if receipt.IntentHash != o.IntentHash() || receipt.IdempotencyKeyHash != hashIdempotencyKey(o.IdempotencyKey) {
			return out, ErrOperationReplayConflict
		}
		out.setCommitted(receipt.DesiredGeneration)
		s.cleanupDeletedClientPolicy(ctx, state, o)

		if found < 0 {
			if len(rows) >= maxClientOutcomes {
				return out, ErrOperationReceiptCapacity
			}
			rows = append(rows, out)
		} else {
			rows[found] = out
		}
		if err := s.store.SyncStateDirectory(); err != nil {
			return out, err
		}
		return out, s.store.SaveClientOperations(rows)
	}
	if found >= 0 {
		if out.Status == "accepted" {
			out.Status, out.Code = "failed", "interrupted_rolled_back"
			rows[found] = out
			if err := s.store.SaveClientOperations(rows); err != nil {
				return out, err
			}
		}
		return out, nil
	}
	if len(rows) >= maxClientOutcomes {
		return out, ErrOperationReceiptCapacity
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if state.ManagedNode.DesiredGeneration != o.Generation {
		out.Status, out.Code = "conflict", "generation_conflict"
	} else if !time.Now().Before(o.ExpiresAt) {
		out.Status, out.Code = "failed", "execution_expired"
	}
	rows = append(rows, out)
	index := len(rows) - 1
	// The durable full intent is written before candidate build or runtime apply.
	if err = s.store.SaveClientOperations(rows); err != nil {
		return out, err
	}
	if out.Status != "accepted" {
		return out, nil
	}
	p := o.Operation.Payload
	target, tunnelFound := tunnelIndexByID(state, p.TunnelID)
	clientFound := false
	collision := false
	for _, t := range state.Tunnels {
		for _, c := range t.Clients {
			if c.ID == p.ClientID {
				collision = true
				if t.ID == p.TunnelID {
					clientFound = true
				}
			}
		}
	}
	fail := func(code string) (ClientOperationOutcome, error) {
		out.Status, out.Code = "failed", code
		rows[index] = out
		return out, s.store.SaveClientOperations(rows)
	}
	if !tunnelFound {
		if o.Operation.Kind == "client.enabled.set" {
			return fail("client_unavailable")
		}
		return fail("tunnel_unavailable")
	}
	if o.Operation.Kind == "client.create" {
		if collision {
			return fail("resource_conflict")
		}
	} else if !clientFound {
		return fail("client_unavailable")
	}
	var created config.Client
	if o.Operation.Kind == "client.create" {
		created, err = newTunnelClient(state.Tunnels[target], p.ClientID, p.Name, time.Time{})
		if err != nil {
			return fail("address_unavailable")
		}
	}

	if o.Operation.Kind == "client.enabled.set" && *o.Operation.Payload.Enabled {
		qctx, cancel := context.WithTimeout(ctx, s.cfg.DatabaseQueryTimeout)
		exceeded, qerr := sqldb.ListExceededTrafficLimits(qctx, s.cfg, time.Now().UTC())
		cancel()
		if qerr != nil && !errors.Is(qerr, sql.ErrNoRows) && !errors.Is(qerr, sqldb.ErrDisabled) {
			return fail("apply_failed")
		}
		for _, x := range exceeded {
			if x.ClientID == o.Operation.Payload.ClientID {
				return fail("quota_exceeded")
			}
		}
	}
	if len(state.ManagedNode.SuccessfulReceipts) >= maxSuccessfulOperationReceipts {
		return fail("receipt_limit")
	}
	if o.Operation.Kind == "client.enabled.set" && !*o.Operation.Payload.Enabled {
		// Match manual local disable: retire an automatic quota-release marker
		// under this same lock, otherwise a later release could undo the disable.
		clearCtx, cancel := context.WithTimeout(ctx, s.cfg.DatabaseQueryTimeout)
		clearErr := sqldb.ClearClientTrafficLimitBlock(clearCtx, s.cfg, o.Operation.Payload.ClientID)
		cancel()
		if clearErr != nil && !errors.Is(clearErr, sqldb.ErrDisabled) && !errors.Is(clearErr, sql.ErrNoRows) {
			return fail("apply_failed")
		}
	}
	tunnelID := state.Tunnels[target].ID
	if !time.Now().Before(o.ExpiresAt) {
		return fail("execution_expired")
	}
	if err := ctx.Err(); err != nil {
		return fail("apply_failed")
	}
	apply := func(st config.State) error {
		if err := s.writeRenderedTunnelFiles(st, tunnelID); err != nil {
			return err
		}
		t := st.Tunnels[target]
		if s.cfg.ApplyConfig && t.Enabled {
			return s.runtimeOps.applyTunnel(t)
		}
		return nil
	}
	result, err := s.commitDesiredStateLocked(desiredStateMutation{
		Request: DesiredStateMutationRequest{OperationID: o.OperationID, IdempotencyKey: o.IdempotencyKey, ExpectedStateEpoch: o.StateEpoch, ExpectedBindingEpoch: o.BindingEpoch, ExpectedDesiredGeneration: o.Generation, IntentHash: o.IntentHash()},
		Mutate: func(candidate *config.State) error {
			switch o.Operation.Kind {
			case "client.create":
				candidate.Tunnels[target].Clients = append(candidate.Tunnels[target].Clients, created)
			case "client.delete":
				kept := make([]config.Client, 0, len(candidate.Tunnels[target].Clients)-1)
				for _, c := range candidate.Tunnels[target].Clients {
					if c.ID != p.ClientID {
						kept = append(kept, c)
					}
				}
				candidate.Tunnels[target].Clients = kept
			case "client.enabled.set":
				for i := range candidate.Tunnels[target].Clients {
					c := &candidate.Tunnels[target].Clients[i]
					if c.ID == p.ClientID {
						c.Enabled = *p.Enabled
						c.UpdatedAt = time.Now().UTC()
					}
				}
			}

			candidate.Tunnels[target].UpdatedAt = time.Now().UTC()
			return nil
		}, Apply: func(_, candidate config.State) error { return apply(candidate) }, Rollback: func(previous, _ config.State) error { return apply(previous) },
	})
	if err != nil {
		// Uncertain state commit or rollback leaves the runtime journal for init.
		if _, jerr := s.store.LoadPendingDesiredStateCommit(); !errors.Is(jerr, os.ErrNotExist) {
			return out, err
		}
		return fail("apply_failed")
	}
	out.setCommitted(result.Receipt.DesiredGeneration)
	if o.Operation.Kind == "client.enabled.set" && *o.Operation.Payload.Enabled {
		clearCtx, cancel := context.WithTimeout(ctx, s.cfg.DatabaseQueryTimeout)
		clearErr := sqldb.ClearClientTrafficLimitBlock(clearCtx, s.cfg, o.Operation.Payload.ClientID)
		cancel()
		if clearErr != nil && !errors.Is(clearErr, sqldb.ErrDisabled) && !errors.Is(clearErr, sql.ErrNoRows) {
			s.log("warn", "client.operation.quota_cleanup_failed", "client enabled; quota marker cleanup requires retry", nil, nil)
		}
	}
	committed, loadErr := s.store.Load()
	if loadErr == nil {
		s.cleanupDeletedClientPolicy(ctx, committed, o)
	}

	rows[index] = out
	return out, s.store.SaveClientOperations(rows)
}

func (o ClientOperationOutcome) receipt() controlapi.OperationReceipt {
	// Validation needs a valid transport envelope; these values are validation-only.
	return controlapi.OperationReceipt{OperationPoll: controlapi.OperationPoll{SessionID: o.Operation.OperationID, BootID: o.Operation.OperationID, BootSequence: 1, StateEpoch: o.Operation.StateEpoch, BindingEpoch: o.Operation.BindingEpoch, Generation: o.Operation.Generation + 1}, LeaseToken: o.Operation.OperationID, Status: o.Status, Code: o.Code, IntentHash: o.Operation.IntentHash(), ClientResult: o.Result, CreateResult: o.CreateResult, DeleteResult: o.DeleteResult}
}
func (o *ClientOperationOutcome) setCommitted(generation uint64) {
	o.Status, o.Code = "succeeded", controlapi.ClientCommittedCode(o.Operation.Operation.Kind)
	p := o.Operation.Operation.Payload
	switch o.Operation.Operation.Kind {
	case "client.enabled.set":
		o.Result = &controlapi.ClientEnabledResult{Generation: generation, TunnelID: p.TunnelID, ClientID: p.ClientID, Enabled: *p.Enabled}
	case "client.create":
		o.CreateResult = &controlapi.ClientCreateResult{Generation: generation, TunnelID: p.TunnelID, ClientID: p.ClientID, Name: p.Name}
	case "client.delete":
		o.DeleteResult = &controlapi.ClientDeleteResult{Generation: generation, TunnelID: p.TunnelID, ClientID: p.ClientID, Deleted: true}
	}
}

// Cleanup follows durable deletion. Retry during replay; never remove policy
// from a later local client reusing the deleted ID and never reverse the commit.
func (s *Service) cleanupDeletedClientPolicy(ctx context.Context, state config.State, o controlapi.OperationEnvelope) {
	if o.Operation.Kind != "client.delete" {
		return
	}
	for _, t := range state.Tunnels {
		for _, c := range t.Clients {
			if c.ID == o.Operation.Payload.ClientID {
				return
			}
		}
	}
	qctx, cancel := context.WithTimeout(ctx, s.cfg.DatabaseQueryTimeout)
	defer cancel()
	err := sqldb.DeleteClientTrafficLimit(qctx, s.cfg, o.Operation.Payload.ClientID)
	if err != nil && !errors.Is(err, sqldb.ErrDisabled) && !errors.Is(err, sql.ErrNoRows) {
		s.log("warn", "client.operation.policy_cleanup_failed", "client deleted; policy cleanup requires retry", nil, nil)
	}
}

// Unconfirmed results are retransmitted with current mTLS boot/session authority.
func (s *Service) PendingClientResults(ctx context.Context) ([]ClientOperationOutcome, error) {
	if err := s.lockControlRequest(ctx); err != nil {
		return nil, err
	}
	defer s.unlockStateMutation()
	preflight, err := s.store.Load()
	if err != nil || preflight.ManagedNode == nil || preflight.EffectiveMode() != config.ModeNode {
		return nil, ErrNodeNotManaged
	}
	if !s.currentClientWorker(preflight) {
		return nil, ErrOperationReplayConflict
	}
	state, err := s.initLocked()
	if err != nil {
		return nil, err
	}
	if state.ManagedNode == nil {
		return nil, nil
	}
	rows, err := s.clientOutcomesLocked(state)
	if err != nil {
		return nil, err
	}
	var result []ClientOperationOutcome
	for _, r := range rows {
		if matchingClientAuthority(state, r) && !r.Confirmed {
			result = append(result, r)
		}
	}
	return result, nil
}
func (s *Service) AcknowledgeClientOutcome(ctx context.Context, o controlapi.OperationEnvelope, confirmed bool) error {
	if err := s.lockControlRequest(ctx); err != nil {
		return err
	}
	defer s.unlockStateMutation()
	preflight, err := s.store.Load()
	if err != nil || !s.currentClientWorker(preflight) {
		return ErrOperationReplayConflict
	}
	state, err := s.initLocked()
	if err != nil {
		return err
	}
	rows, err := s.clientOutcomesLocked(state)
	if err != nil {
		return err
	}
	found := false
	for i, r := range rows {
		if r.Operation.OperationID == o.OperationID {
			if !matchingClientAuthority(state, r) || r.Operation.IntentHash() != o.IntentHash() || r.Status == "accepted" {
				return ErrOperationReplayConflict
			}
			found = true
			if rows[i].AcknowledgedAt == nil {
				now := time.Now().UTC()
				rows[i].AcknowledgedAt = &now
			}
			if confirmed {
				rows[i].Confirmed = true
			}
			if r.Status == "succeeded" {
				for j := range state.ManagedNode.SuccessfulReceipts {
					if state.ManagedNode.SuccessfulReceipts[j].OperationID == o.OperationID {
						state.ManagedNode.SuccessfulReceipts[j].AcknowledgedAt = rows[i].AcknowledgedAt
					}
				}
			}
		}
	}
	if !found {
		return ErrOperationReplayConflict
	}
	if err = s.store.Save(state); err != nil {
		return err
	}
	if err = s.store.SyncStateDirectory(); err != nil {
		return err
	}
	return s.store.SaveClientOperations(rows)
}
