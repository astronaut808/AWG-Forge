package controlapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	ClientConfigExportCapability = "client.config.export.v1"
	ClientConfigExportTTL        = 60 * time.Second
	MaxClientConfigBytes         = 16 << 10
	MaxClientConfigResultBytes   = 64 << 10
)

// These ephemeral messages never enter the durable operation queue or receipts.
type ClientConfigExportRequest struct {
	StateEpoch     string `json:"expected_state_epoch"`
	BindingEpoch   uint64 `json:"expected_binding_epoch"`
	Generation     uint64 `json:"expected_desired_generation"`
	TunnelID       string `json:"tunnel_id"`
	ClientID       string `json:"client_id"`
	TunnelRevision int    `json:"expected_tunnel_revision"`
}

func (r ClientConfigExportRequest) Validate() error {
	if !validUUID(r.StateEpoch) || r.StateEpoch == uuid.Nil.String() || r.BindingEpoch == 0 || r.BindingEpoch > math.MaxInt64 || r.Generation > math.MaxInt64 || !validResourceID(r.TunnelID) || !validResourceID(r.ClientID) || r.TunnelRevision < 1 || r.TunnelRevision > math.MaxInt32 {
		return errors.New("invalid client config export")
	}
	return nil
}

type ClientConfigExport struct {
	ExportID  string    `json:"export_id"`
	NodeID    string    `json:"node_id"`
	TunnelID  string    `json:"tunnel_id"`
	ClientID  string    `json:"client_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type ClientConfigExportLocator struct {
	ExportID string `json:"export_id"`
}

func (l ClientConfigExportLocator) Validate() error {
	if !validUUID(l.ExportID) || l.ExportID == uuid.Nil.String() {
		return errors.New("invalid export locator")
	}
	return nil
}

type ClientConfigExportDelivery struct {
	ClientConfigExportLocator
	Request   ClientConfigExportRequest `json:"request"`
	Authority OperationPoll             `json:"authority"`
	ExpiresAt time.Time                 `json:"expires_at"`
}

func (d ClientConfigExportDelivery) Validate() error {
	if d.ClientConfigExportLocator.Validate() != nil || d.Request.Validate() != nil || d.Authority.Validate() != nil || d.Authority.WaitSeconds != 0 || d.Authority.StateEpoch != d.Request.StateEpoch || d.Authority.BindingEpoch != d.Request.BindingEpoch || d.Authority.Generation != d.Request.Generation || d.ExpiresAt.IsZero() {
		return errors.New("invalid export delivery")
	}
	return nil
}

// Config is allowed only on the dedicated authenticated result endpoint.
type ClientConfigExportResult struct {
	ClientConfigExportLocator
	Authority OperationPoll `json:"authority"`
	Status    string        `json:"status"`
	Config    string        `json:"config"`
}

func (r ClientConfigExportResult) Validate() error {
	if r.ClientConfigExportLocator.Validate() != nil || r.Authority.Validate() != nil || r.Authority.WaitSeconds != 0 {
		return errors.New("invalid export result")
	}
	if r.Status == "failed" && r.Config == "" {
		return nil
	}
	if r.Status != "ready" || len(r.Config) == 0 || len(r.Config) > MaxClientConfigBytes || !utf8.ValidString(r.Config) {
		return errors.New("invalid export result")
	}
	return nil
}

// DecodeClientConfigExport rejects unknown, duplicate, omitted and null fields,
// including nested objects. Errors never include input or configuration bytes.
func DecodeClientConfigExport(raw []byte, value any) error {
	var fields []string
	limit := MaxOperationBytes
	switch value.(type) {
	case *ClientConfigExportRequest:
		fields = []string{"expected_state_epoch", "expected_binding_epoch", "expected_desired_generation", "tunnel_id", "client_id", "expected_tunnel_revision"}
	case *ClientConfigExportLocator:
		fields = []string{"export_id"}
	case *OperationPoll:
		fields = exportPollFields
	case *ClientConfigExportDelivery:
		fields = []string{"export_id", "request", "authority", "expires_at"}
	case *ClientConfigExportResult:
		fields = []string{"export_id", "authority", "status", "config"}
		limit = MaxClientConfigResultBytes
	default:
		return errors.New("invalid export message")
	}
	if len(raw) > limit {
		return errors.New("invalid export message")
	}
	object, err := exactExportObject(raw, fields)
	if err != nil {
		return err
	}
	for _, key := range []string{"authority", "request"} {
		if body, ok := object[key]; ok {
			if key == "authority" {
				var p OperationPoll
				err = DecodeClientConfigExport(body, &p)
			} else {
				var r ClientConfigExportRequest
				err = DecodeClientConfigExport(body, &r)
			}
			if err != nil {
				return err
			}
		}
	}
	if json.Unmarshal(raw, value) != nil {
		return errors.New("invalid export message")
	}
	return nil
}

var exportPollFields = []string{"session_id", "boot_id", "boot_sequence", "state_epoch", "binding_epoch", "desired_generation", "wait_seconds"}

func exactExportObject(raw []byte, fields []string) (map[string]json.RawMessage, error) {
	bad := errors.New("invalid export message")
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, bad
	}
	out := make(map[string]json.RawMessage, len(fields))
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return nil, bad
		}
		key, ok := t.(string)
		if !ok {
			return nil, bad
		}
		allowed := false
		for _, field := range fields {
			if key == field {
				allowed = true
				break
			}
		}
		if _, duplicate := out[key]; duplicate || !allowed {
			return nil, bad
		}
		var body json.RawMessage
		if d.Decode(&body) != nil || bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
			return nil, bad
		}
		out[key] = body
	}
	if _, err = d.Token(); err != nil || len(out) != len(fields) {
		return nil, bad
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return nil, bad
	}
	return out, nil
}
