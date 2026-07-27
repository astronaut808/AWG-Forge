package server

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const slowRequestThreshold = time.Second

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (rw *statusRecorder) WriteHeader(status int) {
	if rw.status != 0 {
		return
	}
	rw.status = status
	rw.ResponseWriter.WriteHeader(status)
}

func (rw *statusRecorder) Write(body []byte) (int, error) {
	if rw.status == 0 {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.ResponseWriter.Write(body)
}

func (rw *statusRecorder) Flush() {
	if rw.status == 0 {
		rw.WriteHeader(http.StatusOK)
	}
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (rw *statusRecorder) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

func (w *web) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		requestID := newRequestID()
		rw.Header().Set("X-Request-ID", requestID)
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: rw}
		next.ServeHTTP(recorder, r)

		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		duration := time.Since(started)
		level := requestLogLevel(status, duration, requestRoute(r.URL.Path))
		if level == slog.LevelDebug && !w.service.RuntimeLog().Enabled(level) {
			return
		}
		w.service.RuntimeLog().Log(r.Context(), level, "http", "http.request.completed", "HTTP request completed", map[string]any{
			"request_id":  requestID,
			"method":      r.Method,
			"route":       requestRoute(r.URL.Path),
			"status":      status,
			"duration_ms": duration.Milliseconds(),
		}, nil)
	})
}

func requestLogLevel(status int, duration time.Duration, route string) slog.Level {
	if status >= http.StatusInternalServerError {
		return slog.LevelError
	}
	if status == http.StatusTooManyRequests || (duration >= slowRequestThreshold && route != "/api/events") {
		return slog.LevelWarn
	}
	return slog.LevelDebug
}

func requestRoute(path string) string {
	switch {
	case path == "/":
		return "/"
	case path == "/api/login", path == "/api/logout", path == "/api/state", path == "/api/events", path == "/api/backup", path == "/api/doctor", path == "/api/audit-log", path == "/api/traffic-summary", path == "/api/firewall/repair", path == "/api/support-bundle", path == "/api/updates", path == "/api/restore/verify", path == "/api/warp", path == "/api/tunnels/suggestion", path == "/api/tunnels", path == "/api/clients":
		return path
	case strings.HasPrefix(path, "/static/"):
		return "/static/*"
	case strings.HasPrefix(path, "/clients/config/"):
		return "/clients/config/{id}"
	case strings.HasPrefix(path, "/api/tunnels/"):
		return "/api/tunnels/{id}"
	case strings.HasPrefix(path, "/api/clients/"):
		return "/api/clients/{id}"
	case strings.HasPrefix(path, "/api/warp/"):
		return "/api/warp/{action}"
	case strings.HasPrefix(path, "/api/"):
		return "/api/unknown"
	default:
		return "/unknown"
	}
}

func newRequestID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(raw[:])
}
