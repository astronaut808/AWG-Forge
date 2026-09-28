package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/controlserver"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/storage"
)

func TestControlNodeCertificateIssuanceAndRealTLSRevocation(t *testing.T) {
	ctx := context.Background()
	cfg := controllerTestConfig(t)
	service := newFastControllerTestService(cfg)
	if _, err := service.Init(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := service.ActivateController(ctx, controllerTestActivationRequest(t, now)); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback unavailable: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	control, err := service.PrepareControlIdentity(ctx, ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: port, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	const nodeID = "11111111-1111-4111-8111-111111111111"
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	issuedPEM, err := service.issueInitialNodeCertificate(ctx, nodeID, csr, now)
	if err != nil {
		t.Fatal(err)
	}
	retryPEM, err := newFastControllerTestService(cfg).issueInitialNodeCertificate(ctx, nodeID, csr, now.Add(time.Second))
	if err != nil || !bytes.Equal(retryPEM, issuedPEM) {
		t.Fatalf("exact issuance retry = %v", err)
	}
	block, trailing := pem.Decode(issuedPEM)
	if block == nil || len(trailing) != 0 {
		t.Fatal("invalid issued certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	material, err := storage.New(cfg.ConfigDir).LoadControlIdentity(control.CAGeneration, control.ServerGeneration)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqldb.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := now
	authorizer, err := NewControlNodeAuthorizer(db, controllerIDFromStore(t, cfg.ConfigDir), control, material, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authorizer.Authorize(ctx, cert, ""); !errors.Is(err, sqldb.ErrNodeCertificateDenied) {
		t.Fatalf("empty route error = %v", err)
	}
	identity, err := authorizer.Authorize(ctx, cert, "node.test")
	if err != nil || identity.NodeID != nodeID || identity.BindingEpoch != 1 {
		t.Fatalf("authorized identity = %+v, %v", identity, err)
	}
	clock = cert.NotAfter
	if _, err := authorizer.Authorize(ctx, cert, "node.test"); !errors.Is(err, sqldb.ErrNodeCertificateDenied) {
		t.Fatalf("expired certificate error = %v", err)
	}
	clock = time.Now().UTC()
	endpoint := controlpki.Endpoint{BindIP: control.BindIP, Advertised: control.Advertised, Port: control.Port}
	runtime, err := controlserver.New(material, endpoint, control.CAPin, authorizer, []controlserver.Route{{
		ID: "node.test", Method: http.MethodGet, Path: "/control/v1/node-test",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := controlserver.IdentityFromContext(r.Context())
			if !ok || identity.NodeID != nodeID || identity.BindingEpoch != 1 {
				http.Error(w, "bad identity", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() { result <- runtime.Serve(serveCtx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-result; err != nil {
			t.Errorf("control shutdown: %v", err)
		}
	})
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(material.CACert) {
		t.Fatal("invalid root")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: control.Advertised,
		Certificates: []tls.Certificate{{Certificate: [][]byte{cert.Raw}, PrivateKey: key}},
	}}, Timeout: 3 * time.Second}
	url := "https://" + net.JoinHostPort(control.BindIP, strconv.Itoa(control.Port)) + "/control/v1/node-test"
	var response *http.Response
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err = client.Get(url)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("initial TLS response = %d", response.StatusCode)
	}
	if err := db.RevokeNodeCertificate(ctx, control.CAGeneration, cert.SerialNumber.String(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	reused := false
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused },
	}))
	response, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden || !reused {
		t.Fatalf("revoked keep-alive response = %d, reused = %v", response.StatusCode, reused)
	}
	if _, err := newFastControllerTestService(cfg).issueInitialNodeCertificate(ctx, nodeID, csr, time.Now().UTC()); !errors.Is(err, sqldb.ErrNodeCertificateDenied) {
		t.Fatalf("revoked retry error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := authorizer.Authorize(ctx, cert, "node.test"); err == nil || errors.Is(err, sqldb.ErrNodeCertificateDenied) {
		t.Fatalf("database outage error = %v", err)
	}
	response, err = client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("database outage response = %d", response.StatusCode)
	}
}

func controllerIDFromStore(t *testing.T, configDir string) string {
	t.Helper()
	state, err := storage.New(configDir).Load()
	if err != nil || state.Controller == nil {
		t.Fatalf("load controller: %v", err)
	}
	return state.Controller.ControllerID
}
