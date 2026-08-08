package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
)

const maxIdempotencyKeyBytes = 128

var errIdempotencyBodyTooLarge = errors.New("idempotency request body exceeds limit")

type apiErrorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func mutationErrorStatus(err error, fallback int) int {
	var applyErr *app.ApplyError
	if errors.As(err, &applyErr) {
		return http.StatusInternalServerError
	}
	return fallback
}

func parseOptionalAPITime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func readJSON(rw http.ResponseWriter, r *http.Request, dst any) error {
	defer func() { _ = r.Body.Close() }()
	r.Body = http.MaxBytesReader(rw, r.Body, maxJSONBodyBytes)
	return json.NewDecoder(r.Body).Decode(dst)
}

func (w *web) withIdempotency(rw http.ResponseWriter, r *http.Request, action string, fn func() (int, any)) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		status, payload := fn()
		writeJSON(rw, status, payload)
		return
	}
	if len(key) > maxIdempotencyKeyBytes {
		writeError(rw, http.StatusBadRequest, "invalid idempotency key")
		return
	}
	fingerprint, err := idempotencyRequestFingerprint(r)
	if err != nil {
		if errors.Is(err, errIdempotencyBodyTooLarge) {
			writeError(rw, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeOperationError(rw, http.StatusBadRequest, "request_read_failed", "unable to read request")
		}
		return
	}
	cacheKey := action + ":" + key
	entry, owner, conflict := w.idempotencyEntry(cacheKey, fingerprint)
	if conflict {
		writeOperationError(rw, http.StatusConflict, "idempotency_key_reused", "idempotency key was already used with a different request")
		return
	}
	if !owner {
		<-entry.ready
		writeCachedJSON(rw, entry.status, entry.body)
		return
	}
	status, payload := fn()
	body, err := json.Marshal(payload)
	if err != nil {
		status = http.StatusInternalServerError
		body, _ = json.Marshal(errorPayload("failed to encode response"))
	}
	cachedBody := json.RawMessage(body)
	w.finishIdempotency(cacheKey, status, cachedBody)
	writeCachedJSON(rw, status, cachedBody)
}

func idempotencyRequestFingerprint(r *http.Request) (string, error) {
	if r.Body == nil {
		sum := sha256.Sum256(nil)
		return hex.EncodeToString(sum[:]), nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBodyBytes+1))
	if err != nil {
		return "", err
	}
	if err := r.Body.Close(); err != nil {
		return "", err
	}
	if len(body) > maxJSONBodyBytes {
		return "", errIdempotencyBodyTooLarge
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func (w *web) idempotencyEntry(key, fingerprint string) (*idempotencyEntry, bool, bool) {
	now := time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	for k, entry := range w.idem {
		if now.Sub(entry.createdAt) > idempotencyTTL {
			select {
			case <-entry.ready:
				delete(w.idem, k)
			default:
			}
		}
	}
	if entry, ok := w.idem[key]; ok {
		if entry.fingerprint != fingerprint {
			return nil, false, true
		}
		return entry, false, false
	}
	entry := &idempotencyEntry{fingerprint: fingerprint, createdAt: now, ready: make(chan struct{})}
	w.idem[key] = entry
	return entry, true, false
}

func (w *web) finishIdempotency(key string, status int, body json.RawMessage) {
	w.mu.Lock()
	entry := w.idem[key]
	entry.status = status
	entry.body = body
	close(entry.ready)
	w.mu.Unlock()
}

func writeJSON(rw http.ResponseWriter, status int, payload any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(payload)
}

func writeCachedJSON(rw http.ResponseWriter, status int, body json.RawMessage) {
	if !json.Valid(body) {
		writeError(rw, http.StatusInternalServerError, "failed to encode response")
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(body)
}

func writeRawResponse(rw http.ResponseWriter, body []byte) {
	// Callers set explicit Content-Type for trusted embedded assets or downloads.
	_, _ = rw.Write(body) // nosemgrep: go.lang.security.audit.xss.no-direct-write-to-responsewriter.no-direct-write-to-responsewriter
}

func writeError(rw http.ResponseWriter, status int, message string) {
	writeJSON(rw, status, errorPayload(message))
}

func writeOperationError(rw http.ResponseWriter, status int, code, message string) {
	writeJSON(rw, status, operationErrorPayload(code, message))
}

func errorPayload(message string) apiErrorResponse {
	return operationErrorPayload(errorCodeForMessage(message), message)
}

func operationErrorPayload(code, message string) apiErrorResponse {
	return apiErrorResponse{Error: message, Code: code}
}

func errorCodeForMessage(message string) string {
	switch message {
	case "unauthorized":
		return "unauthorized"
	case "forbidden":
		return "forbidden"
	case "not found", "client not found":
		return "not_found"
	case "method not allowed":
		return "method_not_allowed"
	case "invalid json":
		return "invalid_json"
	case "invalid idempotency key":
		return "invalid_idempotency_key"
	case "request body too large":
		return "request_body_too_large"
	default:
		return "request_failed"
	}
}

func configFilename(client config.Client) string {
	var b strings.Builder
	lastDash := false
	for _, r := range client.Name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '.', r == '_':
			b.WriteRune(r)
			lastDash = false
		case r == ' ', r == '-':
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	name := strings.Trim(b.String(), ".-_")
	if name == "" {
		return client.ID
	}
	return name
}
