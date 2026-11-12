package nodeagent

import (
	"context"
	"net/http"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/google/uuid"
)

func runPendingClientResults(ctx context.Context, s *app.Service, c *http.Client, base string, p controlapi.OperationPoll) error {
	rows, err := s.PendingClientResults(ctx)
	if err != nil {
		return err
	}
	// One bounded outcome per loop preserves presence/renewal scheduling.
	if len(rows) == 0 {
		return nil
	}
	out := rows[0]
	if out.Status == "accepted" {
		out, err = s.ExecuteClientOperation(ctx, out.Operation)
		if err != nil {
			return err
		}
	}
	return sendClientOutcome(ctx, s, c, base, p, uuid.NewString(), out)
}
func sendClientOutcome(ctx context.Context, s *app.Service, c *http.Client, base string, p controlapi.OperationPoll, token string, out app.ClientOperationOutcome) error {
	state, err := s.NodeObservationState(ctx)
	if err != nil {
		return err
	}
	if state.ManagedNode.StateEpoch != out.Operation.StateEpoch || state.ManagedNode.BindingEpoch != out.Operation.BindingEpoch {
		return errFenced
	}
	p.Generation = state.ManagedNode.DesiredGeneration
	p.WaitSeconds = 0
	path := "/control/v1/node/operations/" + out.Operation.OperationID
	receipt := controlapi.OperationReceipt{OperationPoll: p, LeaseToken: token, Status: out.Status, Code: out.Code, ClientResult: out.Result, CreateResult: out.CreateResult, DeleteResult: out.DeleteResult, IntentHash: out.Operation.IntentHash()}
	// Current authenticated session carries a stable node outcome across boot,
	// lease expiry and lost responses. It does not fabricate a fresh snapshot.
	if out.AcknowledgedAt == nil {
		if err = requestJSON(ctx, c, base, http.MethodPut, path+"/result", "", receipt, nil); err != nil {
			return err
		}
		if err = s.AcknowledgeClientOutcome(ctx, out.Operation, false); err != nil {
			return err
		}
	}
	ack := controlapi.ClientResultAck{OperationPoll: p, IntentHash: out.Operation.IntentHash()}
	if err = requestJSON(ctx, c, base, http.MethodPost, path+"/ack", "", ack, nil); err != nil {
		return err
	}
	return s.AcknowledgeClientOutcome(ctx, out.Operation, true)
}
