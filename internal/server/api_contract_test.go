package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenAPIContractDocumentsCoreControlPlane(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.json"))
	if err != nil {
		t.Fatalf("read OpenAPI document: %v", err)
	}
	var document struct {
		OpenAPI    string                     `json:"openapi"`
		Paths      map[string]map[string]any  `json:"paths"`
		Components map[string]json.RawMessage `json:"components"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("OpenAPI document is not valid JSON: %v", err)
	}
	if got, want := document.OpenAPI, "3.1.1"; got != want {
		t.Fatalf("OpenAPI version = %q, want %q", got, want)
	}
	for path, method := range map[string]string{
		"/api/login":                      http.MethodPost,
		"/api/state":                      http.MethodGet,
		"/api/tunnels":                    http.MethodPost,
		"/api/tunnels/{id}/settings":      http.MethodPatch,
		"/api/tunnels/{id}/protocol":      http.MethodPatch,
		"/api/tunnels/{id}/regenerate":    http.MethodPost,
		"/api/clients":                    http.MethodPost,
		"/api/clients/{id}/traffic-limit": http.MethodPatch,
		"/api/warp":                       http.MethodGet,
		"/api/warp/import":                http.MethodPost,
	} {
		operations, ok := document.Paths[path]
		if !ok || operations[strings.ToLower(method)] == nil {
			t.Fatalf("OpenAPI document is missing %s %s", method, path)
		}
	}
	if !strings.Contains(string(document.Components["schemas"]), `"APIError"`) || !strings.Contains(string(document.Components["schemas"]), `"code"`) {
		t.Fatal("OpenAPI document must define the stable APIError code field")
	}
}

func TestAPIErrorResponseContract(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeOperationError(recorder, http.StatusConflict, "idempotency_key_reused", "idempotency key was already used with a different request")

	if got, want := recorder.Code, http.StatusConflict; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if got, want := recorder.Header().Get("Content-Type"), "application/json"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	var response apiErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if got, want := response.Code, "idempotency_key_reused"; got != want {
		t.Fatalf("code = %q, want %q", got, want)
	}
	if got, want := response.Error, "idempotency key was already used with a different request"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestAPIContractRegenerateProtocolRejectsMalformedJSON(t *testing.T) {
	w := &web{idem: map[string]*idempotencyEntry{}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://example.test/api/tunnels/tunnel-1/regenerate", strings.NewReader("{"))
	request.Header.Set("Origin", "http://example.test")
	request.Header.Set("Idempotency-Key", "regenerate-invalid-json")

	w.regenerateProtocolAPI(recorder, request, "tunnel-1")

	if got, want := recorder.Code, http.StatusBadRequest; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	var response apiErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if got, want := response.Code, "invalid_json"; got != want {
		t.Fatalf("code = %q, want %q", got, want)
	}
}

func TestIdempotencyReplaysOnlyMatchingRequest(t *testing.T) {
	w := &web{idem: map[string]*idempotencyEntry{}}
	calls := 0
	run := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "http://example.test/api/tunnels", strings.NewReader(body))
		request.Header.Set("Idempotency-Key", "create-tunnel-1")
		w.withIdempotency(recorder, request, "create-tunnel", func() (int, any) {
			calls++
			return http.StatusCreated, map[string]any{"call": calls}
		})
		return recorder
	}

	first := run(`{"name":"first"}`)
	if got, want := first.Code, http.StatusCreated; got != want {
		t.Fatalf("first status = %d, want %d", got, want)
	}
	second := run(`{"name":"first"}`)
	if got, want := second.Code, http.StatusCreated; got != want {
		t.Fatalf("replayed status = %d, want %d", got, want)
	}
	if got, want := second.Body.String(), first.Body.String(); got != want {
		t.Fatalf("replayed body = %q, want %q", got, want)
	}
	if got, want := calls, 1; got != want {
		t.Fatalf("handler calls = %d, want %d", got, want)
	}

	conflict := run(`{"name":"different"}`)
	if got, want := conflict.Code, http.StatusConflict; got != want {
		t.Fatalf("conflict status = %d, want %d", got, want)
	}
	var response apiErrorResponse
	if err := json.Unmarshal(conflict.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode conflict: %v", err)
	}
	if got, want := response.Code, "idempotency_key_reused"; got != want {
		t.Fatalf("conflict code = %q, want %q", got, want)
	}
	if got, want := calls, 1; got != want {
		t.Fatalf("handler calls after conflict = %d, want %d", got, want)
	}
}

func TestIdempotencyConcurrentDuplicateWaitsForInFlightResult(t *testing.T) {
	w := &web{idem: map[string]*idempotencyEntry{}}
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	defer func() {
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
	}()
	secondCallback := make(chan struct{}, 1)
	results := make(chan *httptest.ResponseRecorder, 2)
	var calls atomic.Int32

	run := func(first bool) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "http://example.test/api/tunnels", strings.NewReader(`{"name":"same"}`))
		request.Header.Set("Idempotency-Key", "create-tunnel-in-flight")
		w.withIdempotency(recorder, request, "create-tunnel", func() (int, any) {
			calls.Add(1)
			if first {
				close(firstStarted)
				<-releaseFirst
			} else {
				secondCallback <- struct{}{}
			}
			return http.StatusCreated, map[string]any{"created": true}
		})
		results <- recorder
	}

	go run(true)
	<-firstStarted

	// An active mutation must survive cache pruning, even after its normal TTL.
	w.mu.Lock()
	w.idem["create-tunnel:create-tunnel-in-flight"].createdAt = time.Now().Add(-idempotencyTTL - time.Second)
	w.mu.Unlock()
	go run(false)

	select {
	case <-secondCallback:
		t.Fatal("duplicate request executed a second mutation while the first was in flight")
	case <-results:
		t.Fatal("duplicate request returned before the in-flight mutation completed")
	case <-time.After(200 * time.Millisecond):
	}

	close(releaseFirst)
	first, second := <-results, <-results
	if got, want := calls.Load(), int32(1); got != want {
		t.Fatalf("handler calls = %d, want %d", got, want)
	}
	if got, want := second.Code, first.Code; got != want {
		t.Fatalf("replayed status = %d, want %d", got, want)
	}
	if got, want := second.Body.String(), first.Body.String(); got != want {
		t.Fatalf("replayed body = %q, want %q", got, want)
	}
}

func TestIdempotencyRejectsOversizedKeyAndBody(t *testing.T) {
	w := &web{idem: map[string]*idempotencyEntry{}}
	t.Run("key", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "http://example.test/api/tunnels", nil)
		request.Header.Set("Idempotency-Key", strings.Repeat("a", maxIdempotencyKeyBytes+1))
		w.withIdempotency(recorder, request, "create-tunnel", func() (int, any) {
			t.Fatal("handler must not run")
			return 0, nil
		})
		if got, want := recorder.Code, http.StatusBadRequest; got != want {
			t.Fatalf("status = %d, want %d", got, want)
		}
	})
	t.Run("body", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "http://example.test/api/tunnels", strings.NewReader(strings.Repeat("a", maxJSONBodyBytes+1)))
		request.Header.Set("Idempotency-Key", "large-request")
		w.withIdempotency(recorder, request, "create-tunnel", func() (int, any) {
			t.Fatal("handler must not run")
			return 0, nil
		})
		if got, want := recorder.Code, http.StatusRequestEntityTooLarge; got != want {
			t.Fatalf("status = %d, want %d", got, want)
		}
		var response apiErrorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode error response: %v", err)
		}
		if got, want := response.Code, "request_body_too_large"; got != want {
			t.Fatalf("code = %q, want %q", got, want)
		}
	})
}
