package controlapi

import (
	"encoding/json"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestClientCRUDStrictVariants(t *testing.T) {
	common := RefreshRequest{OperationID: uuid.NewString(), IdempotencyKey: uuid.NewString(), StateEpoch: uuid.NewString(), BindingEpoch: 1}
	yes := true
	for _, kind := range []string{"client.create", "client.delete"} {
		t.Run(kind, func(t *testing.T) {
			p := ClientEnabledPayload{TunnelID: "awg0", ClientID: "0123456789abcdef"}
			last := "name"
			p.Name = strings.Repeat("x", 64)
			if kind == "client.delete" {
				p.Name = ""
				p.Confirmed = &yes
				last = "confirmed"
			}
			o := OperationEnvelope{RefreshRequest: common, IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute), Operation: RefreshOperation{Kind: kind, SchemaVersion: 1, Payload: p}}
			raw, _ := json.Marshal(o)
			var decoded OperationEnvelope
			if DecodeOperation(raw, &decoded) != nil || decoded.Validate() != nil {
				t.Fatal("valid variant denied")
			}
			var fields map[string]any
			_ = json.Unmarshal(raw, &fields)
			payload := fields["operation"].(map[string]any)["payload"].(map[string]any)
			original := payload[last]
			for _, value := range []any{nil, false, "../bad", "", 1} {
				payload[last] = value
				bad, _ := json.Marshal(fields)
				if DecodeOperation(bad, &decoded) == nil && decoded.Validate() == nil {
					t.Fatalf("invalid variant admitted: %v", value)
				}
			}
			payload[last] = original
			payload["enabled"] = false
			bad, _ := json.Marshal(fields)
			if DecodeOperation(bad, &decoded) == nil {
				t.Fatal("mismatched field admitted")
			}
			delete(payload, "enabled")
			delete(payload, last)
			bad, _ = json.Marshal(fields)
			if DecodeOperation(bad, &decoded) == nil {
				t.Fatal("missing field admitted")
			}
		})
	}
}
func TestClientCRUDReceiptVariants(t *testing.T) {
	p := OperationPoll{SessionID: uuid.NewString(), BootID: uuid.NewString(), BootSequence: 1, StateEpoch: uuid.NewString(), BindingEpoch: 1, Generation: 1}
	for _, kind := range []string{"client.create", "client.delete"} {
		t.Run(kind, func(t *testing.T) {
			r := OperationReceipt{OperationPoll: p, LeaseToken: uuid.NewString(), Status: "succeeded", Code: ClientCommittedCode(kind), IntentHash: strings.Repeat("a", 64)}
			field, last := "create_result", "name"
			if kind == "client.create" {
				r.CreateResult = &ClientCreateResult{Generation: 1, TunnelID: "awg0", ClientID: "0123456789abcdef", Name: "phone"}
			} else {
				field, last = "delete_result", "deleted"
				r.DeleteResult = &ClientDeleteResult{Generation: 1, TunnelID: "awg0", ClientID: "phone", Deleted: true}
			}
			raw, _ := json.Marshal(r)
			var got OperationReceipt
			if DecodeOperation(raw, &got) != nil || got.Validate() != nil {
				t.Fatal("safe success rejected")
			}
			var fields map[string]any
			_ = json.Unmarshal(raw, &fields)
			delete(fields[field].(map[string]any), last)
			bad, _ := json.Marshal(fields)
			if DecodeOperation(bad, &got) == nil {
				t.Fatal("partial success accepted")
			}
			r.ClientResult = &ClientEnabledResult{Generation: 1, TunnelID: "awg0", ClientID: "phone"}
			if r.Validate() == nil {
				t.Fatal("mixed result accepted")
			}
		})
	}
}
