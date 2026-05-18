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
)

//go:embed static/*
var staticFiles embed.FS

type web struct {
	cfg      config.Config
	service  *app.Service
	sessions []byte
	limits   map[string][]time.Time
	mu       sync.Mutex
}

func Serve(cfg config.Config, service *app.Service) error {
	secret, err := service.SessionSecret()
	if err != nil {
		return err
	}
	w := &web{cfg: cfg, service: service, sessions: []byte(secret), limits: map[string][]time.Time{}}
	mux := http.NewServeMux()
	mux.Handle("/static/", w.securityHandler(http.FileServer(http.FS(staticFiles))))
	mux.HandleFunc("/", w.security(w.index))
	mux.HandleFunc("/api/login", w.security(w.loginAPI))
	mux.HandleFunc("/api/logout", w.security(w.requireAuth(w.logoutAPI)))
	mux.HandleFunc("/api/state", w.security(w.requireAuth(w.stateAPI)))
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

func (w *web) tunnelsAPI(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	var req struct {
		Profile string `json:"profile"`
		Name    string `json:"name"`
		Port    int    `json:"port"`
		Subnet  string `json:"subnet"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(rw, http.StatusBadRequest, "invalid json")
		return
	}
	tunnel, err := w.service.CreateTunnel(req.Profile, req.Name, req.Subnet, req.Port)
	if err != nil {
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(rw, http.StatusCreated, map[string]any{"tunnel": publicTunnel(tunnel, app.TunnelStatus{})})
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
		writeError(rw, http.StatusBadRequest, "invalid json")
		return
	}
	tunnel, err := w.service.UpdateTunnelSettings(id, req.Name, req.Subnet, req.DNS, req.AllowedIPs, req.Keepalive, req.MTU, req.Port, req.Enabled)
	if err != nil {
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"tunnel": publicTunnel(tunnel, app.TunnelStatus{})})
}

func (w *web) deleteTunnelAPI(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodDelete || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	if err := w.service.DeleteTunnel(id); err != nil {
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

func (w *web) restartTunnelAPI(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	if err := w.service.RestartTunnelByID(id); err != nil {
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

func (w *web) updateProtocolAPI(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPatch || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	var req struct {
		Profile string                `json:"profile"`
		Params  config.ProtocolParams `json:"params"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(rw, http.StatusBadRequest, "invalid json")
		return
	}
	if err := w.service.UpdateTunnelProtocol(id, req.Profile, req.Params); err != nil {
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

func (w *web) regenerateProtocolAPI(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	var req struct {
		Profile string `json:"profile"`
	}
	_ = readJSON(r, &req)
	if err := w.service.RegenerateTunnelProtocol(id, req.Profile); err != nil {
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

func (w *web) clientsAPI(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	var req struct {
		TunnelID string `json:"tunnel_id"`
		Name     string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(rw, http.StatusBadRequest, "invalid json")
		return
	}
	client, err := w.service.AddClientToTunnel(req.TunnelID, req.Name)
	if err != nil {
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(rw, http.StatusCreated, map[string]any{"client": publicClient(client)})
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
	if err := w.service.SetClientEnabled(id, enabled); err != nil {
		writeError(rw, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

func (w *web) deleteClientAPI(rw http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodDelete || !w.validOrigin(r) {
		writeError(rw, http.StatusForbidden, "forbidden")
		return
	}
	if err := w.service.RemoveClient(id); err != nil {
		writeError(rw, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
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
	conf, err := w.service.ClientConfig(id)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	png, err := qrcode.Encode(conf, qrcode.Medium, 320)
	if err != nil {
		writeError(rw, http.StatusInternalServerError, "qr failed")
		return
	}
	rw.Header().Set("Content-Type", "image/png")
	_, _ = rw.Write(png)
}

func (w *web) publicState(state config.State) map[string]any {
	var tunnels []map[string]any
	for _, tunnel := range state.Tunnels {
		status, _ := w.service.TunnelStatusByID(tunnel.ID)
		tunnels = append(tunnels, publicTunnel(tunnel, status))
	}
	return map[string]any{
		"authenticated": true,
		"server_host":   state.ServerHost,
		"profiles": []map[string]any{
			profileMeta("awg_legacy_1_0", "1.0", "Legacy", true),
			profileMeta("awg_1_5", "1.5", "Modern", true),
			profileMeta("awg_2_0", "2.0", "Planned", false),
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
		"params":      orderedParams(tunnel.ProtocolProfileID, tunnel.ProtocolParams),
		"clients":     publicClients(tunnel.Clients),
		"status": map[string]any{
			"up":            status.Up,
			"apply_enabled": status.ApplyEnabled,
			"last_render":   status.LastRenderAt,
			"last_apply":    status.LastApplyAt,
			"last_error":    status.LastError,
		},
	}
}

func publicClients(clients []config.Client) []map[string]any {
	out := make([]map[string]any, 0, len(clients))
	for _, client := range clients {
		out = append(out, publicClient(client))
	}
	return out
}

func publicClient(client config.Client) map[string]any {
	return map[string]any{
		"id":         client.ID,
		"tunnel_id":  client.TunnelID,
		"name":       client.Name,
		"enabled":    client.Enabled,
		"address":    client.IPv4Address,
		"created_at": client.CreatedAt,
		"updated_at": client.UpdatedAt,
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
	case "awg_1_5", "awg_2_0":
		keys = append(keys, "I1", "I2", "I3", "I4", "I5")
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
	exp := time.Now().Add(24 * time.Hour).Unix()
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

func writeJSON(rw http.ResponseWriter, status int, payload any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(payload)
}

func writeError(rw http.ResponseWriter, status int, message string) {
	writeJSON(rw, status, map[string]any{"error": message})
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
