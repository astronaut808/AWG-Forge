package server

import (
	"context"
	"embed"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
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
		writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !w.allowLogin(ip) {
		writeError(rw, http.StatusTooManyRequests, "too many login attempts")
		return
	}
	if subtleCompare(req.Password, w.cfg.Password) {
		w.setSession(rw, r)
		writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
		return
	}
	writeError(rw, http.StatusUnauthorized, "invalid password")
}

func (w *web) logoutAPI(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	http.SetCookie(rw, sessionCookie(r, "", -1))
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
	writeJSON(rw, http.StatusOK, map[string]any{"results": doctor.Check(w.cfg, w.service)})
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
		return
	}
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
		writeError(rw, http.StatusInternalServerError, err.Error())
		return
	}
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
	writeJSON(rw, http.StatusOK, map[string]any{"updates": updates.Check(ctx)})
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
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
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
			return http.StatusBadRequest, errorPayload("invalid json")
		}
		tunnel, err := w.service.CreateTunnel(req.Profile, req.Name, req.Subnet, req.Port)
		if err != nil {
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
			return http.StatusBadRequest, errorPayload("invalid json")
		}
		if err := w.service.UpdateTunnelProtocol(id, req.Profile, req.Params); err != nil {
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
			return http.StatusBadRequest, errorPayload("invalid json")
		}
		client, err := w.service.AddClientToTunnel(req.TunnelID, req.Name)
		if err != nil {
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
			return http.StatusBadRequest, errorPayload("invalid json")
		}
		client, err := w.service.UpdateClientSettings(id, req.Name, req.Notes)
		if err != nil {
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
	for _, tunnel := range state.Tunnels {
		status, _ := w.service.TunnelStatusByID(tunnel.ID)
		tunnels = append(tunnels, publicTunnelWithFirewall(tunnel, status, firewallSummaryForTunnel(tunnel, firewallReport, firewallErr)))
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
