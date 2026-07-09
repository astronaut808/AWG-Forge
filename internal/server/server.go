package server

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/audit"
	"github.com/astronaut808/awg-forge/internal/backup"
	"github.com/astronaut808/awg-forge/internal/buildinfo"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/doctor"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/support"
	"github.com/astronaut808/awg-forge/internal/updates"
	"github.com/boombuler/barcode/qr"
)

//go:embed static/*
var staticFiles embed.FS

type web struct {
	cfg      config.Config
	service  *app.Service
	sessions []byte
	limits   map[string][]time.Time
	idem     map[string]*idempotencyEntry
	mu       sync.Mutex
}

const idempotencyTTL = 10 * time.Minute
const maxJSONBodyBytes = 1 << 20
const clientQRTargetSize = 1024
const clientQRQuietZoneModules = 4
const clientQRMinModulePixels = 4

type idempotencyEntry struct {
	status    int
	body      []byte
	createdAt time.Time
	ready     chan struct{}
}

func Serve(cfg config.Config, service *app.Service) error {
	secret, err := service.SessionSecret()
	if err != nil {
		return err
	}
	go enforceExpiredClients(service)
	go collectTrafficHistory(cfg, service)
	w := &web{cfg: cfg, service: service, sessions: []byte(secret), limits: map[string][]time.Time{}, idem: map[string]*idempotencyEntry{}}
	mux := http.NewServeMux()
	mux.Handle("/static/", w.securityHandler(http.FileServer(http.FS(staticFiles))))
	mux.HandleFunc("/", w.security(w.index))
	mux.HandleFunc("/api/login", w.security(w.loginAPI))
	mux.HandleFunc("/api/logout", w.security(w.requireAuth(w.logoutAPI)))
	mux.HandleFunc("/api/state", w.security(w.requireAuth(w.stateAPI)))
	mux.HandleFunc("/api/events", w.security(w.requireAuth(w.eventsAPI)))
	mux.HandleFunc("/api/backup", w.security(w.requireAuth(w.backupAPI)))
	mux.HandleFunc("/api/doctor", w.security(w.requireAuth(w.doctorAPI)))
	mux.HandleFunc("/api/audit-log", w.security(w.requireAuth(w.auditLogAPI)))
	mux.HandleFunc("/api/traffic-summary", w.security(w.requireAuth(w.trafficSummaryAPI)))
	mux.HandleFunc("/api/firewall/repair", w.security(w.requireAuth(w.firewallRepairAPI)))
	mux.HandleFunc("/api/support-bundle", w.security(w.requireAuth(w.supportBundleAPI)))
	mux.HandleFunc("/api/updates", w.security(w.requireAuth(w.updatesAPI)))
	mux.HandleFunc("/api/restore/verify", w.security(w.requireAuth(w.restoreVerifyAPI)))
	mux.HandleFunc("/api/warp", w.security(w.requireAuth(w.warpAPI)))
	mux.HandleFunc("/api/warp/", w.security(w.requireAuth(w.warpAPI)))
	mux.HandleFunc("/api/tunnels", w.security(w.requireAuth(w.tunnelsAPI)))
	mux.HandleFunc("/api/tunnels/", w.security(w.requireAuth(w.tunnelAPI)))
	mux.HandleFunc("/api/clients", w.security(w.requireAuth(w.clientsAPI)))
	mux.HandleFunc("/api/clients/", w.security(w.requireAuth(w.clientAPI)))
	mux.HandleFunc("/clients/config/", w.security(w.requireAuth(w.clientConfig)))

	addr := fmt.Sprintf("%s:%d", cfg.WebUIHost, cfg.WebUIPort)
	fmt.Printf("awg-forge web UI listening on http://%s\n", addr)
	// Built-in HTTP is intentional for localhost/LAN/reverse-proxy deployments; TLS termination is deployment-specific.
	return http.ListenAndServe(addr, mux) // nosemgrep: go.lang.security.audit.net.use-tls.use-tls
}

func enforceExpiredClients(service *app.Service) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		_ = service.EnforceExpiredClients()
	}
}

func collectTrafficHistory(cfg config.Config, service *app.Service) {
	if cfg.DatabaseMode != sqldb.ModeSQLite || !cfg.ApplyConfig {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		collectTrafficHistoryOnce(cfg, service)
		<-ticker.C
	}
}

func collectTrafficHistoryOnce(cfg config.Config, service *app.Service) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.DatabaseQueryTimeout)
	defer cancel()
	state, err := service.State()
	if err != nil {
		return
	}
	state, runtime := service.ClientRuntimeSnapshot(state)
	now := time.Now().UTC()
	var samples []sqldb.TrafficSample
	for _, tunnel := range state.Tunnels {
		for _, client := range tunnel.Clients {
			item, ok := runtime[tunnel.ID][client.ID]
			if !ok || !item.Present {
				continue
			}
			samples = append(samples, sqldb.TrafficSample{
				SampledAt:         now,
				TunnelID:          tunnel.ID,
				ClientID:          client.ID,
				RxBytes:           item.RxBytes,
				TxBytes:           item.TxBytes,
				LatestHandshakeAt: item.LastSeenAt,
				Present:           true,
			})
		}
	}
	if err := sqldb.RecordTrafficSamples(ctx, cfg, samples); err != nil && !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, sqldb.ErrDisabled) {
		service.Audit().Log(context.Background(), audit.Event{
			Level:   "warn",
			Event:   "traffic_history.record_failed",
			Message: "traffic history sample write failed",
			Error:   audit.Error(err),
		})
		return
	}
	enforceTrafficLimits(ctx, cfg, service)
}

