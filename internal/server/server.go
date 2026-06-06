package server

import (
	"context"
	"embed"
	"fmt"
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
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/doctor"
	"github.com/astronaut808/awg-forge/internal/support"
	"github.com/astronaut808/awg-forge/internal/updates"
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
	w := &web{cfg: cfg, service: service, sessions: []byte(secret), limits: map[string][]time.Time{}, idem: map[string]*idempotencyEntry{}}
	mux := http.NewServeMux()
	mux.Handle("/static/", w.securityHandler(http.FileServer(http.FS(staticFiles))))
	mux.HandleFunc("/", w.security(w.index))
	mux.HandleFunc("/api/login", w.security(w.loginAPI))
	mux.HandleFunc("/api/logout", w.security(w.requireAuth(w.logoutAPI)))
	mux.HandleFunc("/api/state", w.security(w.requireAuth(w.stateAPI)))
	mux.HandleFunc("/api/backup", w.security(w.requireAuth(w.backupAPI)))
	mux.HandleFunc("/api/doctor", w.security(w.requireAuth(w.doctorAPI)))
	mux.HandleFunc("/api/audit-log", w.security(w.requireAuth(w.auditLogAPI)))
	mux.HandleFunc("/api/firewall/repair", w.security(w.requireAuth(w.firewallRepairAPI)))
	mux.HandleFunc("/api/support-bundle", w.security(w.requireAuth(w.supportBundleAPI)))
	mux.HandleFunc("/api/updates", w.security(w.requireAuth(w.updatesAPI)))
	mux.HandleFunc("/api/restore/verify", w.security(w.requireAuth(w.restoreVerifyAPI)))
	mux.HandleFunc("/api/tunnels", w.security(w.requireAuth(w.tunnelsAPI)))
	mux.HandleFunc("/api/tunnels/", w.security(w.requireAuth(w.tunnelAPI)))
	mux.HandleFunc("/api/clients", w.security(w.requireAuth(w.clientsAPI)))
	mux.HandleFunc("/api/clients/", w.security(w.requireAuth(w.clientAPI)))
	mux.HandleFunc("/clients/config/", w.security(w.requireAuth(w.clientConfig)))

	addr := fmt.Sprintf("%s:%d", cfg.WebUIHost, cfg.WebUIPort)
	fmt.Printf("awg-forge web UI listening on http://%s\n", addr)
	return http.ListenAndServe(addr, mux)
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
	_, _ = rw.Write(b)
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
	writeJSON(rw, http.StatusOK, w.publicState(state))
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
	events, err := audit.ReadFile(w.cfg.AuditLogPath, audit.ReadOptions{
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
	_, _ = rw.Write(archive.Data)
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
	_, _ = rw.Write(bundle.Data)
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
			Profile string `json:"profile"`
			Name    string `json:"name"`
			Port    int    `json:"port"`
			Subnet  string `json:"subnet"`
		}
		if err := readJSON(rw, r, &req); err != nil {
			w.audit("warn", "tunnel.create.rejected", "tunnel creation request rejected", map[string]any{"reason": "invalid json"}, err)
			return http.StatusBadRequest, errorPayload("invalid json")
		}
		tunnel, err := w.service.CreateTunnel(req.Profile, req.Name, req.Subnet, req.Port)
		if err != nil {
			w.audit("warn", "tunnel.create.rejected", "tunnel creation request rejected", map[string]any{"profile": req.Profile, "name": req.Name, "port": req.Port, "subnet": req.Subnet}, err)
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
		tunnel, err := w.service.UpdateTunnelSettings(id, app.TunnelSettingsUpdate{
			Name:       req.Name,
			ServerHost: req.ServerHost,
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
			TunnelID string `json:"tunnel_id"`
			Name     string `json:"name"`
		}
		if err := readJSON(rw, r, &req); err != nil {
			w.audit("warn", "client.create.rejected", "client creation request rejected", map[string]any{"reason": "invalid json"}, err)
			return http.StatusBadRequest, errorPayload("invalid json")
		}
		client, err := w.service.AddClientToTunnel(req.TunnelID, req.Name)
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
	case "enable":
		w.setClientEnabledAPI(rw, r, id, true)
	case "disable":
		w.setClientEnabledAPI(rw, r, id, false)
	case "delete":
		w.deleteClientAPI(rw, r, id)
	case "import-key":
		w.clientImportKeyAPI(rw, r, id)
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
			Name  string `json:"name"`
			Notes string `json:"notes"`
		}
		if err := readJSON(rw, r, &req); err != nil {
			w.audit("warn", "client.settings.rejected", "client settings request rejected", map[string]any{"client_id": id, "reason": "invalid json"}, err)
			return http.StatusBadRequest, errorPayload("invalid json")
		}
		client, err := w.service.UpdateClientSettings(id, req.Name, req.Notes)
		if err != nil {
			w.audit("warn", "client.settings.rejected", "client settings request rejected", map[string]any{"client_id": id, "client_name": req.Name}, err)
			return mutationErrorStatus(err, http.StatusBadRequest), errorPayload(err.Error())
		}
		return http.StatusOK, map[string]any{"client": publicClient(client)}
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
		if err := w.service.SetClientEnabled(id, enabled); err != nil {
			w.audit("warn", "client.enabled_state.rejected", "client enabled state request rejected", map[string]any{"client_id": id, "enabled": enabled}, err)
			return mutationErrorStatus(err, http.StatusNotFound), errorPayload(err.Error())
		}
		return http.StatusOK, map[string]any{"ok": true}
	})
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
		return http.StatusOK, map[string]any{"ok": true}
	})
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
	_, _ = rw.Write([]byte(conf))
}

func (w *web) publicState(state config.State) map[string]any {
	var tunnels []map[string]any
	firewallReport, firewallErr := w.service.FirewallCheck()
	runtime := w.service.ClientRuntimeSnapshot(state)
	for _, tunnel := range state.Tunnels {
		status, _ := w.service.TunnelStatusByID(tunnel.ID)
		tunnels = append(tunnels, publicTunnelWithFirewall(tunnel, status, firewallSummaryForTunnel(tunnel, firewallReport, firewallErr), runtime[tunnel.ID]))
	}
	return map[string]any{
		"authenticated":       true,
		"apply_enabled":       w.cfg.ApplyConfig,
		"server_host":         state.ServerHost,
		"published_udp_ports": w.cfg.PublishedUDPPorts,
		"profiles": []map[string]any{
			profileMeta("awg_legacy_1_0", "1.0", "Legacy", true, state),
			profileMeta("awg_1_5", "1.5", "Modern", true, state),
			profileMeta("awg_2_0", "2.0", "Modern", true, state),
		},
		"tunnels": tunnels,
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
