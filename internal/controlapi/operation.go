package controlapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"time"

	"github.com/google/uuid"
)

const (
	RefreshCapability       = "snapshot.refresh.v1"
	ClientEnabledCapability = "client.enabled.set.v1"
	MaxOperationBytes       = 4096
	MaxOperations           = 4096
	MaxNodeOperations       = 64
	OperationTTL            = 2 * time.Minute
	OperationRetention      = 24 * time.Hour
	OperationLease          = 40 * time.Second
	MaxPollWait             = 5
)

// RefreshRequest has no free-form payload. IDs are immutable within retention.
type RefreshRequest struct {
	OperationID    string `json:"operation_id"`
	IdempotencyKey string `json:"idempotency_key"`
	StateEpoch     string `json:"expected_state_epoch"`
	BindingEpoch   uint64 `json:"expected_binding_epoch"`
	Generation     uint64 `json:"expected_desired_generation"`
}

func (r RefreshRequest) Validate() error {
	if !validUUID(r.OperationID) || r.OperationID == uuid.Nil.String() || !validUUID(r.IdempotencyKey) || r.IdempotencyKey == uuid.Nil.String() || !validUUID(r.StateEpoch) || r.BindingEpoch == 0 || r.BindingEpoch > math.MaxInt64 || r.Generation > math.MaxInt64 {
		return errors.New("invalid refresh")
	}
	return nil
}

type OperationPoll struct {
	SessionID    string `json:"session_id"`
	BootID       string `json:"boot_id"`
	BootSequence uint64 `json:"boot_sequence"`
	StateEpoch   string `json:"state_epoch"`
	BindingEpoch uint64 `json:"binding_epoch"`
	Generation   uint64 `json:"desired_generation"`
	WaitSeconds  int    `json:"wait_seconds"`
}

func (p OperationPoll) Validate() error {
	if !validUUID(p.SessionID) || !validUUID(p.BootID) || !validUUID(p.StateEpoch) || p.BootSequence == 0 || p.BootSequence > math.MaxInt64 || p.BindingEpoch == 0 || p.BindingEpoch > math.MaxInt64 || p.Generation > math.MaxInt64 || p.WaitSeconds < 0 || p.WaitSeconds > MaxPollWait {
		return errors.New("invalid poll")
	}
	return nil
}

type RefreshOperation struct {
	Kind          string               `json:"kind"`
	SchemaVersion int                  `json:"schema_version"`
	Payload       ClientEnabledPayload `json:"payload"`
}

type ClientEnabledPayload struct {
	TunnelID string `json:"tunnel_id,omitempty"`
	ClientID string `json:"client_id,omitempty"`
	Enabled  *bool  `json:"enabled,omitempty"`
}

func (p ClientEnabledPayload) Valid() bool {
	return validResourceID(p.TunnelID) && validResourceID(p.ClientID) && p.Enabled != nil
}

func validResourceID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

type ClientEnabledRequest struct {
	RefreshRequest
	ClientEnabledPayload
}

func (r ClientEnabledRequest) Validate() error {
	if r.RefreshRequest.Validate() != nil || !r.Valid() || r.Generation >= math.MaxInt64 {
		return errors.New("invalid client enabled intent")
	}
	return nil
}

type OperationEnvelope struct {
	RefreshRequest
	IssuedAt  time.Time        `json:"issued_at"`
	ExpiresAt time.Time        `json:"expires_at"`
	Operation RefreshOperation `json:"operation"`
}

func (o OperationEnvelope) Validate() error {
	if o.RefreshRequest.Validate() != nil || o.IssuedAt.IsZero() || !o.ExpiresAt.After(o.IssuedAt) || o.ExpiresAt.Sub(o.IssuedAt) > OperationTTL || o.Operation.SchemaVersion != 1 {
		return errors.New("invalid operation")
	}
	switch o.Operation.Kind {
	case "snapshot.refresh":
		if o.Operation.Payload.TunnelID != "" || o.Operation.Payload.ClientID != "" || o.Operation.Payload.Enabled != nil {
			return errors.New("invalid refresh payload")
		}
	case "client.enabled.set":
		if !o.Operation.Payload.Valid() || o.Generation >= math.MaxInt64 {
			return errors.New("invalid client payload")
		}
	default:
		return errors.New("unsupported operation")
	}
	return nil
}