func enforceTrafficLimits(ctx context.Context, cfg config.Config, service *app.Service) {
	exceeded, err := sqldb.ListExceededTrafficLimits(ctx, cfg, time.Now().UTC())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, sqldb.ErrDisabled) {
			return
		}
		service.Audit().Log(context.Background(), audit.Event{
			Level:   "warn",
			Event:   "traffic_limit.check_failed",
			Message: "traffic limit check failed",
			Error:   audit.Error(err),
		})
		return
	}
	for _, item := range exceeded {
		if err := service.DisableClientForTrafficLimit(item.ClientID, item.TotalBytes, item.LimitBytes); err != nil {
			service.Audit().Log(context.Background(), audit.Event{
				Level:   "warn",
				Event:   "traffic_limit.enforce_failed",
				Message: "traffic limit enforcement failed",
				Fields: map[string]any{
					"tunnel_id":           item.TunnelID,
					"client_id":           item.ClientID,
					"traffic_total_bytes": item.TotalBytes,
					"traffic_limit_bytes": item.LimitBytes,
				},
				Error: audit.Error(err),
			})
		}
	}
}

func (w *web) index(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(rw, r)
		return
	}
	b, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		http.Error(rw, "ui unavailable", http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	writeRawResponse(rw, b)
}

func (w *web) loginAPI(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := readJSON(rw, r, &req); err != nil {
		writeError(rw, http.StatusBadRequest, "invalid json")
		return
	}
	if w.cfg.Password == "" {
		w.setSession(rw, r)
		w.audit("info", "login.succeeded", "login succeeded without password", nil, nil)
		writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !w.allowLogin(ip) {
		w.audit("warn", "login.rate_limited", "login rate limited", nil, nil)
		writeError(rw, http.StatusTooManyRequests, "too many login attempts")
		return
	}
	if subtleCompare(req.Password, w.cfg.Password) {
		w.setSession(rw, r)
		w.audit("info", "login.succeeded", "login succeeded", nil, nil)
		writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
		return
	}
	w.audit("warn", "login.failed", "invalid password", nil, nil)
	writeError(rw, http.StatusUnauthorized, "invalid password")
}

func (w *web) logoutAPI(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	http.SetCookie(rw, sessionCookie(r, "", -1, w.sessionCookieSecure(r)))
	w.audit("info", "logout", "logout", nil, nil)
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

func (w *web) stateAPI(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	state, err := w.service.State()
	if err != nil {
		writeError(rw, http.StatusInternalServerError, "state unavailable")
		return
	}
	writeJSON(rw, http.StatusOK, w.publicState(r.Context(), state))
}

func (w *web) eventsAPI(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	flusher, ok := rw.(http.Flusher)
	if !ok {
		writeError(rw, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	noStore(rw)
	rw.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	rw.Header().Set("Connection", "keep-alive")
	rw.Header().Set("X-Accel-Buffering", "no")

	writeStateEvent := func() bool {
		state, err := w.service.State()
		if err != nil {
			writeServerSentEvent(rw, "error", []byte(`{"error":"state unavailable"}`))
			flusher.Flush()
			return false
		}
		body, err := json.Marshal(w.publicState(r.Context(), state))
		if err != nil {
			writeServerSentEvent(rw, "error", []byte(`{"error":"state unavailable"}`))
			flusher.Flush()
			return false
		}
		writeServerSentEvent(rw, "state", body)
		flusher.Flush()
		return true
	}

	if !writeStateEvent() {
		return
	}

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !writeStateEvent() {
				return
			}
		}
	}
}

func (w *web) doctorAPI(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	results := doctor.Check(w.cfg, w.service)
	w.audit("info", "doctor.completed", "doctor completed", doctorSummaryFields(results), nil)
	writeJSON(rw, http.StatusOK, map[string]any{"results": results})
}

func (w *web) auditLogAPI(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tail, _ := strconv.Atoi(r.URL.Query().Get("tail"))
	events, err := audit.ReadConfigured(r.Context(), w.cfg, audit.ReadOptions{
		Tail:  tail,
		Level: r.URL.Query().Get("level"),
		Event: r.URL.Query().Get("event"),
	})
	if err != nil {
		writeError(rw, http.StatusInternalServerError, "audit log unavailable")
		return
	}
	noStore(rw)
	writeJSON(rw, http.StatusOK, map[string]any{"events": events})
}

func (w *web) trafficSummaryAPI(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if w.cfg.DatabaseMode != sqldb.ModeSQLite {
		noStore(rw)
		writeJSON(rw, http.StatusOK, map[string]any{"enabled": false, "rows": []sqldb.TrafficSummaryRow{}})
		return
	}
	rows, err := sqldb.ListTrafficSummary(r.Context(), w.cfg, time.Now().UTC())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, sqldb.ErrDisabled) {
			noStore(rw)
			writeJSON(rw, http.StatusOK, map[string]any{"enabled": false, "rows": []sqldb.TrafficSummaryRow{}})
			return
		}
		writeError(rw, http.StatusInternalServerError, "traffic summary unavailable")
		return
	}
	noStore(rw)
	writeJSON(rw, http.StatusOK, map[string]any{"enabled": true, "rows": rows})
}

func (w *web) firewallRepairAPI(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	report, err := w.service.FirewallRepair()
	if err != nil {
		writeJSON(rw, http.StatusInternalServerError, map[string]any{"error": err.Error(), "firewall": report})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"firewall": report})
}

