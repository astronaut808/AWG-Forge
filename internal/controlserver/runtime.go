// Package controlserver contains the isolated, explicitly enabled control transport.
package controlserver

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/google/uuid"
)

const (
	maxBodyBytes    = 1 << 20
	maxHeaderBytes  = 16 << 10
	maxConnections  = 64
	maxRequests     = 32
	shutdownTimeout = 10 * time.Second
)

// Authorizer must check the current certificate registry and node binding on
// every request. An error, including database unavailability, denies access.
type Authorizer interface {
	Authorize(context.Context, *x509.Certificate, string) (NodeIdentity, error)
}

// NodeIdentity is the registry-verified authority passed to a typed route.
// Request headers and CSR subject fields never populate it.
type NodeIdentity struct {
	ControllerID string
	NodeID       string
	BindingEpoch uint64
}

type nodeIdentityContextKey struct{}

func IdentityFromContext(ctx context.Context) (NodeIdentity, bool) {
	identity, ok := ctx.Value(nodeIdentityContextKey{}).(NodeIdentity)
	return identity, ok && identity.ControllerID != "" && identity.NodeID != "" && identity.BindingEpoch > 0
}

// Route is an exact method/path pair. ID is passed to the authorizer; handlers
// must scope node-specific operations to the identity in the request context.
type Route struct {
	ID      string
	Method  string
	Path    string
	Handler http.Handler
	// Bootstrap is accepted only for the two hard-coded enrollment route pairs.
	Bootstrap bool
}

type routeKey struct{ method, path string }

type Runtime struct {
	endpoint         controlpki.Endpoint
	tlsConfig        *tls.Config
	mu               sync.Mutex
	snapshot         *certificateSnapshot
	caPEM            []byte
	pin              string
	listener         net.Listener
	connections      map[*trackedConnection]struct{}
	changed          chan struct{}
	closed           bool
	serving          bool
	closeReason      error
	authorizer       Authorizer
	routes           map[routeKey]Route
	requests         chan struct{}
	activeRequests   sync.WaitGroup // Admission/Add is serialized with Close by mu.
	requestContext   context.Context
	cancelRequests   context.CancelFunc
	bootstrapWindow  time.Time
	bootstrapCount   int
	bootstrapSources map[string]int
}

// New validates the prepared identity and builds a separate, closed-by-default
// route table. It does not bind or start a listener.
func New(material controlpki.Material, endpoint controlpki.Endpoint, pin string, authorizer Authorizer, routes []Route) (*Runtime, error) {
	return NewWithOptions(material, endpoint, pin, authorizer, routes, Options{})
}

type Options struct {
	AllowNonLoopback bool
}

// NewWithOptions permits a specific non-loopback bind only after application
// consent. It preserves the same TLS, admission and resource limits as New.
func NewWithOptions(material controlpki.Material, endpoint controlpki.Endpoint, pin string, authorizer Authorizer, routes []Route, options Options) (*Runtime, error) {
	bind, err := netip.ParseAddr(endpoint.BindIP)
	if err != nil || bind.Is4In6() || bind.Zone() != "" || bind.IsUnspecified() || bind.IsMulticast() || endpoint.BindIP != bind.String() {
		return nil, errors.New("control runtime requires a specific literal bind IP")
	}
	if !bind.IsLoopback() && !options.AllowNonLoopback {
		return nil, errors.New("control runtime requires explicit non-loopback consent")
	}
	if endpoint.Port < 1 || endpoint.Port > 65535 {
		return nil, errors.New("invalid control runtime port")
	}
	if err := controlpki.Validate(material, endpoint, pin, time.Now(), false); err != nil {
		return nil, fmt.Errorf("invalid prepared control identity: %w", err)
	}
	snapshot, err := makeCertificateSnapshot(material, endpoint, pin)
	if err != nil {
		return nil, err
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(material.CACert) {
		return nil, errors.New("invalid control client CA")
	}
	if len(routes) > 0 && authorizer == nil {
		return nil, errors.New("control routes require an authorizer")
	}
	routeTable := make(map[routeKey]Route, len(routes))
	routeIDs := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		if route.ID == "" || route.Method == "" || strings.ToUpper(route.Method) != route.Method || route.Path == "" ||
			!strings.HasPrefix(route.Path, "/control/v1/") || path.Clean(route.Path) != route.Path ||
			strings.ContainsAny(route.Path, "?#%") || route.Handler == nil {
			return nil, errors.New("invalid control route")
		}
		if route.Bootstrap && !validBootstrapRoute(route) {
			return nil, errors.New("invalid bootstrap route")
		}
		key := routeKey{route.Method, route.Path}
		if _, exists := routeTable[key]; exists {
			return nil, errors.New("duplicate control route")
		}
		if _, exists := routeIDs[route.ID]; exists {
			return nil, errors.New("duplicate control route identity")
		}
		routeTable[key] = route
		routeIDs[route.ID] = struct{}{}
	}
	requestContext, cancelRequests := context.WithCancel(context.Background())
	runtime := &Runtime{
		endpoint: endpoint, snapshot: snapshot, caPEM: append([]byte(nil), material.CACert...), pin: pin,
		connections: make(map[*trackedConnection]struct{}), changed: make(chan struct{}, 1),
		tlsConfig: &tls.Config{
			MinVersion: tls.VersionTLS13,
			ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: clientCAs,
			SessionTicketsDisabled: true, NextProtos: []string{"http/1.1"},
		},
		authorizer: authorizer, routes: routeTable, requests: make(chan struct{}, maxRequests),
		requestContext: requestContext, cancelRequests: cancelRequests, bootstrapSources: make(map[string]int),
	}
	// Certificates must remain empty: IP clients send no SNI and must still use
	// the same callback as DNS clients, including ServeTLS's cloned configuration.
	runtime.tlsConfig.GetCertificate = runtime.getCertificate
	return runtime, nil
}

