package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const sessionTTL = 30 * time.Minute

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

func noStore(rw http.ResponseWriter) {
	h := rw.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Pragma", "no-cache")
	h.Set("Expires", "0")
}

func (w *web) validOrigin(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		return w.safeRequestSource(origin, r)
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		return w.safeRequestSource(ref, r)
	}
	return requestHostIsLoopback(r.Host) && !w.trustedProxyRequest(r)
}

func (w *web) safeRequestSource(raw string, r *http.Request) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Host == "" {
		return false
	}
	return u.Scheme == w.effectiveScheme(r) && sameRequestHost(u.Host, r.Host)
}

func sameRequestHost(a, b string) bool {
	if a == b {
		return true
	}
	ah, ap := splitHostPort(a)
	bh, bp := splitHostPort(b)
	return ap == bp && isLoopbackHost(ah) && isLoopbackHost(bh)
}

func requestHostIsLoopback(hostport string) bool {
	return isLoopbackHost(hostOnly(hostport))
}

func hostOnly(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(hostport, "[]")
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

func (w *web) setSession(rw http.ResponseWriter, r *http.Request) {
	exp := time.Now().Add(sessionTTL).Unix()
	payload := fmt.Sprintf("%d", exp)
	sig := w.sign(payload)
	http.SetCookie(rw, sessionCookie(r, payload+"."+sig, 0, w.sessionCookieSecure(r)))
}

func sessionCookie(r *http.Request, value string, maxAge int, secure bool) *http.Cookie {
	// Secure is intentionally dynamic: auto enables it for non-loopback hosts while preserving localhost/SSH tunnel flows.
	return &http.Cookie{ // nosemgrep: go.lang.security.audit.net.cookie-missing-secure.cookie-missing-secure
		Name:     "awg_forge_session",
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	}
}

func (w *web) sessionCookieSecure(r *http.Request) bool {
	switch w.cfg.SessionCookieSecure {
	case "true":
		return true
	case "false":
		return false
	default:
		if r == nil {
			return true
		}
		if w.effectiveScheme(r) == "https" {
			return true
		}
		return !requestHostIsLoopback(r.Host)
	}
}

func (w *web) effectiveScheme(r *http.Request) string {
	if r != nil && r.TLS != nil {
		return "https"
	}
	if w.trustedProxyRequest(r) {
		scheme := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")))
		if scheme == "http" || scheme == "https" {
			return scheme
		}
	}
	return "http"
}

func (w *web) clientIP(r *http.Request) string {
	remote, ok := remoteAddress(r)
	if !ok {
		return ""
	}
	if !w.trustedProxyRequest(r) {
		return remote.String()
	}
	values := r.Header.Values("X-Forwarded-For")
	if len(values) == 0 {
		return remote.String()
	}
	parts := strings.Split(strings.Join(values, ","), ",")
	addresses := make([]netip.Addr, 0, len(parts))
	for _, part := range parts {
		address, err := netip.ParseAddr(strings.TrimSpace(part))
		if err != nil {
			return remote.String()
		}
		addresses = append(addresses, address.Unmap())
	}
	for index := len(addresses) - 1; index >= 0; index-- {
		if !w.trustedProxyAddress(addresses[index]) {
			return addresses[index].String()
		}
	}
	return remote.String()
}

func (w *web) trustedProxyRequest(r *http.Request) bool {
	if r == nil || !w.cfg.WebUITrustProxyHeaders {
		return false
	}
	remote, ok := remoteAddress(r)
	return ok && w.trustedProxyAddress(remote)
}

func (w *web) trustedProxyAddress(address netip.Addr) bool {
	for _, prefix := range w.cfg.WebUITrustedProxyCIDRs {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func remoteAddress(r *http.Request) (netip.Addr, bool) {
	if r == nil {
		return netip.Addr{}, false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = strings.Trim(r.RemoteAddr, "[]")
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return address.Unmap(), true
}

func (w *web) loginAuditFields(r *http.Request) map[string]any {
	return map[string]any{
		"client_ip_hash": w.sign("client-ip:" + w.clientIP(r)),
		"scheme":         w.effectiveScheme(r),
	}
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