func (w *web) warpAPI(rw http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/warp")
	switch {
	case path == "" && r.Method == http.MethodGet:
		state, err := w.service.State()
		if err != nil {
			writeError(rw, http.StatusInternalServerError, "state unavailable")
			return
		}
		writeJSON(rw, http.StatusOK, map[string]any{
			"warp":   w.service.WarpSummary(state),
			"status": w.service.WarpRuntimeStatus(state),
		})
	case path == "/import" && r.Method == http.MethodPost && w.validOrigin(r):
		w.withIdempotency(rw, r, "warp-import", func() (int, any) {
			var req struct {
				Config string `json:"config"`
			}
			if err := readJSON(rw, r, &req); err != nil {
				return http.StatusBadRequest, errorPayload("invalid json")
			}
			_, err := w.service.ImportWarpConfig(req.Config)
			if err != nil {
				w.audit("warn", "warp.import.rejected", "WARP import request rejected", nil, err)
				return mutationErrorStatus(err, http.StatusBadRequest), errorPayload(err.Error())
			}
			state, _ := w.service.State()
			return http.StatusOK, map[string]any{"warp": w.service.WarpSummary(state)}
		})
	case path == "/register" && r.Method == http.MethodPost && w.validOrigin(r):
		w.withIdempotency(rw, r, "warp-register", func() (int, any) {
			if _, err := w.service.RegisterWarp(r.Context()); err != nil {
				w.audit("warn", "warp.register.rejected", "WARP registration request rejected", nil, err)
				return mutationErrorStatus(err, http.StatusBadRequest), errorPayload(err.Error())
			}
			state, _ := w.service.State()
			return http.StatusOK, map[string]any{"warp": w.service.WarpSummary(state)}
		})
	case path == "/restart" && r.Method == http.MethodPost && w.validOrigin(r):
		w.withIdempotency(rw, r, "warp-restart", func() (int, any) {
			if err := w.service.RestartWarp(); err != nil {
				w.audit("warn", "warp.restart.rejected", "WARP restart request rejected", nil, err)
				return mutationErrorStatus(err, http.StatusBadRequest), errorPayload(err.Error())
			}
			return http.StatusOK, map[string]any{"ok": true}
		})
	case path == "" && r.Method == http.MethodDelete && w.validOrigin(r):
		w.withIdempotency(rw, r, "warp-delete", func() (int, any) {
			if err := w.service.DeleteWarpConfig(r.Context()); err != nil {
				w.audit("warn", "warp.delete.rejected", "WARP delete request rejected", nil, err)
				return mutationErrorStatus(err, http.StatusBadRequest), errorPayload(err.Error())
			}
			return http.StatusOK, map[string]any{"ok": true}
		})
	default:
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (w *web) backupAPI(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := readJSON(rw, r, &req); err != nil {
		writeError(rw, http.StatusBadRequest, "invalid json")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	archive, err := backup.Create(ctx, w.cfg, w.service, req.Password, backup.Options{})
	if err != nil {
		writeError(rw, http.StatusBadRequest, err.Error())
		w.audit("error", "backup.create.failed", "encrypted backup creation failed", nil, err)
		return
	}
	w.audit("info", "backup.created", "encrypted backup created", map[string]any{"name": archive.Name}, nil)
	noStore(rw)
	rw.Header().Set("Content-Type", "application/octet-stream")
	rw.Header().Set("Content-Disposition", `attachment; filename="`+archive.Name+`"`)
	writeRawResponse(rw, archive.Data)
}

func (w *web) supportBundleAPI(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	bundle, err := support.Generate(ctx, w.cfg, w.service, support.Options{})
	if err != nil {
		w.audit("error", "support_bundle.failed", "support bundle creation failed", nil, err)
		writeError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	w.audit("info", "support_bundle.created", "support bundle created", map[string]any{"name": bundle.Name}, nil)
	noStore(rw)
	rw.Header().Set("Content-Type", "application/zip")
	rw.Header().Set("Content-Disposition", `attachment; filename="`+bundle.Name+`"`)
	writeRawResponse(rw, bundle.Data)
}

func (w *web) updatesAPI(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	report := updates.Check(ctx)
	w.audit("info", "updates.checked", "AmneziaWG update check completed", map[string]any{"components": len(report.Components)}, nil)
	writeJSON(rw, http.StatusOK, map[string]any{"updates": report})
}

func (w *web) restoreVerifyAPI(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	noStore(rw)
	r.Body = http.MaxBytesReader(rw, r.Body, 64<<20)
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeError(rw, http.StatusBadRequest, "invalid backup upload")
		return
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}
	password := r.FormValue("password")
	file, _, err := r.FormFile("backup")
	if err != nil {
		writeError(rw, http.StatusBadRequest, "backup file is required")
		return
	}
	defer func() { _ = file.Close() }()

	tmp, err := os.CreateTemp("", "awg-forge-restore-verify-*.afbackup")
	if err != nil {
		writeError(rw, http.StatusInternalServerError, "temporary file unavailable")
		return
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := io.Copy(tmp, file); err != nil {
		_ = tmp.Close()
		writeError(rw, http.StatusBadRequest, "backup upload failed")
		return
	}
	if err := tmp.Close(); err != nil {
		writeError(rw, http.StatusInternalServerError, "temporary file unavailable")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	report, err := backup.Verify(ctx, w.cfg, password, tmpPath)
	if err != nil {
		w.audit("error", "restore.verify.failed", "backup verification failed", nil, err)
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	w.audit("info", "restore.verified", "backup verified", map[string]any{"tunnels": len(report.Tunnels), "clients": report.ClientCount}, nil)
	writeJSON(rw, http.StatusOK, map[string]any{"report": report})
}

func (w *web) tunnelsAPI(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	w.withIdempotency(rw, r, "create-tunnel", func() (int, any) {
		var req struct {
			Profile    string `json:"profile"`
			Name       string `json:"name"`
			EgressMode string `json:"egress_mode"`
			Port       int    `json:"port"`
			Subnet     string `json:"subnet"`
		}
		if err := readJSON(rw, r, &req); err != nil {
			w.audit("warn", "tunnel.create.rejected", "tunnel creation request rejected", map[string]any{"reason": "invalid json"}, err)
			return http.StatusBadRequest, errorPayload("invalid json")
		}
		tunnel, err := w.service.CreateTunnelWithOptions(r.Context(), app.TunnelCreateOptions{
			ProfileID:  req.Profile,
			Name:       req.Name,
			EgressMode: req.EgressMode,
			Subnet:     req.Subnet,
			Port:       req.Port,
		})
		if err != nil {
			w.audit("warn", "tunnel.create.rejected", "tunnel creation request rejected", map[string]any{"profile": req.Profile, "name": req.Name, "egress": req.EgressMode, "port": req.Port, "subnet": req.Subnet}, err)
			return mutationErrorStatus(err, http.StatusBadRequest), errorPayload(err.Error())
		}
		return http.StatusCreated, map[string]any{"tunnel": publicTunnel(tunnel, app.TunnelStatus{})}
	})
}

func (w *web) tunnelAPI(rw http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/tunnels/"), "/")
	if len(parts) < 2 || parts[0] == "" {
		writeError(rw, http.StatusNotFound, "not found")
		return
	}
	id, action := parts[0], parts[1]
	switch action {
	case "settings":
		w.updateTunnelSettingsAPI(rw, r, id)
	case "delete":
		w.deleteTunnelAPI(rw, r, id)
	case "restart":
		w.restartTunnelAPI(rw, r, id)
	case "health":
		w.tunnelHealthAPI(rw, r, id)
	case "protocol":
		w.updateProtocolAPI(rw, r, id)
	case "regenerate":
		w.regenerateProtocolAPI(rw, r, id)
	default:
		writeError(rw, http.StatusNotFound, "not found")
	}
}

func (w *web) tunnelHealthAPI(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	health, err := w.service.TunnelHealthByID(id, 2)
	if err != nil {
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"health": health})
}

func (w *web) updateTunnelSettingsAPI(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPatch || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	w.withIdempotency(rw, r, "update-tunnel-settings:"+id, func() (int, any) {
		var req struct {
			Name       string `json:"name"`
			ServerHost string `json:"server_host"`
			EgressMode string `json:"egress_mode"`
			Port       int    `json:"port"`
			Subnet     string `json:"subnet"`
			DNS        string `json:"dns"`
			AllowedIPs string `json:"allowed_ips"`
			Keepalive  int    `json:"keepalive"`
			MTU        int    `json:"mtu"`
			Enabled    bool   `json:"enabled"`
		}
		if err := readJSON(rw, r, &req); err != nil {
			w.audit("warn", "tunnel.settings.rejected", "tunnel settings request rejected", map[string]any{"tunnel_id": id, "reason": "invalid json"}, err)
			return http.StatusBadRequest, errorPayload("invalid json")
		}
		tunnel, err := w.service.UpdateTunnelSettingsContext(r.Context(), id, app.TunnelSettingsUpdate{
			Name:       req.Name,
			ServerHost: req.ServerHost,
			EgressMode: req.EgressMode,
			Subnet:     req.Subnet,
			DNS:        req.DNS,
			AllowedIPs: req.AllowedIPs,
			Keepalive:  req.Keepalive,
			MTU:        req.MTU,
			Port:       req.Port,
			Enabled:    req.Enabled,
		})
		if err != nil {
			w.audit("warn", "tunnel.settings.rejected", "tunnel settings request rejected", map[string]any{"tunnel_id": id, "name": req.Name, "port": req.Port, "subnet": req.Subnet, "enabled": req.Enabled}, err)
			return mutationErrorStatus(err, http.StatusBadRequest), errorPayload(err.Error())
		}
		return http.StatusOK, map[string]any{"tunnel": publicTunnel(tunnel, app.TunnelStatus{})}
	})
}

func (w *web) deleteTunnelAPI(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodDelete || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	w.withIdempotency(rw, r, "delete-tunnel:"+id, func() (int, any) {
		if err := w.service.DeleteTunnel(id); err != nil {
			w.audit("warn", "tunnel.delete.rejected", "tunnel delete request rejected", map[string]any{"tunnel_id": id}, err)
			return mutationErrorStatus(err, http.StatusBadRequest), errorPayload(err.Error())
		}
		return http.StatusOK, map[string]any{"ok": true}
	})
}

func (w *web) restartTunnelAPI(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	w.withIdempotency(rw, r, "restart-tunnel:"+id, func() (int, any) {
		if err := w.service.RestartTunnelByID(id); err != nil {
			w.audit("warn", "tunnel.restart.rejected", "tunnel restart request rejected", map[string]any{"tunnel_id": id}, err)
			return mutationErrorStatus(err, http.StatusBadRequest), errorPayload(err.Error())
		}
		return http.StatusOK, map[string]any{"ok": true}
	})
}

func (w *web) updateProtocolAPI(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPatch || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	w.withIdempotency(rw, r, "update-protocol:"+id, func() (int, any) {
		var req struct {
			Profile string                `json:"profile"`
			Params  config.ProtocolParams `json:"params"`
		}
		if err := readJSON(rw, r, &req); err != nil {
			w.audit("warn", "tunnel.protocol.rejected", "tunnel protocol request rejected", map[string]any{"tunnel_id": id, "reason": "invalid json"}, err)
			return http.StatusBadRequest, errorPayload("invalid json")
		}
		if err := w.service.UpdateTunnelProtocol(id, req.Profile, req.Params); err != nil {
			w.audit("warn", "tunnel.protocol.rejected", "tunnel protocol request rejected", map[string]any{"tunnel_id": id, "profile": req.Profile}, err)
			return mutationErrorStatus(err, http.StatusBadRequest), errorPayload(err.Error())
		}
		return http.StatusOK, map[string]any{"ok": true}
	})
}

func (w *web) regenerateProtocolAPI(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	w.withIdempotency(rw, r, "regenerate-protocol:"+id, func() (int, any) {
		var req struct {
			Profile string `json:"profile"`
		}
		_ = readJSON(rw, r, &req)
		if err := w.service.RegenerateTunnelProtocol(id, req.Profile); err != nil {
			w.audit("warn", "tunnel.protocol_regenerate.rejected", "tunnel protocol regenerate request rejected", map[string]any{"tunnel_id": id, "profile": req.Profile}, err)
			return mutationErrorStatus(err, http.StatusBadRequest), errorPayload(err.Error())
		}
		return http.StatusOK, map[string]any{"ok": true}
	})
}

func (w *web) clientsAPI(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	w.withIdempotency(rw, r, "create-client", func() (int, any) {
		var req struct {
			TunnelID  string `json:"tunnel_id"`
			Name      string `json:"name"`
			ExpiresAt string `json:"expires_at"`
		}
		if err := readJSON(rw, r, &req); err != nil {
			w.audit("warn", "client.create.rejected", "client creation request rejected", map[string]any{"reason": "invalid json"}, err)
			return http.StatusBadRequest, errorPayload("invalid json")
		}
		expiresAt, err := parseOptionalAPITime(req.ExpiresAt)
		if err != nil {
			w.audit("warn", "client.create.rejected", "client creation request rejected", map[string]any{"tunnel_id": req.TunnelID, "client_name": req.Name, "reason": "invalid expires_at"}, err)
			return http.StatusBadRequest, errorPayload("invalid expires_at")
		}
		client, err := w.service.AddClientToTunnelWithOptions(req.TunnelID, req.Name, app.ClientCreateOptions{ExpiresAt: expiresAt})
		if err != nil {
			w.audit("warn", "client.create.rejected", "client creation request rejected", map[string]any{"tunnel_id": req.TunnelID, "client_name": req.Name}, err)
			return mutationErrorStatus(err, http.StatusBadRequest), errorPayload(err.Error())
		}
		return http.StatusCreated, map[string]any{"client": publicClient(client)}
	})
}

func (w *web) clientAPI(rw http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/clients/"), "/")
	if len(parts) < 2 || parts[0] == "" {
		writeError(rw, http.StatusNotFound, "not found")
		return
	}
	id, action := parts[0], parts[1]
	switch action {
	case "settings":
		w.updateClientSettingsAPI(rw, r, id)
	case "traffic-limit":
		w.updateClientTrafficLimitAPI(rw, r, id)
	case "enable":
		w.setClientEnabledAPI(rw, r, id, true)
	case "disable":
		w.setClientEnabledAPI(rw, r, id, false)
	case "delete":
		w.deleteClientAPI(rw, r, id)
	case "import-key":
		w.clientImportKeyAPI(rw, r, id)
	case "amnezia-vpn-qr-series":
		w.clientAmneziaVPNQRSeriesAPI(rw, r, id)
	case "amnezia-vpn-qr":
		w.clientAmneziaVPNQRAPI(rw, r, id)
	case "qr":
		w.clientQRAPI(rw, r, id)
	default:
		writeError(rw, http.StatusNotFound, "not found")
	}
}

func (w *web) updateClientSettingsAPI(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPatch || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	w.withIdempotency(rw, r, "update-client-settings:"+id, func() (int, any) {
		var req struct {
			Name      string `json:"name"`
			Notes     string `json:"notes"`
			ExpiresAt string `json:"expires_at"`
		}
		if err := readJSON(rw, r, &req); err != nil {
			w.audit("warn", "client.settings.rejected", "client settings request rejected", map[string]any{"client_id": id, "reason": "invalid json"}, err)
			return http.StatusBadRequest, errorPayload("invalid json")
		}
		expiresAt, err := parseOptionalAPITime(req.ExpiresAt)
		if err != nil {
			w.audit("warn", "client.settings.rejected", "client settings request rejected", map[string]any{"client_id": id, "client_name": req.Name, "reason": "invalid expires_at"}, err)
			return http.StatusBadRequest, errorPayload("invalid expires_at")
		}
		client, err := w.service.UpdateClientSettingsWithOptions(id, app.ClientSettingsUpdate{Name: req.Name, Notes: req.Notes, ExpiresAt: expiresAt})
		if err != nil {
			w.audit("warn", "client.settings.rejected", "client settings request rejected", map[string]any{"client_id": id, "client_name": req.Name}, err)
			return mutationErrorStatus(err, http.StatusBadRequest), errorPayload(err.Error())
		}
		return http.StatusOK, map[string]any{"client": publicClient(client)}
	})
}

func (w *web) updateClientTrafficLimitAPI(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPatch || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	w.withIdempotency(rw, r, "update-client-traffic-limit:"+id, func() (int, any) {
		var req struct {
			LimitBytes json.RawMessage `json:"limit_bytes"`
		}
		if err := readJSON(rw, r, &req); err != nil {
			w.audit("warn", "client.traffic_limit.rejected", "client traffic limit request rejected", map[string]any{"client_id": id, "reason": "invalid json"}, err)
			return http.StatusBadRequest, errorPayload("invalid json")
		}
		limitBytesValue, hasLimit, err := parseTrafficLimitBytes(req.LimitBytes)
		if err != nil {
			w.audit("warn", "client.traffic_limit.rejected", "client traffic limit request rejected", map[string]any{"client_id": id, "reason": "invalid limit"}, err)
			return http.StatusBadRequest, errorPayload(err.Error())
		}
		var limitBytes *uint64
		if hasLimit {
			limitBytes = &limitBytesValue
		}
		tunnel, client, ok := w.findClientForAPI(id)
		if !ok {
			return http.StatusNotFound, errorPayload("client not found")
		}
		ctx, cancel := context.WithTimeout(r.Context(), w.cfg.DatabaseQueryTimeout)
		defer cancel()
		if err := sqldb.SetClientTrafficLimit(ctx, w.cfg, tunnel.ID, client.ID, limitBytes); err != nil {
			w.audit("warn", "client.traffic_limit.rejected", "client traffic limit request rejected", map[string]any{"client_id": id}, err)
			return mutationErrorStatus(err, http.StatusBadRequest), errorPayload(err.Error())
		}
		enforceTrafficLimits(ctx, w.cfg, w.service)
		w.audit("info", "client.traffic_limit.updated", "client traffic limit updated", map[string]any{"client_id": id, "limit_set": limitBytes != nil}, nil)
		return http.StatusOK, map[string]any{"ok": true}
	})
}

func (w *web) setClientEnabledAPI(rw http.ResponseWriter, r *http.Request, id string, enabled bool) {
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	action := "disable-client:"
	if enabled {
		action = "enable-client:"
	}
	w.withIdempotency(rw, r, action+id, func() (int, any) {
		if enabled {
			ctx, cancel := context.WithTimeout(r.Context(), w.cfg.DatabaseQueryTimeout)
			defer cancel()
			exceeded, found, err := trafficLimitExceededForClient(ctx, w.cfg, id)
			if err != nil {
				w.audit("warn", "client.enabled_state.rejected", "client enabled state request rejected", map[string]any{"client_id": id, "enabled": enabled, "reason": "traffic limit check failed"}, err)
				return mutationErrorStatus(err, http.StatusBadRequest), errorPayload(err.Error())
			}
			if found {
				w.audit("warn", "client.enabled_state.rejected", "client enabled state request rejected", map[string]any{
					"client_id":           id,
					"enabled":             enabled,
					"reason":              "traffic limit exceeded",
					"traffic_total_bytes": exceeded.TotalBytes,
					"traffic_limit_bytes": exceeded.LimitBytes,
				}, nil)
				return http.StatusConflict, errorPayload("traffic limit exceeded; increase or clear the limit before enabling")
			}
		}
		if err := w.service.SetClientEnabled(id, enabled); err != nil {
			w.audit("warn", "client.enabled_state.rejected", "client enabled state request rejected", map[string]any{"client_id": id, "enabled": enabled}, err)
			return mutationErrorStatus(err, http.StatusNotFound), errorPayload(err.Error())
		}
		if enabled {
			ctx, cancel := context.WithTimeout(r.Context(), w.cfg.DatabaseQueryTimeout)
			defer cancel()
			enforceTrafficLimits(ctx, w.cfg, w.service)
		}
		return http.StatusOK, map[string]any{"ok": true}
	})
}

func trafficLimitExceededForClient(ctx context.Context, cfg config.Config, clientID string) (sqldb.ExceededTrafficLimit, bool, error) {
	exceeded, err := sqldb.ListExceededTrafficLimits(ctx, cfg, time.Now().UTC())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, sqldb.ErrDisabled) {
			return sqldb.ExceededTrafficLimit{}, false, nil
		}
		return sqldb.ExceededTrafficLimit{}, false, err
	}
	for i := range exceeded {
		if exceeded[i].ClientID == clientID {
			return exceeded[i], true, nil
		}
	}
	return sqldb.ExceededTrafficLimit{}, false, nil
}

func (w *web) deleteClientAPI(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodDelete || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	w.withIdempotency(rw, r, "delete-client:"+id, func() (int, any) {
		if err := w.service.RemoveClient(id); err != nil {
			w.audit("warn", "client.delete.rejected", "client delete request rejected", map[string]any{"client_id": id}, err)
			return mutationErrorStatus(err, http.StatusNotFound), errorPayload(err.Error())
		}
		ctx, cancel := context.WithTimeout(r.Context(), w.cfg.DatabaseQueryTimeout)
		defer cancel()
		if err := sqldb.DeleteClientTrafficLimit(ctx, w.cfg, id); err != nil && !errors.Is(err, sqldb.ErrDisabled) && !errors.Is(err, sql.ErrNoRows) {
			w.audit("warn", "client.traffic_limit_cleanup.failed", "client traffic limit cleanup failed", map[string]any{"client_id": id}, err)
		}
		return http.StatusOK, map[string]any{"ok": true}
	})
}

func (w *web) clientQRAPI(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	conf, client, err := w.service.ClientConfigForDownload(id)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	code, err := qr.Encode(conf, qr.L, qr.Auto)
	if err != nil {
		w.audit("warn", "client.qr.rejected", "client QR generation failed", map[string]any{"client_id": id}, err)
		writeError(rw, http.StatusBadRequest, "client config is too large for QR")
		return
	}
	w.audit("info", "client.qr.viewed", "client config QR viewed", map[string]any{"client_id": id}, nil)
	if err := writeQRCodePNG(rw, code, configFilename(client)+".png"); err != nil {
		w.audit("warn", "client.qr.write_failed", "client QR response write failed", map[string]any{"client_id": id}, err)
	}
}

func (w *web) clientAmneziaVPNQRAPI(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, err := w.service.ClientExportContext(id)
	if err != nil {
		writeError(rw, http.StatusNotFound, "not found")
		return
	}
	if raw := r.URL.Query().Get("chunk"); raw != "" {
		chunkIndex, err := strconv.Atoi(raw)
		if err != nil || chunkIndex != 0 {
			writeError(rw, http.StatusBadRequest, "invalid QR chunk")
			return
		}
	}
	payload, err := buildAmneziaVPNQRPayload(ctx)
	if err != nil {
		w.audit("warn", "client.amneziavpn_qr.rejected", "client AmneziaVPN QR generation failed", map[string]any{"client_id": id}, err)
		writeError(rw, http.StatusBadRequest, "client AmneziaVPN QR payload could not be built")
		return
	}
	code, err := qr.Encode(payload, qr.L, qr.Auto)
	if err != nil {
		w.audit("warn", "client.amneziavpn_qr.rejected", "client AmneziaVPN QR generation failed", map[string]any{"client_id": id}, err)
		writeError(rw, http.StatusBadRequest, "client AmneziaVPN QR is too large")
		return
	}
	w.audit("info", "client.amneziavpn_qr.viewed", "client AmneziaVPN import QR viewed", map[string]any{"client_id": id}, nil)
	rw.Header().Set("X-QR-Chunk", "1")
	rw.Header().Set("X-QR-Chunks", "1")
	if err := writeQRCodePNG(rw, code, fmt.Sprintf("%s-amneziavpn.png", configFilename(ctx.Client))); err != nil {
		w.audit("warn", "client.amneziavpn_qr.write_failed", "client AmneziaVPN QR response write failed", map[string]any{"client_id": id}, err)
	}
}

func (w *web) clientAmneziaVPNQRSeriesAPI(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := w.service.ClientExportContext(id); err != nil {
		writeError(rw, http.StatusNotFound, "not found")
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"chunks": 1})
}

func writeQRCodePNG(rw http.ResponseWriter, code image.Image, filename string) error {
	noStore(rw)
	rw.Header().Set("Content-Type", "image/png")
	rw.Header().Set("Content-Disposition", `inline; filename="`+filename+`"`)
	return png.Encode(rw, renderQRCodeImage(code))
}

func renderQRCodeImage(code image.Image) image.Image {
	bounds := code.Bounds()
	modulesX := bounds.Dx()
	modulesY := bounds.Dy()
	largest := modulesX
	if modulesY > largest {
		largest = modulesY
	}
	modulePixels := clientQRTargetSize / (largest + clientQRQuietZoneModules*2)
	if modulePixels < clientQRMinModulePixels {
		modulePixels = clientQRMinModulePixels
	}

	width := (modulesX + clientQRQuietZoneModules*2) * modulePixels
	height := (modulesY + clientQRQuietZoneModules*2) * modulePixels
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	fillImage(dst, color.White)

	for y := 0; y < modulesY; y++ {
		for x := 0; x < modulesX; x++ {
			if !isDark(code.At(bounds.Min.X+x, bounds.Min.Y+y)) {
				continue
			}
			startX := (x + clientQRQuietZoneModules) * modulePixels
			startY := (y + clientQRQuietZoneModules) * modulePixels
			for yy := 0; yy < modulePixels; yy++ {
				for xx := 0; xx < modulePixels; xx++ {
					dst.Set(startX+xx, startY+yy, color.Black)
				}
			}
		}
	}
	return dst
}

func fillImage(img *image.RGBA, c color.Color) {
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			img.Set(x, y, c)
		}
	}
}

