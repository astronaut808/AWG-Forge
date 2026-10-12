package controlapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestRefreshStrictJSONRejectsMissingNullAndArbitraryPayload(t *testing.T) {
	r := RefreshRequest{OperationID: uuid.NewString(), IdempotencyKey: uuid.NewString(), StateEpoch: uuid.NewString(), BindingEpoch: 1}
	raw, _ := json.Marshal(r)
	var decoded RefreshRequest
	if DecodeOperation(raw, &decoded) != nil || decoded.Validate() != nil {
		t.Fatal("valid zero generation rejected")
	}
	for _, bad := range []string{strings.Replace(string(raw), `"expected_desired_generation":0`, `"expected_desired_generation":null`, 1), strings.Replace(string(raw), `,"expected_desired_generation":0`, "", 1), strings.Replace(string(raw), `"expected_binding_epoch":1`, `"expected_binding_epoch":-1`, 1), strings.TrimSuffix(string(raw), "}") + `,"command":"id"}`, string(raw) + ` {}`, `null`} {
		if DecodeOperation([]byte(bad), &decoded) == nil && decoded.Validate() == nil {
			t.Fatal("invalid intent accepted", bad)
		}
	}
}

func TestRefreshRejectsNilIntentIDs(t *testing.T) {
	r := RefreshRequest{OperationID: uuid.NewString(), IdempotencyKey: uuid.NewString(), StateEpoch: uuid.NewString(), BindingEpoch: 1}
	for _, field := range []string{"operation", "idempotency"} {
		invalid := r
		if field == "operation" {
			invalid.OperationID = uuid.Nil.String()
		} else {
			invalid.IdempotencyKey = uuid.Nil.String()
		}
		if invalid.Validate() == nil {
			t.Fatal("nil intent ID accepted", field)
		}
	}
}