// IntentHash binds the complete immutable typed envelope, including its deadline.
func (o OperationEnvelope) IntentHash() string {
	raw, _ := json.Marshal(o)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// LeaseToken is a correlation fence, never an authentication credential.
type OperationDelivery struct {
	AcceptedSequence   uint64            `json:"accepted_snapshot_sequence"`
	LeaseToken         string            `json:"lease_token"`
	LeaseExpiresAt     time.Time         `json:"lease_expires_at"`
	Operation          OperationEnvelope `json:"operation"`
	ReconciliationOnly bool              `json:"reconciliation_only,omitempty"`
}

func (d OperationDelivery) Validate() error {
	if d.ReconciliationOnly && d.Operation.Operation.Kind != "client.enabled.set" {
		return errors.New("invalid reconciliation kind")
	}
	if !validUUID(d.LeaseToken) || d.Operation.Validate() != nil || d.LeaseExpiresAt.IsZero() || d.LeaseExpiresAt.After(d.Operation.ExpiresAt) && !d.ReconciliationOnly {
		return errors.New("invalid delivery")
	}
	return nil
}

type RefreshCorrelation struct {
	OperationID string `json:"operation_id"`
	LeaseToken  string `json:"lease_token"`
}

func (c RefreshCorrelation) Valid() bool { return validUUID(c.OperationID) && validUUID(c.LeaseToken) }

type RefreshResult struct {
	SnapshotAccepted bool   `json:"snapshot_accepted"`
	Sequence         uint64 `json:"snapshot_sequence"`
}

// Safe fixed codes avoid arbitrary diagnostics crossing this boundary.
type OperationReceipt struct {
	OperationPoll
	LeaseToken   string               `json:"lease_token"`
	Status       string               `json:"status"`
	Code         string               `json:"code"`
	Result       *RefreshResult       `json:"result,omitempty"`
	ClientResult *ClientEnabledResult `json:"client_result,omitempty"`
	IntentHash   string               `json:"intent_hash,omitempty"`
}

type ClientEnabledResult struct {
	Generation uint64 `json:"committed_desired_generation"`
	TunnelID   string `json:"tunnel_id"`
	ClientID   string `json:"client_id"`
	Enabled    bool   `json:"enabled"`
}

func validIntentHash(hash string) bool {
	b, err := hex.DecodeString(hash)
	return err == nil && len(b) == sha256.Size && hex.EncodeToString(b) == hash
}

func (r OperationReceipt) Validate() error {
	if r.OperationPoll.Validate() != nil || r.WaitSeconds != 0 || !validUUID(r.LeaseToken) {
		return errors.New("invalid receipt")
	}
	switch r.Status {
	case "succeeded":
		if r.Code == "client_enabled_committed" {
			if !validIntentHash(r.IntentHash) || r.Result != nil || r.ClientResult == nil || r.ClientResult.Generation == 0 || r.ClientResult.Generation > math.MaxInt64 || !validResourceID(r.ClientResult.TunnelID) || !validResourceID(r.ClientResult.ClientID) {
				return errors.New("invalid client result")
			}
			return nil
		}
		if r.Code != "snapshot_accepted" || r.Result == nil || !r.Result.SnapshotAccepted || r.Result.Sequence == 0 || r.Result.Sequence > math.MaxInt64 {
			return errors.New("invalid result")
		}
	case "conflict":
		if r.Code != "generation_conflict" || r.Result != nil {
			return errors.New("invalid conflict")
		}
	case "failed":
		if r.Result != nil || r.Code != "collection_failed" && (!validIntentHash(r.IntentHash) || r.Code != "apply_failed" && r.Code != "execution_expired" && r.Code != "interrupted_rolled_back" && r.Code != "client_unavailable" && r.Code != "quota_exceeded" && r.Code != "receipt_limit") {
			return errors.New("invalid failure")
		}
	default:
		return errors.New("invalid status")
	}
	if r.ClientResult != nil || r.IntentHash != "" && !validIntentHash(r.IntentHash) || r.Result != nil && r.IntentHash != "" {
		return errors.New("invalid result kind")
	}
	return nil
}

// Browser status omits certificate, session and lease material.
type OperationStatus struct {
	ControllerID string               `json:"controller_id"`
	NodeID       string               `json:"node_id"`
	OperationID  string               `json:"operation_id"`
	StateEpoch   string               `json:"state_epoch"`
	BindingEpoch uint64               `json:"binding_epoch"`
	Generation   uint64               `json:"expected_desired_generation"`
	Status       string               `json:"status"`
	Code         string               `json:"code"`
	IssuedAt     time.Time            `json:"issued_at"`
	ExpiresAt    time.Time            `json:"expires_at"`
	CompletedAt  *time.Time           `json:"completed_at"`
	Sequence     uint64               `json:"snapshot_sequence"`
	Kind         string               `json:"kind,omitempty"`
	ClientResult *ClientEnabledResult `json:"client_result,omitempty"`
}

type ClientResultAck struct {
	OperationPoll
	IntentHash string `json:"intent_hash"`
}

func (r ClientResultAck) Validate() error {
	if r.OperationPoll.Validate() != nil || r.WaitSeconds != 0 || !validIntentHash(r.IntentHash) {
		return errors.New("invalid client acknowledgement")
	}
	return nil
}

// DecodeOperation rejects unknown, missing and null fields before admission.
// Empty payload is a required object; no arbitrary extension bag is accepted.
func DecodeOperation(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > MaxOperationBytes {
		return errors.New("invalid operation JSON")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return errors.New("invalid operation JSON")
	}
	var required []string
	poll := []string{"session_id", "boot_id", "boot_sequence", "state_epoch", "binding_epoch", "desired_generation", "wait_seconds"}
	refresh := []string{"operation_id", "idempotency_key", "expected_state_epoch", "expected_binding_epoch", "expected_desired_generation"}
	switch target.(type) {
	case *RefreshRequest:
		required = refresh
	case *ClientEnabledRequest:
		required = append(refresh, "tunnel_id", "client_id", "enabled")
	case *OperationPoll:
		required = poll
	case *OperationEnvelope:
		required = append(refresh, "issued_at", "expires_at", "operation")
	case *OperationReceipt:
		required = append(poll, "lease_token", "status", "code")
	case *ClientResultAck:
		required = append(poll, "intent_hash")
	case *OperationDelivery:
		required = []string{"lease_token", "lease_expires_at", "accepted_snapshot_sequence", "operation"}
	default:
		return errors.New("unsupported operation JSON")
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return errors.New("missing operation field")
		}
	}
	var generic any
	if json.Unmarshal(raw, &generic) != nil || containsNull(generic) {
		return errors.New("null operation field")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return errors.New("invalid operation JSON")
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return errors.New("invalid operation JSON")
	}
	if body, ok := fields["client_result"]; ok {
		var result map[string]json.RawMessage
		if json.Unmarshal(body, &result) != nil || len(result) != 4 {
			return errors.New("invalid client result")
		}
		for _, key := range []string{"committed_desired_generation", "tunnel_id", "client_id", "enabled"} {
			if _, ok := result[key]; !ok {
				return errors.New("missing client result field")
			}
		}
	}
	if body, ok := fields["operation"]; ok {
		switch target.(type) {
		case *OperationDelivery:
			var nested OperationEnvelope
			return DecodeOperation(body, &nested)
		case *OperationEnvelope:
			var operation map[string]json.RawMessage
			if json.Unmarshal(body, &operation) != nil || len(operation) != 3 {
				return errors.New("invalid typed operation")
			}
			for _, key := range []string{"kind", "schema_version", "payload"} {
				if _, ok := operation[key]; !ok {
					return errors.New("invalid typed operation")
				}
			}
			var kind string
			_ = json.Unmarshal(operation["kind"], &kind)
			var payload map[string]json.RawMessage
			if json.Unmarshal(operation["payload"], &payload) != nil || payload == nil {
				return errors.New("invalid payload")
			}
			if kind == "client.enabled.set" {
				for _, key := range []string{"tunnel_id", "client_id", "enabled"} {
					if _, ok := payload[key]; !ok {
						return errors.New("missing client field")
					}
				}
			} else if kind == "snapshot.refresh" && len(payload) != 0 {
				return errors.New("invalid refresh payload")
			}
		}
	}
	return nil
}
func containsNull(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case map[string]any:
		for _, e := range x {
			if containsNull(e) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if containsNull(e) {
				return true
			}
		}
	}
	return false
}