// Bind reserves the socket without accepting connections or TLS. An
// application owner can commit enablement while holding this exact socket.
func (runtime *Runtime) Bind() error {
	if runtime == nil || runtime.tlsConfig == nil {
		return errors.New("uninitialized control runtime")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.bindLocked()
}

func (runtime *Runtime) bindLocked() error {
	if !time.Now().Before(runtime.snapshot.expiresAt) {
		return controlpki.ErrExpired
	}
	if runtime.closed || runtime.serving || runtime.listener != nil {
		return errors.New("control runtime is closed or already bound")
	}
	address := net.JoinHostPort(runtime.endpoint.BindIP, strconv.Itoa(runtime.endpoint.Port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("bind control listener: %w", err)
	}
	runtime.listener = limitListener(listener, maxConnections)
	return nil
}

// Serve uses a reserved socket, or binds it for isolated transport callers. It
// drains on cancellation. The application owner binds before committing state.
func (runtime *Runtime) Serve(ctx context.Context) error {
	if runtime == nil || runtime.tlsConfig == nil {
		return errors.New("uninitialized control runtime")
	}
	if err := ctx.Err(); err != nil {
		runtime.Close()
		return err
	}
	runtime.mu.Lock()
	if !time.Now().Before(runtime.snapshot.expiresAt) {
		runtime.closeLocked(controlpki.ErrExpired)
		runtime.mu.Unlock()
		return controlpki.ErrExpired
	}
	if runtime.closed || runtime.serving {
		runtime.mu.Unlock()
		return errors.New("control runtime is closed or already serving")
	}
	if runtime.listener == nil {
		if err := runtime.bindLocked(); err != nil {
			runtime.mu.Unlock()
			return err
		}
	}
	runtime.serving = true
	runtime.mu.Unlock()
	defer runtime.Close()
	server := &http.Server{
		Handler: runtime.handler(), TLSConfig: runtime.tlsConfig.Clone(),
		// Raw handshake diagnostics may contain certificate subjects.
		ErrorLog: log.New(io.Discard, "", 0), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second,
		MaxHeaderBytes: maxHeaderBytes,
		BaseContext:    func(net.Listener) context.Context { return runtime.requestContext },
	}
	result := make(chan error, 1)
	go func() {
		result <- server.ServeTLS(&trackingListener{Listener: runtime.listener, runtime: runtime}, "", "")
	}()
	drain := func() error {
		runtime.cancelRequests()
		runtime.mu.Lock()
		deadline := time.Now().Add(shutdownTimeout)
		if runtime.snapshot.expiresAt.Before(deadline) {
			deadline = runtime.snapshot.expiresAt
		}
		runtime.mu.Unlock()
		shutdownCtx, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		err := server.Shutdown(shutdownCtx)
		if err != nil {
			_ = server.Close()
		}
		return err
	}
	for {
		runtime.mu.Lock()
		expiresAt := runtime.snapshot.expiresAt
		if !time.Now().Before(expiresAt) && !runtime.closed {
			runtime.closeLocked(controlpki.ErrExpired)
		}
		closed, reason := runtime.closed, runtime.closeReason
		runtime.mu.Unlock()
		if closed {
			drainErr := drain()
			<-result
			if reason != nil {
				return errors.Join(reason, drainErr)
			}
			return drainErr
		}
		expiry := time.NewTimer(time.Until(expiresAt))
		select {
		case err := <-result:
			expiry.Stop()
			runtime.Close()
			drainErr := drain()
			runtime.mu.Lock()
			reason = runtime.closeReason
			runtime.mu.Unlock()
			if reason != nil {
				return errors.Join(reason, drainErr)
			}
			if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
				return drainErr
			}
			return errors.Join(err, drainErr)
		case <-expiry.C:
			// Re-read the current snapshot under the lock before expiring it. A reload
			// can win the race with the predecessor's timer without being shut down.
		case <-runtime.changed:
			expiry.Stop()
		case <-ctx.Done():
			expiry.Stop()
			err := drain()
			runtime.mu.Lock()
			if !time.Now().Before(runtime.snapshot.expiresAt) {
				runtime.closeLocked(controlpki.ErrExpired)
			}
			reason = runtime.closeReason
			runtime.mu.Unlock()
			if err != nil {
				_ = server.Close()
			}

			serveErr := <-result
			if reason != nil {
				return errors.Join(reason, err)
			}
			if err != nil && !errors.Is(err, net.ErrClosed) {
				return err
			}
			if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && !errors.Is(serveErr, net.ErrClosed) {
				return serveErr
			}
			return nil
		}
	}
}

func (runtime *Runtime) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		select {
		case runtime.requests <- struct{}{}:
			defer func() { <-runtime.requests }()
		default:
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		runtime.mu.Lock()
		if runtime.closed {
			runtime.mu.Unlock()
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		runtime.activeRequests.Add(1)
		runtime.mu.Unlock()
		defer runtime.activeRequests.Done()
		// EscapedPath rules out alternate encodings of an authorized route.
		route, ok := runtime.resolveRoute(r.Method, r.URL.EscapedPath())
		if !ok || r.URL.RawQuery != "" {
			http.NotFound(w, r)
			return
		}
		if !runtime.identityValid(time.Now()) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if route.Bootstrap {
			if r.TLS == nil || !runtime.allowBootstrap(r.RemoteAddr, time.Now()) {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "rate limited", http.StatusTooManyRequests)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
			route.Handler.ServeHTTP(w, r)
			return
		}
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		cert := r.TLS.PeerCertificates[0]
		now := time.Now()
		if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) || !runtime.identityValid(now) || runtime.authorizer == nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		identity, err := runtime.authorizer.Authorize(r.Context(), cert, route.ID)
		if err != nil || identity.ControllerID == "" || identity.NodeID == "" || identity.BindingEpoch == 0 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			http.Error(w, "invalid request", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		route.Handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), nodeIdentityContextKey{}, identity)))
	})
}

