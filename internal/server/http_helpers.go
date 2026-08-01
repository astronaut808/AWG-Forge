package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
)

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
	cacheKey := action + ":" + key
	entry, owner := w.idempotencyEntry(cacheKey)
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

func (w *web) idempotencyEntry(key string) (*idempotencyEntry, bool) {
	now := time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	for k, entry := range w.idem {
		if now.Sub(entry.createdAt) > idempotencyTTL {
			delete(w.idem, k)
		}
	}
	if entry, ok := w.idem[key]; ok {
		return entry, false
	}
	entry := &idempotencyEntry{createdAt: now, ready: make(chan struct{})}
	w.idem[key] = entry
	return entry, true
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

func errorPayload(message string) map[string]any {
	return map[string]any{"error": message}
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
