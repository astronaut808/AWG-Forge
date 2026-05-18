package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/doctor"
	"github.com/astronaut808/awg-forge/internal/render"
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

const sessionTTL = 30 * time.Minute
const idempotencyTTL = 10 * time.Minute

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
	mux.HandleFunc("/api/doctor", w.security(w.requireAuth(w.doctorAPI)))
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
	if err := readJSON(r, &req); err != nil {
		writeError(rw, http.StatusBadRequest, "invalid json")
		return
	}
	if w.cfg.Password == "" {
		w.setSession(rw)
		writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !w.allowLogin(ip) {
		writeError(rw, http.StatusTooManyRequests, "too many login attempts")
		return
	}
	if subtleCompare(req.Password, w.cfg.Password) {
		w.setSession(rw)
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
	http.SetCookie(rw, &http.Cookie{Name: "awg_forge_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
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
		if err := readJSON(r, &req); err != nil {
			return http.StatusBadRequest, errorPayload("invalid json")
		}
		tunnel, err := w.service.CreateTunnel(req.Profile, req.Name, req.Subnet, req.Port)
		if err != nil {
			return http.StatusBadRequest, errorPayload(err.Error())
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
	case "protocol":
		w.updateProtocolAPI(rw, r, id)
	case "regenerate":
		w.regenerateProtocolAPI(rw, r, id)
	default:
		writeError(rw, http.StatusNotFound, "not found")
	}
}

func (w *web) updateTunnelSettingsAPI(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPatch || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	w.withIdempotency(rw, r, "update-tunnel-settings:"+id, func() (int, any) {
		var req struct {
			Name       string `json:"name"`
			Port       int    `json:"port"`
			Subnet     string `json:"subnet"`
			DNS        string `json:"dns"`
			AllowedIPs string `json:"allowed_ips"`
			Keepalive  int    `json:"keepalive"`
			MTU        int    `json:"mtu"`
			Enabled    bool   `json:"enabled"`
		}
		if err := readJSON(r, &req); err != nil {
			return http.StatusBadRequest, errorPayload("invalid json")
		}
		tunnel, err := w.service.UpdateTunnelSettings(id, req.Name, req.Subnet, req.DNS, req.AllowedIPs, req.Keepalive, req.MTU, req.Port, req.Enabled)
		if err != nil {
			return http.StatusBadRequest, errorPayload(err.Error())
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
			return http.StatusBadRequest, errorPayload(err.Error())
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
			return http.StatusBadRequest, errorPayload(err.Error())
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
		if err := readJSON(r, &req); err != nil {
			return http.StatusBadRequest, errorPayload("invalid json")
		}
		if err := w.service.UpdateTunnelProtocol(id, req.Profile, req.Params); err != nil {
			return http.StatusBadRequest, errorPayload(err.Error())
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
		_ = readJSON(r, &req)
		if err := w.service.RegenerateTunnelProtocol(id, req.Profile); err != nil {
			return http.StatusBadRequest, errorPayload(err.Error())
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
		if err := readJSON(r, &req); err != nil {
			return http.StatusBadRequest, errorPayload("invalid json")
		}
		client, err := w.service.AddClientToTunnel(req.TunnelID, req.Name)
		if err != nil {
			return http.StatusBadRequest, errorPayload(err.Error())
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
	case "enable":
		w.setClientEnabledAPI(rw, r, id, true)
	case "disable":
		w.setClientEnabledAPI(rw, r, id, false)
	case "delete":
		w.deleteClientAPI(rw, r, id)
	case "qr":
		w.clientQRAPI(rw, r, id)
	case "qr.png":
		w.clientQRPNG(rw, r, id)
	default:
		writeError(rw, http.StatusNotFound, "not found")
	}
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
			return http.StatusNotFound, errorPayload(err.Error())
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
			return http.StatusNotFound, errorPayload(err.Error())
		}
		return http.StatusOK, map[string]any{"ok": true}
	})
}

func (w *web) clientConfig(rw http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/clients/config/")
	conf, client, err := w.service.ClientConfigForDownload(id)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Content-Type", "application/octet-stream")
	rw.Header().Set("Content-Disposition", `attachment; filename="`+configFilename(client)+`.conf"`)
	_, _ = rw.Write([]byte(conf))
}

func (w *web) clientQRPNG(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	payload, _, err := w.service.ClientAmneziaImportConfig(id)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	texts, err := render.AmneziaQRTexts(payload)
	if err != nil || len(texts) == 0 {
		writeError(rw, http.StatusInternalServerError, "qr failed")
		return
	}
	png, err := qrcode.Encode(texts[0], qrcode.High, 512)
	if err != nil {
		writeError(rw, http.StatusInternalServerError, "qr failed")
		return
	}
	rw.Header().Set("Content-Type", "image/png")
	_, _ = rw.Write(png)
}

func (w *web) clientQRAPI(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	payload, client, err := w.service.ClientAmneziaImportConfig(id)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	texts, err := render.AmneziaQRTexts(payload)
	if err != nil {
		writeError(rw, http.StatusInternalServerError, "qr failed")
		return
	}
	chunks := make([]map[string]any, 0, len(texts))
	for i, text := range texts {
		png, err := qrcode.Encode(text, qrcode.High, 512)
		if err != nil {
			writeError(rw, http.StatusInternalServerError, "qr failed")
			return
		}
		chunks = append(chunks, map[string]any{
			"index": i + 1,
			"total": len(texts),
			"png":   base64.StdEncoding.EncodeToString(png),
		})
	}
	writeJSON(rw, http.StatusOK, map[string]any{
		"kind":   "amnezia",
		"client": publicClient(client),
		"chunks": chunks,
	})
}

func (w *web) publicState(state config.State) map[string]any {
	var tunnels []map[string]any
	for _, tunnel := range state.Tunnels {
		status, _ := w.service.TunnelStatusByID(tunnel.ID)
		tunnels = append(tunnels, publicTunnel(tunnel, status))
	}
	return map[string]any{
		"authenticated":       true,
		"server_host":         state.ServerHost,
		"published_udp_ports": w.cfg.PublishedUDPPorts,
		"profiles": []map[string]any{
			profileMeta("awg_legacy_1_0", "1.0", "Legacy", true),
			profileMeta("awg_1_5", "1.5", "Modern", true),
			profileMeta("awg_2_0", "2.0", "Modern", true),
		},
		"tunnels": tunnels,
	}
}

func profileMeta(id, tab, label string, available bool) map[string]any {
	name, port, subnet := app.SuggestedTunnelSpec(id)
	return map[string]any{
		"id":               id,
		"tab":              tab,
		"label":            label,
		"available":        available,
		"suggested_name":   name,
		"suggested_port":   port,
		"suggested_subnet": subnet,
	}
}

func publicTunnel(tunnel config.Tunnel, status app.TunnelStatus) map[string]any {
	return map[string]any{
		"id":          tunnel.ID,
		"name":        tunnel.Name,
		"interface":   tunnel.InterfaceName,
		"enabled":     tunnel.Enabled,
		"listen_port": tunnel.ListenPort,
		"address":     tunnel.ServerAddress,
		"subnet":      tunnel.IPv4Subnet,
		"dns":         tunnel.DNS,
		"allowed_ips": tunnel.AllowedIPs,
		"keepalive":   tunnel.Keepalive,
		"mtu":         tunnel.MTU,
		"profile":     tunnel.ProtocolProfileID,
		"revision":    tunnel.ConfigRevision,
		"params":      orderedParams(tunnel.ProtocolProfileID, tunnel.ProtocolParams),
		"clients":     publicClients(tunnel),
		"status": map[string]any{
			"up":            status.Up,
			"apply_enabled": status.ApplyEnabled,
			"last_render":   status.LastRenderAt,
			"last_apply":    status.LastApplyAt,
			"last_error":    status.LastError,
		},
	}
}

func publicClients(tunnel config.Tunnel) []map[string]any {
	out := make([]map[string]any, 0, len(tunnel.Clients))
	for _, client := range tunnel.Clients {
		out = append(out, publicClientForTunnel(tunnel, client))
	}
	return out
}

func publicClient(client config.Client) map[string]any {
	return publicClientForTunnel(config.Tunnel{}, client)
}

func publicClientForTunnel(tunnel config.Tunnel, client config.Client) map[string]any {
	return map[string]any{
		"id":               client.ID,
		"tunnel_id":        client.TunnelID,
		"name":             client.Name,
		"enabled":          client.Enabled,
		"address":          client.IPv4Address,
		"revision":         client.ConfigRevision,
		"needs_new_config": tunnel.ConfigRevision > 0 && client.ConfigRevision < tunnel.ConfigRevision,
		"created_at":       client.CreatedAt,
		"updated_at":       client.UpdatedAt,
	}
}

func orderedParams(profileID string, params config.ProtocolParams) []map[string]string {
	keys := protocolParamKeys(profileID)
	out := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, map[string]string{"key": key, "value": params[key]})
	}
	return out
}

func protocolParamKeys(profileID string) []string {
	keys := []string{"Jc", "Jmin", "Jmax", "S1", "S2", "H1", "H2", "H3", "H4"}
	switch profileID {
	case "awg_1_5":
		keys = append(keys, "I1", "I2", "I3", "I4", "I5")
	case "awg_2_0":
		keys = append(keys, "S3", "S4", "I1", "I2", "I3", "I4", "I5")
	}
	sort.Strings(keys)
	return keys
}

func (w *web) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if w.cfg.Password == "" || w.hasSession(r) {
			next(rw, r)
			return
		}
		writeError(rw, http.StatusUnauthorized, "unauthorized")
	}
}

func (w *web) security(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		w.setSecurityHeaders(rw)
		next(rw, r)
	}
}