func isDark(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r+g+b < 0xffff*3/2
}

func (w *web) clientImportKeyAPI(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	key, client, err := w.service.ClientImportKey(id)
	if err != nil {
		writeError(rw, http.StatusNotFound, "not found")
		return
	}
	noStore(rw)
	writeJSON(rw, http.StatusOK, map[string]any{
		"client":     publicClient(client),
		"import_key": key,
		"format":     "vpn-conf-base64url",
		"warning":    "Experimental AmneziaVPN/DefaultVPN import key. Use .conf for production clients.",
	})
}

func (w *web) clientConfig(rw http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/clients/config/")
	conf, client, err := w.service.ClientConfigForDownload(id)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	noStore(rw)
	rw.Header().Set("Content-Type", "application/octet-stream")
	rw.Header().Set("Content-Disposition", `attachment; filename="`+configFilename(client)+`.conf"`)
	writeRawResponse(rw, []byte(conf))
}

func writeServerSentEvent(rw http.ResponseWriter, event string, body []byte) {
	// SSE frames carry JSON bytes only; callers marshal structured state or pass static JSON.
	_, _ = fmt.Fprintf(rw, "event: %s\ndata: %s\n\n", event, body) // nosemgrep: go.lang.security.audit.xss.no-fprintf-to-responsewriter.no-fprintf-to-responsewriter
}

