package controlapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"time"

	"github.com/google/uuid"
)

const (
	RefreshCapability       = "snapshot.refresh.v1"
	ClientEnabledCapability = "client.enabled.set.v1"
	ClientCreateCapability  = "client.create.v1"
	ClientDeleteCapability  = "client.delete.v1"
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
	TunnelID  string `json:"tunnel_id,omitempty"`
	ClientID  string `json:"client_id,omitempty"`
	Enabled   *bool  `json:"enabled,omitempty"`
	Name      string `json:"name,omitempty"`
	Confirmed *bool  `json:"confirmed,omitempty"`
}

func (p ClientEnabledPayload) Valid() bool {
	return validResourceID(p.TunnelID) && validResourceID(p.ClientID) && p.Enabled != nil && p.Name == "" && p.Confirmed == nil
}

var clientCreateIDRE = regexp.MustCompile(`^[a-f0-9]{16}$`)
var clientNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_. -]{0,62}[A-Za-z0-9]$|^[A-Za-z0-9]$`)

func IsClientMutation(kind string) bool {
	return kind == "client.enabled.set" || kind == "client.create" || kind == "client.delete"
}
func OperationCapability(kind string) string {
	switch kind {
	case "snapshot.refresh":
		return RefreshCapability
	case "client.enabled.set":
		return ClientEnabledCapability
	case "client.create":
		return ClientCreateCapability
	case "client.delete":
		return ClientDeleteCapability
	}
	return ""
}
func ClientCommittedCode(kind string) string {
	switch kind {
	case "client.enabled.set":
		return "client_enabled_committed"
	case "client.create":
		return "client_created_committed"
	case "client.delete":
		return "client_deleted_committed"
	}
	return ""
}
func (p ClientEnabledPayload) ValidFor(kind string) bool {
	switch kind {
	case "client.enabled.set":
		return p.Valid()
	case "client.create":
		return validResourceID(p.TunnelID) && clientCreateIDRE.MatchString(p.ClientID) && clientNameRE.MatchString(p.Name) && p.Enabled == nil && p.Confirmed == nil
	case "client.delete":
		return validResourceID(p.TunnelID) && validResourceID(p.ClientID) && p.Confirmed != nil && *p.Confirmed && p.Enabled == nil && p.Name == ""
	}
	return false
}

type ClientCreateRequest struct {
	RefreshRequest
	TunnelID string `json:"tunnel_id"`
	ClientID string `json:"client_id"`
	Name     string `json:"name"`
}

func (r ClientCreateRequest) Payload() ClientEnabledPayload {
	return ClientEnabledPayload{TunnelID: r.TunnelID, ClientID: r.ClientID, Name: r.Name}
}
func (r ClientCreateRequest) Validate() error {
	if r.RefreshRequest.Validate() != nil || r.Generation >= math.MaxInt64 || !r.Payload().ValidFor("client.create") {
		return errors.New("invalid client create intent")
	}
	return nil
}

type ClientDeleteRequest struct {
	RefreshRequest
	TunnelID  string `json:"tunnel_id"`
	ClientID  string `json:"client_id"`
	Confirmed *bool  `json:"confirmed"`
}

func (r ClientDeleteRequest) Payload() ClientEnabledPayload {
	return ClientEnabledPayload{TunnelID: r.TunnelID, ClientID: r.ClientID, Confirmed: r.Confirmed}
}
func (r ClientDeleteRequest) Validate() error {
	if r.RefreshRequest.Validate() != nil || r.Generation >= math.MaxInt64 || !r.Payload().ValidFor("client.delete") {
		return errors.New("invalid client delete intent")
	}
	return nil
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
		if o.Operation.Payload.TunnelID != "" || o.Operation.Payload.ClientID != "" || o.Operation.Payload.Enabled != nil || o.Operation.Payload.Name != "" || o.Operation.Payload.Confirmed != nil {
			return errors.New("invalid refresh payload")
		}
	case "client.enabled.set", "client.create", "client.delete":
		if !o.Operation.Payload.ValidFor(o.Operation.Kind) || o.Generation >= math.MaxInt64 {
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
	if d.ReconciliationOnly && !IsClientMutation(d.Operation.Operation.Kind) {
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
	CreateResult *ClientCreateResult  `json:"create_result,omitempty"`
	DeleteResult *ClientDeleteResult  `json:"delete_result,omitempty"`
	IntentHash   string               `json:"intent_hash,omitempty"`
}

type ClientEnabledResult struct {
	Generation uint64 `json:"committed_desired_generation"`
	TunnelID   string `json:"tunnel_id"`
	ClientID   string `json:"client_id"`
	Enabled    bool   `json:"enabled"`
}

type ClientCreateResult struct {
	Generation uint64 `json:"committed_desired_generation"`
	TunnelID   string `json:"tunnel_id"`
	ClientID   string `json:"client_id"`
	Name       string `json:"name"`
}
type ClientDeleteResult struct {
	Generation uint64 `json:"committed_desired_generation"`
	TunnelID   string `json:"tunnel_id"`
	ClientID   string `json:"client_id"`
	Deleted    bool   `json:"deleted"`
}

// MatchesClientSuccess compares only the immutable intent and its committed generation.
func (r OperationReceipt) MatchesClientSuccess(o OperationEnvelope) bool {
	if r.Status != "succeeded" || r.Code != ClientCommittedCode(o.Operation.Kind) || r.Validate() != nil {
		return false
	}
	p := o.Operation.Payload
	generation := o.Generation + 1
	switch o.Operation.Kind {
	case "client.enabled.set":
		x := r.ClientResult
		return x != nil && x.Generation == generation && x.TunnelID == p.TunnelID && x.ClientID == p.ClientID && x.Enabled == *p.Enabled
	case "client.create":
		x := r.CreateResult
		return x != nil && x.Generation == generation && x.TunnelID == p.TunnelID && x.ClientID == p.ClientID && x.Name == p.Name
	case "client.delete":
		x := r.DeleteResult
		return x != nil && x.Generation == generation && x.TunnelID == p.TunnelID && x.ClientID == p.ClientID && x.Deleted
	}
	return false
}
func (r OperationReceipt) CommittedGeneration() uint64 {
	if r.ClientResult != nil {
		return r.ClientResult.Generation
	}
	if r.CreateResult != nil {
		return r.CreateResult.Generation
	}
	if r.DeleteResult != nil {
		return r.DeleteResult.Generation
	}
	return 0
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
			if !validIntentHash(r.IntentHash) || r.Result != nil || r.CreateResult != nil || r.DeleteResult != nil || r.ClientResult == nil || r.ClientResult.Generation == 0 || r.ClientResult.Generation > math.MaxInt64 || !validResourceID(r.ClientResult.TunnelID) || !validResourceID(r.ClientResult.ClientID) {
				return errors.New("invalid client result")
			}
			return nil
		}
		if r.Code == "client_created_committed" || r.Code == "client_deleted_committed" {
			if !validIntentHash(r.IntentHash) || r.Result != nil || r.ClientResult != nil {
				return errors.New("invalid client result")
			}
			if r.Code == "client_created_committed" {
				x := r.CreateResult
				if x == nil || r.DeleteResult != nil || x.Generation == 0 || x.Generation > math.MaxInt64 || !validResourceID(x.TunnelID) || !clientCreateIDRE.MatchString(x.ClientID) || !clientNameRE.MatchString(x.Name) {
					return errors.New("invalid create result")
				}
			} else {
				x := r.DeleteResult
				if x == nil || r.CreateResult != nil || x.Generation == 0 || x.Generation > math.MaxInt64 || !validResourceID(x.TunnelID) || !validResourceID(x.ClientID) || !x.Deleted {
					return errors.New("invalid delete result")
				}
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
		if r.Result != nil || r.Code != "collection_failed" && (!validIntentHash(r.IntentHash) || r.Code != "apply_failed" && r.Code != "execution_expired" && r.Code != "interrupted_rolled_back" && r.Code != "client_unavailable" && r.Code != "quota_exceeded" && r.Code != "receipt_limit" && r.Code != "tunnel_unavailable" && r.Code != "resource_conflict" && r.Code != "address_unavailable") {
			return errors.New("invalid failure")
		}
	default:
		return errors.New("invalid status")
	}
	if r.ClientResult != nil || r.CreateResult != nil || r.DeleteResult != nil || r.IntentHash != "" && !validIntentHash(r.IntentHash) || r.Result != nil && r.IntentHash != "" {
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
	CreateResult *ClientCreateResult  `json:"create_result,omitempty"`
	DeleteResult *ClientDeleteResult  `json:"delete_result,omitempty"`
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
	case *ClientCreateRequest:
		required = append(refresh, "tunnel_id", "client_id", "name")
	case *ClientDeleteRequest:
		required = append(refresh, "tunnel_id", "client_id", "confirmed")
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
	switch target.(type) {
	case *RefreshRequest, *ClientEnabledRequest, *ClientCreateRequest, *ClientDeleteRequest:
		if len(fields) != len(required) {
			return errors.New("invalid operation fields")
		}
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
	for field, last := range map[string]string{"client_result": "enabled", "create_result": "name", "delete_result": "deleted"} {
		if body, ok := fields[field]; ok {
			var result map[string]json.RawMessage
			if json.Unmarshal(body, &result) != nil || len(result) != 4 {
				return errors.New("invalid client result")
			}
			for _, key := range []string{"committed_desired_generation", "tunnel_id", "client_id", last} {
				if _, ok := result[key]; !ok {
					return errors.New("missing client result field")
				}
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
			if IsClientMutation(kind) {
				last := map[string]string{"client.enabled.set": "enabled", "client.create": "name", "client.delete": "confirmed"}[kind]
				if len(payload) != 3 {
					return errors.New("invalid client payload fields")
				}
				for _, key := range []string{"tunnel_id", "client_id", last} {
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