func (w *web) securityHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.setSecurityHeaders(rw)
		next.ServeHTTP(rw, r)
	})
}

func (w *web) setSecurityHeaders(rw http.ResponseWriter) {
	h := rw.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'")
}

func (w *web) validOrigin(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		return safeRequestSource(origin, r.Host)
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		return safeRequestSource(ref, r.Host)
	}
	return true
}

func safeRequestSource(raw, requestHost string) bool {
	if raw == "null" {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return true
	}
	if u.Host == "" {
		return true
	}
	return sameRequestHost(u.Host, requestHost)
}

func sameRequestHost(a, b string) bool {
	if a == b {
		return true
	}
	ah, ap := splitHostPort(a)
	bh, bp := splitHostPort(b)
	return ap == bp && isLoopbackHost(ah) && isLoopbackHost(bh)
}

func splitHostPort(hostport string) (string, string) {
	host, port, err := net.SplitHostPort(hostport)
	if err == nil {
		return strings.Trim(host, "[]"), port
	}
	return strings.Trim(hostport, "[]"), ""
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(host), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (w *web) allowLogin(ip string) bool {
	now := time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	var recent []time.Time
	for _, t := range w.limits[ip] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 5 {
		w.limits[ip] = recent
		return false
	}
	w.limits[ip] = append(recent, now)
	return true
}

func (w *web) setSession(rw http.ResponseWriter) {
	exp := time.Now().Add(sessionTTL).Unix()
	payload := fmt.Sprintf("%d", exp)
	sig := w.sign(payload)
	http.SetCookie(rw, &http.Cookie{Name: "awg_forge_session", Value: payload + "." + sig, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func (w *web) hasSession(r *http.Request) bool {
	c, err := r.Cookie("awg_forge_session")
	if err != nil {
		return false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 2 || !subtleCompare(w.sign(parts[0]), parts[1]) {
		return false
	}
	var exp int64
	if _, err := fmt.Sscanf(parts[0], "%d", &exp); err != nil {
		return false
	}
	return time.Now().Unix() < exp
}

func (w *web) sign(payload string) string {
	mac := hmac.New(sha256.New, w.sessions)
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func subtleCompare(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

func readJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
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
	w.finishIdempotency(cacheKey, status, body)
	writeCachedJSON(rw, status, body)
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

func (w *web) finishIdempotency(key string, status int, body []byte) {
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

func writeCachedJSON(rw http.ResponseWriter, status int, body []byte) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_, _ = rw.Write(body)
	_, _ = rw.Write([]byte("\n"))
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