func (w *web) publicState(ctx context.Context, state config.State) map[string]any {
	var tunnels []map[string]any
	firewallReport, firewallErr := w.service.FirewallCheck()
	state, runtime := w.service.ClientRuntimeSnapshot(state)
	traffic := w.clientTrafficSummary(ctx, state)
	for _, tunnel := range state.Tunnels {
		status, _ := w.service.TunnelStatusByID(tunnel.ID)
		tunnels = append(tunnels, publicTunnelWithFirewall(tunnel, status, firewallSummaryForTunnel(tunnel, firewallReport, firewallErr), runtime[tunnel.ID], traffic[tunnel.ID]))
	}
	return map[string]any{
		"authenticated":       true,
		"apply_enabled":       w.cfg.ApplyConfig,
		"server_host":         state.ServerHost,
		"warp":                w.service.WarpSummary(state),
		"database":            publicDatabase(w.cfg),
		"build":               buildinfo.Current(),
		"published_udp_ports": w.cfg.PublishedUDPPorts,
		"profiles": []map[string]any{
			profileMeta("awg_legacy_1_0", "1.0", "Legacy", true, state),
			profileMeta("awg_1_5", "1.5", "Modern", true, state),
			profileMeta("awg_2_0", "2.0", "Modern", true, state),
		},
		"tunnels": tunnels,
	}
}