func validBootstrapRoute(route Route) bool {
	return route.ID == "enrollment.bootstrap" && route.Method == http.MethodGet && route.Path == "/control/v1/bootstrap" ||
		route.ID == "enrollment.claim" && route.Method == http.MethodPost && route.Path == "/control/v1/enrollments/{invitation_id}/claim" ||
		route.ID == "enrollment.status" && route.Method == http.MethodGet && route.Path == "/control/v1/enrollments/{enrollment_id}"
}

func (runtime *Runtime) resolveRoute(method, requestPath string) (Route, bool) {
	if route, ok := runtime.routes[routeKey{method, requestPath}]; ok {
		return route, true
	}
	parts := strings.Split(requestPath, "/")
	if len(parts) != 5 && len(parts) != 6 || parts[1] != "control" || parts[2] != "v1" || parts[3] != "enrollments" {
		return Route{}, false
	}
	id, err := uuid.Parse(parts[4])
	if err != nil || id.String() != parts[4] {
		return Route{}, false
	}
	template := "/control/v1/enrollments/{enrollment_id}"
	if len(parts) == 6 {
		if parts[5] != "claim" {
			return Route{}, false
		}
		template = "/control/v1/enrollments/{invitation_id}/claim"
	}
	route, ok := runtime.routes[routeKey{method, template}]
	return route, ok && route.Bootstrap && validBootstrapRoute(route)
}

func (runtime *Runtime) allowBootstrap(remote string, now time.Time) bool {
	source, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.bootstrapWindow.IsZero() || now.Sub(runtime.bootstrapWindow) >= time.Minute {
		runtime.bootstrapWindow = now
		runtime.bootstrapCount = 0
		clear(runtime.bootstrapSources)
	}
	if runtime.bootstrapCount >= 60 || runtime.bootstrapSources[source] >= 30 {
		return false
	}
	if _, exists := runtime.bootstrapSources[source]; !exists && len(runtime.bootstrapSources) >= 128 {
		return false
	}
	runtime.bootstrapCount++
	runtime.bootstrapSources[source]++
	return true
}
