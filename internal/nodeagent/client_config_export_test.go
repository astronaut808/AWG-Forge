package nodeagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/google/uuid"
)

func TestClientConfigExportFaultDoesNotStarveOperations(t *testing.T) {
	for _, status := range []int{404, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var controller string
			seen := make(chan struct{}, 1)
			server, service, cfg, id := observationWorkerFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/control/v1/node/presence":
					_ = json.NewEncoder(w).Encode(controlapi.PresenceAccepted{ControllerID: controller, SessionID: uuid.NewString(), SessionExpiresAt: time.Now().Add(time.Minute), NextPollSeconds: 1})
				case "/control/v1/node/snapshot":
					var s controlapi.Snapshot
					_ = json.NewDecoder(r.Body).Decode(&s)
					_ = json.NewEncoder(w).Encode(controlapi.SnapshotAccepted{Sequence: s.Sequence, ReceivedAt: time.Now()})
				case "/control/v1/node/client-config-exports/poll":
					w.WriteHeader(status)
				case "/control/v1/node/operations/poll":
					select {
					case seen <- struct{}{}:
					default:
					}
					w.WriteHeader(204)
				default:
					w.WriteHeader(404)
				}
			})
			controller = id
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- Run(ctx, service, cfg) }()
			select {
			case <-seen:
				cancel()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("ordinary poll starved")
			}
		})
	}
}

func TestClientConfigExportHTTPDeliveryStrictDecode(t *testing.T) {
	for _, body := range []string{
		`{"export_id":"` + uuid.NewString() + `","request":{},"authority":{},"expires_at":"2026-01-01T00:00:00Z"}`,
		`{"export_id":"` + uuid.NewString() + `","export_id":"` + uuid.NewString() + `","request":{},"authority":{},"expires_at":"2026-01-01T00:00:00Z"}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			// Malformed JSON must reach the decoder unchanged, including duplicate keys.
			_, _ = w.Write([]byte(body)) // nosemgrep: go.lang.security.audit.xss.no-direct-write-to-responsewriter.no-direct-write-to-responsewriter
		}))
		var output controlapi.ClientConfigExportDelivery
		err := requestJSON(context.Background(), server.Client(), server.URL, http.MethodPost, "/control/v1/node/client-config-exports/poll", "", nil, &output)
		server.Close()
		if err == nil {
			t.Fatal("malformed HTTP delivery accepted")
		}
	}
}