func (w *web) clientTrafficSummary(ctx context.Context, state config.State) map[string]map[string]clientTrafficSummary {
	out := map[string]map[string]clientTrafficSummary{}
	for _, tunnel := range state.Tunnels {
		out[tunnel.ID] = map[string]clientTrafficSummary{}
	}
	if w.cfg.DatabaseMode != sqldb.ModeSQLite {
		return out
	}
	rows, err := sqldb.ListTrafficSummary(ctx, w.cfg, time.Now().UTC())
	if err != nil {
		return out
	}
	for _, row := range rows {
		if _, ok := out[row.TunnelID]; !ok {
			out[row.TunnelID] = map[string]clientTrafficSummary{}
		}
		out[row.TunnelID][row.ClientID] = clientTrafficSummary{
			Enabled:    true,
			RxTotal:    row.RxTotal,
			TxTotal:    row.TxTotal,
			LimitBytes: row.LimitBytes,
			Exceeded:   trafficExceeded(row.RxTotal, row.TxTotal, row.LimitBytes),
		}
	}
	limits, err := sqldb.ListClientTrafficLimits(ctx, w.cfg)
	if err != nil {
		w.audit("warn", "traffic_history.limits_unavailable", "traffic limit summary unavailable", nil, err)
		return out
	}
	for _, limit := range limits {
		if _, ok := out[limit.TunnelID]; !ok {
			out[limit.TunnelID] = map[string]clientTrafficSummary{}
		}
		summary := out[limit.TunnelID][limit.ClientID]
		summary.Enabled = true
		summary.LimitBytes = uint64Ptr(limit.LimitBytes)
		summary.Exceeded = trafficExceeded(summary.RxTotal, summary.TxTotal, summary.LimitBytes)
		out[limit.TunnelID][limit.ClientID] = summary
	}
	return out
}

