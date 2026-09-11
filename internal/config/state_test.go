package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStandaloneStateJSONOmitsManagedNodeMetadata(t *testing.T) {
	encoded, err := json.Marshal(State{SchemaVersion: CurrentStateSchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "managed_node") {
		t.Fatalf("standalone state unexpectedly contains managed-node metadata: %s", encoded)
	}
}

func TestManagedNodeStateJSONRoundTrip(t *testing.T) {
	state := State{
		SchemaVersion: CurrentStateSchemaVersion,
		ManagedNode: &ManagedNodeState{
			NodeID:            "11111111-1111-4111-8111-111111111111",
			ControllerID:      "22222222-2222-4222-8222-222222222222",
			StateEpoch:        "33333333-3333-4333-8333-333333333333",
			BindingEpoch:      2,
			BootSequence:      3,
			DesiredGeneration: 4,
			SuccessfulReceipts: []DesiredStateReceipt{{
				OperationID:        "44444444-4444-4444-8444-444444444444",
				IdempotencyKeyHash: strings.Repeat("a", 64),
				DesiredGeneration:  4,
				CompletedAt:        time.Date(2026, time.September, 8, 8, 0, 0, 0, time.UTC),
			}},
		},
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var decoded State
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ManagedNode == nil || decoded.ManagedNode.BootSequence != 3 || decoded.ManagedNode.DesiredGeneration != 4 {
		t.Fatalf("managed-node metadata did not round-trip: %#v", decoded.ManagedNode)
	}
	if len(decoded.ManagedNode.SuccessfulReceipts) != 1 || decoded.ManagedNode.SuccessfulReceipts[0].OperationID != state.ManagedNode.SuccessfulReceipts[0].OperationID {
		t.Fatalf("successful receipts did not round-trip: %#v", decoded.ManagedNode.SuccessfulReceipts)
	}
}
