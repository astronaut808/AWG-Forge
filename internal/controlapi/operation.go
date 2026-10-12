package controlapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"time"

	"github.com/google/uuid"
)

const (
	RefreshCapability  = "snapshot.refresh.v1"
	MaxOperationBytes  = 4096
	MaxOperations      = 4096
	MaxNodeOperations  = 64
	OperationTTL       = 2 * time.Minute
	OperationRetention = 24 * time.Hour
	OperationLease     = 40 * time.Second
	MaxPollWait        = 5
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
	Kind          string   `json:"kind"`
	SchemaVersion int      `json:"schema_version"`
	Payload       struct{} `json:"payload"`
}
type OperationEnvelope struct {
	RefreshRequest
	IssuedAt  time.Time        `json:"issued_at"`
	ExpiresAt time.Time        `json:"expires_at"`
	Operation RefreshOperation `json:"operation"`
}

func (o OperationEnvelope) Validate() error {
	if o.RefreshRequest.Validate() != nil || o.IssuedAt.IsZero() || !o.ExpiresAt.After(o.IssuedAt) || o.ExpiresAt.Sub(o.IssuedAt) > OperationTTL || o.Operation.Kind != "snapshot.refresh" || o.Operation.SchemaVersion != 1 {
		return errors.New("invalid operation")
	}
	return nil
}

// LeaseToken is a correlation fence, never an authentication credential.
type OperationDelivery struct {
	AcceptedSequence uint64            `json:"accepted_snapshot_sequence"`
	LeaseToken       string            `json:"lease_token"`
	LeaseExpiresAt   time.Time         `json:"lease_expires_at"`
	Operation        OperationEnvelope `json:"operation"`
}

func (d OperationDelivery) Validate() error {
	if !validUUID(d.LeaseToken) || d.Operation.Validate() != nil || d.LeaseExpiresAt.IsZero() || d.LeaseExpiresAt.After(d.Operation.ExpiresAt) {
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
	LeaseToken string         `json:"lease_token"`
	Status     string         `json:"status"`
	Code       string         `json:"code"`
	Result     *RefreshResult `json:"result,omitempty"`
}

func (r OperationReceipt) Validate() error {
	if r.OperationPoll.Validate() != nil || r.WaitSeconds != 0 || !validUUID(r.LeaseToken) {
		return errors.New("invalid receipt")
	}
	switch r.Status {
	case "succeeded":
		if r.Code != "snapshot_accepted" || r.Result == nil || !r.Result.SnapshotAccepted || r.Result.Sequence == 0 || r.Result.Sequence > math.MaxInt64 {
			return errors.New("invalid result")
		}
	case "conflict":
		if r.Code != "generation_conflict" || r.Result != nil {
			return errors.New("invalid conflict")
		}
	case "failed":
		if r.Code != "collection_failed" || r.Result != nil {
			return errors.New("invalid failure")
		}
	default:
		return errors.New("invalid status")
	}
	return nil
}

// Browser status omits certificate, session and lease material.
type OperationStatus struct {
	ControllerID string     `json:"controller_id"`
	NodeID       string     `json:"node_id"`
	OperationID  string     `json:"operation_id"`
	StateEpoch   string     `json:"state_epoch"`
	BindingEpoch uint64     `json:"binding_epoch"`
	Generation   uint64     `json:"expected_desired_generation"`
	Status       string     `json:"status"`
	Code         string     `json:"code"`
	IssuedAt     time.Time  `json:"issued_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
	CompletedAt  *time.Time `json:"completed_at"`
	Sequence     uint64     `json:"snapshot_sequence"`
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
	case *OperationPoll:
		required = poll
	case *OperationEnvelope:
		required = append(refresh, "issued_at", "expires_at", "operation")
	case *OperationReceipt:
		required = append(poll, "lease_token", "status", "code")
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