func trafficExceeded(rxTotal, txTotal uint64, limitBytes *uint64) bool {
	if limitBytes == nil {
		return false
	}
	if rxTotal >= *limitBytes {
		return true
	}
	return txTotal >= *limitBytes-rxTotal
}

func (w *web) findClientForAPI(id string) (config.Tunnel, config.Client, bool) {
	state, err := w.service.State()
	if err != nil {
		return config.Tunnel{}, config.Client{}, false
	}
	for _, tunnel := range state.Tunnels {
		for _, client := range tunnel.Clients {
			if client.ID == id {
				return tunnel, client, true
			}
		}
	}
	return config.Tunnel{}, config.Client{}, false
}

func parseTrafficLimitBytes(raw json.RawMessage) (uint64, bool, error) {
	if len(raw) == 0 {
		return 0, false, errors.New("limit_bytes is required")
	}
	value := strings.TrimSpace(string(raw))
	if value == "null" {
		return 0, false, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, false, errors.New("limit_bytes must be a positive integer or null")
	}
	if parsed == 0 {
		return 0, false, errors.New("limit_bytes must be positive")
	}
	return parsed, true, nil
}

func uint64Ptr(value uint64) *uint64 {
	return &value
}

func publicDatabase(cfg config.Config) map[string]any {
	mode := cfg.DatabaseMode
	if mode == "" {
		mode = sqldb.ModeOff
	}
	return map[string]any{
		"mode":    mode,
		"enabled": mode != sqldb.ModeOff,
	}
}

func (w *web) audit(level, event, message string, fields map[string]any, err error) {
	if w == nil || w.service == nil {
		return
	}
	w.service.Audit().Log(context.Background(), audit.Event{
		Level:   level,
		Event:   event,
		Message: message,
		Fields:  fields,
		Error:   audit.Error(err),
	})
}

func doctorSummaryFields(results []doctor.Result) map[string]any {
	fields := map[string]any{"results": len(results)}
	okCount := 0
	warnCount := 0
	failCount := 0
	for _, result := range results {
		switch result.Level {
		case "ok":
			okCount++
		case "warn":
			warnCount++
		case "fail":
			failCount++
		}
	}
	fields["ok"] = okCount
	fields["warn"] = warnCount
	fields["fail"] = failCount
	return fields
}
