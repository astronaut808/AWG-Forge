package controlapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func exportTestRequest() ClientConfigExportRequest {
	return ClientConfigExportRequest{StateEpoch: uuid.NewString(), BindingEpoch: 1, Generation: 0, TunnelID: "tunnel", ClientID: "client", TunnelRevision: 1}
}

func TestClientConfigExportStrictContracts(t *testing.T) {
	r := exportTestRequest()
	p := OperationPoll{SessionID: uuid.NewString(), BootID: uuid.NewString(), BootSequence: 1, StateEpoch: r.StateEpoch, BindingEpoch: 1}
	d := ClientConfigExportDelivery{ClientConfigExportLocator: ClientConfigExportLocator{ExportID: uuid.NewString()}, Request: r, Authority: p, ExpiresAt: time.Now().Add(time.Minute)}
	result := ClientConfigExportResult{ClientConfigExportLocator: d.ClientConfigExportLocator, Authority: p, Status: "ready", Config: "disposable-test-config"}
	for _, pair := range []struct{ input, output any }{{r, &ClientConfigExportRequest{}}, {p, &OperationPoll{}}, {d, &ClientConfigExportDelivery{}}, {result, &ClientConfigExportResult{}}, {d.ClientConfigExportLocator, &ClientConfigExportLocator{}}} {
		raw, err := json.Marshal(pair.input)
		if err != nil {
			t.Fatal(err)
		}
		if DecodeClientConfigExport(raw, pair.output) != nil {
			t.Fatal("valid message rejected")
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			t.Fatal("fixture decode")
		}
		for key, value := range fields {
			delete(fields, key)
			omitted, _ := json.Marshal(fields)
			if DecodeClientConfigExport(omitted, pair.output) == nil {
				t.Fatal("omitted field admitted", key)
			}
			fields[key] = json.RawMessage("null")
			null, _ := json.Marshal(fields)
			if DecodeClientConfigExport(null, pair.output) == nil {
				t.Fatal("null field admitted", key)
			}
			fields[key] = value
			duplicate := append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"`+key+`":`+string(value)+`}`)...)
			if DecodeClientConfigExport(duplicate, pair.output) == nil {
				t.Fatal("duplicate field admitted", key)
			}
		}
		for _, candidate := range [][]byte{append(raw, raw...), []byte(`null`), []byte(`[]`), append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"private_key":"never-echo"}`)...)} {
			if err := DecodeClientConfigExport(candidate, pair.output); err == nil || strings.Contains(err.Error(), "never-echo") {
				t.Fatal("invalid message accepted or disclosed")
			}
		}
	}
	result.Config = strings.Repeat("x", MaxClientConfigBytes+1)
	if result.Validate() == nil {
		t.Fatal("oversize accepted")
	}
	result.Status = "failed"
	if result.Validate() == nil {
		t.Fatal("failed result with secret accepted")
	}
	result.Config = ""
	if result.Validate() != nil {
		t.Fatal("safe failure rejected")
	}
	r.TunnelID = "../escape"
	if r.Validate() == nil {
		t.Fatal("unsafe target")
	}
}
