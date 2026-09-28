// Package controlserver contains the isolated control transport. No production
// caller starts it until node authorization and explicit enablement exist.
package controlserver

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
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
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
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
}

type routeKey struct{ method, path string }

type Runtime struct {
	endpoint   controlpki.Endpoint
	tlsConfig  *tls.Config
	expiresAt  time.Time
	authorizer Authorizer
	routes     map[routeKey]Route
	requests   chan struct{}
}

// New validates the prepared identity and builds a separate, closed-by-default
// route table. It does not bind or start a listener.
func New(material controlpki.Material, endpoint controlpki.Endpoint, pin string, authorizer Authorizer, routes []Route) (*Runtime, error) {
	bind, err := netip.ParseAddr(endpoint.BindIP)
	if err != nil || !bind.IsLoopback() || bind.Is4In6() || bind.Zone() != "" {
		return nil, errors.New("control runtime requires a literal loopback bind IP")
	}
	if endpoint.Port < 1 || endpoint.Port > 65535 {
		return nil, errors.New("invalid control runtime port")
	}
	if err := controlpki.Validate(material, endpoint, pin, time.Now(), false); err != nil {
		return nil, fmt.Errorf("invalid prepared control identity: %w", err)
	}
	serverCert, err := tls.X509KeyPair(material.ServerCert, material.ServerKey)
	if err != nil {
		return nil, errors.New("invalid control server key pair")
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(material.CACert) {
		return nil, errors.New("invalid control client CA")
	}
	caBlock, _ := pem.Decode(material.CACert)
	if caBlock == nil {
		return nil, errors.New("invalid control CA certificate")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return nil, errors.New("invalid control CA certificate")
	}
	serverLeaf, err := x509.ParseCertificate(serverCert.Certificate[0])
	if err != nil {
		return nil, errors.New("invalid control server certificate")
	}
	expiresAt := serverLeaf.NotAfter
	if caCert.NotAfter.Before(expiresAt) {
		expiresAt = caCert.NotAfter
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
	return &Runtime{
		endpoint:  endpoint,
		expiresAt: expiresAt,
		tlsConfig: &tls.Config{
			MinVersion:             tls.VersionTLS13,
			Certificates:           []tls.Certificate{serverCert},
			ClientAuth:             tls.VerifyClientCertIfGiven,
			ClientCAs:              clientCAs,
			SessionTicketsDisabled: true,
			NextProtos:             []string{"http/1.1"},
		},
		authorizer: authorizer,
		routes:     routeTable,
		requests:   make(chan struct{}, maxRequests),
	}, nil
}

// Serve binds only the validated loopback endpoint and drains on cancellation.
func (runtime *Runtime) Serve(ctx context.Context) error {
	if runtime == nil || runtime.tlsConfig == nil {
		return errors.New("uninitialized control runtime")
	}
	if !time.Now().Before(runtime.expiresAt) {
		return controlpki.ErrExpired
	}
	address := net.JoinHostPort(runtime.endpoint.BindIP, strconv.Itoa(runtime.endpoint.Port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("bind control loopback listener: %w", err)
	}
	defer func() { _ = listener.Close() }()
	server := &http.Server{
		Handler:   runtime.handler(),
		TLSConfig: runtime.tlsConfig.Clone(),
		// net/http's default logger includes certificate subjects and remote
		// addresses in handshake failures. Do not emit those raw diagnostics.
		ErrorLog:          log.New(io.Discard, "", 0),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    maxHeaderBytes,
	}
	result := make(chan error, 1)
	go func() { result <- server.ServeTLS(limitListener(listener, maxConnections), "", "") }()
	expiry := time.NewTimer(time.Until(runtime.expiresAt))
	defer expiry.Stop()
	select {
	case err := <-result:
		_ = server.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-expiry.C:
		_ = server.Close()
		<-result
		return controlpki.ErrExpired
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			<-result
			return err
		}
		err := <-result
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
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
		// EscapedPath rules out alternate encodings of an authorized route.
		route, ok := runtime.routes[routeKey{r.Method, r.URL.EscapedPath()}]
		if !ok || r.URL.RawQuery != "" {
			http.NotFound(w, r)
			return
		}
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		cert := r.TLS.PeerCertificates[0]
		now := time.Now()
		if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) || !now.Before(runtime.expiresAt) || runtime.authorizer == nil {
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
