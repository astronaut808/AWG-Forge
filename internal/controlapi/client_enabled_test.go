package controlapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestClientEnabledStrictJSONRequiresTypedPayload(t *testing.T) {
	enabled := true
	req := ClientEnabledRequest{RefreshRequest: RefreshRequest{OperationID: uuid.NewString(), IdempotencyKey: uuid.NewString(), StateEpoch: uuid.NewString(), BindingEpoch: 1}, ClientEnabledPayload: ClientEnabledPayload{TunnelID: "tunnel-1", ClientID: "client-1", Enabled: &enabled}}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ClientEnabledRequest
	if DecodeOperation(raw, &decoded) != nil || decoded.Validate() != nil {
		t.Fatal("valid typed request rejected")
	}
	for _, bad := range []string{strings.Replace(string(raw), `,"enabled":true`, "", 1), strings.Replace(string(raw), `"enabled":true`, `"enabled":null`, 1), strings.TrimSuffix(string(raw), "}") + `,"extra":true}`} {
		if DecodeOperation([]byte(bad), &decoded) == nil {
			t.Fatalf("invalid payload accepted: %s", bad)
		}
	}
	issued := time.Now().Add(-2 * time.Minute)
	delivery := OperationDelivery{LeaseToken: uuid.NewString(), LeaseExpiresAt: time.Now().Add(time.Minute), Operation: OperationEnvelope{RefreshRequest: req.RefreshRequest, IssuedAt: issued, ExpiresAt: issued.Add(time.Minute), Operation: RefreshOperation{Kind: "client.enabled.set", SchemaVersion: 1, Payload: req.ClientEnabledPayload}}, ReconciliationOnly: true}
	if delivery.Validate() != nil {
		t.Fatal("client reconciliation delivery rejected")
	}
}

func TestClientEnabledResultRequiresExplicitFalseAndRefreshPayloadStaysEmpty(t *testing.T) {
	req := RefreshRequest{OperationID: uuid.NewString(), IdempotencyKey: uuid.NewString(), StateEpoch: uuid.NewString(), BindingEpoch: 1}
	envelope := OperationEnvelope{RefreshRequest: req, IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute), Operation: RefreshOperation{Kind: "snapshot.refresh", SchemaVersion: 1}}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var got OperationEnvelope
	if err := DecodeOperation([]byte(strings.Replace(string(raw), `"payload":{}`, `"payload":{"tunnel_id":""}`, 1)), &got); err == nil {
		t.Fatal("refresh extensions accepted")
	}
	receipt := OperationReceipt{OperationPoll: OperationPoll{SessionID: uuid.NewString(), BootID: uuid.NewString(), BootSequence: 1, StateEpoch: req.StateEpoch, BindingEpoch: 1, Generation: 1}, LeaseToken: uuid.NewString(), Status: "succeeded", Code: "client_enabled_committed", IntentHash: strings.Repeat("a", 64), ClientResult: &ClientEnabledResult{Generation: 1, TunnelID: "awg0", ClientID: "phone", Enabled: false}}
	raw, err = json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var decoded OperationReceipt
	if DecodeOperation(raw, &decoded) != nil || decoded.Validate() != nil {
		t.Fatal("explicit false result rejected")
	}
	if DecodeOperation([]byte(strings.Replace(string(raw), `,"enabled":false`, "", 1)), &decoded) == nil {
		t.Fatal("missing false result accepted")
	}
}
