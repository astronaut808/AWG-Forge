package controlserver

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
	_ "modernc.org/sqlite"
)

type testAuthorizer struct {
	db    *sql.DB
	calls atomic.Int32
}

func (auth *testAuthorizer) Authorize(_ context.Context, cert *x509.Certificate, route string) (bool, error) {
	auth.calls.Add(1)
	if cert == nil || route != "node.test" {
		return false, nil
	}
	var allowed bool
	err := auth.db.QueryRow("SELECT active FROM test_cert_registry WHERE serial = ?", cert.SerialNumber.String()).Scan(&allowed)
	return allowed, err
}

func TestRuntimeRealTLSFailClosedAndRevocation(t *testing.T) {
	endpoint := loopbackEndpoint(t)
	material, pin, err := controlpki.Generate(endpoint, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	clientCert := issueTestClient(t, material)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("CREATE TABLE test_cert_registry (serial TEXT PRIMARY KEY, active INTEGER NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO test_cert_registry (serial, active) VALUES ('42', 1)"); err != nil {
		t.Fatal(err)
	}
	auth := &testAuthorizer{db: db}
	runtime, err := New(material, endpoint, pin, auth, []Route{{
		ID: "node.test", Method: http.MethodGet, Path: "/control/v1/node-test",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- runtime.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-result; err != nil {
			t.Errorf("control shutdown: %v", err)
		}
	})

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(material.CACert) {
		t.Fatal("parse root")
	}
	newClient := func(root *x509.CertPool, name string, certs []tls.Certificate) *http.Client {
		return &http.Client{Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: root, ServerName: name, Certificates: certs},
			MaxIdleConnsPerHost: 1,
		}, Timeout: 3 * time.Second}
	}
	url := "https://" + net.JoinHostPort(endpoint.BindIP, strconv.Itoa(endpoint.Port)) + "/control/v1/node-test"
	client := newClient(roots, endpoint.Advertised, []tls.Certificate{clientCert})
	request := func(client *http.Client, url string, header bool, reused ...*bool) (*http.Response, error) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		if header {
			req.Header.Set("X-Forwarded-Client-Cert", "forged")
			req.Header.Set("Cookie", "session=forged")
		}
		if len(reused) > 0 {
			req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
				GotConn: func(info httptrace.GotConnInfo) { *reused[0] = info.Reused },
			}))
		}
		return client.Do(req)
	}
	waitForTLS(t, client, url)
	assertStatus := func(client *http.Client, url string, header bool, want int) {
		t.Helper()
		response, err := request(client, url, header)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != want {
			t.Fatalf("status = %d, want %d", response.StatusCode, want)
		}
		if response.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store header")
		}
	}
	assertStatus(client, url, false, http.StatusNoContent)
	if _, err := db.Exec("UPDATE test_cert_registry SET active = 0 WHERE serial = '42'"); err != nil {
		t.Fatal(err)
	}
	reused := false
	response, err := request(client, url, false, &reused)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden || !reused {
		t.Fatalf("revocation response = %d, reused = %v", response.StatusCode, reused)
	}
	if _, err := db.Exec("UPDATE test_cert_registry SET active = 1 WHERE serial = '42'"); err != nil {
		t.Fatal(err)
	}
	assertStatus(client, url, false, http.StatusNoContent)
	large, err := http.NewRequest(http.MethodGet, url, bytes.NewReader(make([]byte, maxBodyBytes+1)))
	if err != nil {
		t.Fatal(err)
	}
	response, err = client.Do(large)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body status = %d", response.StatusCode)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	assertStatus(client, url, false, http.StatusForbidden)
	if auth.calls.Load() < 4 {
		t.Fatal("authorization was not rechecked per request")
	}
	withoutCert := newClient(roots, endpoint.Advertised, nil)
	assertStatus(withoutCert, url, true, http.StatusForbidden)
	assertStatus(client, url+"/other", false, http.StatusNotFound)
	assertStatus(client, url+"?node=other", false, http.StatusNotFound)
	assertStatus(client, url[:len(url)-len("node-test")]+"%6eode-test", false, http.StatusNotFound)
	wrongRoot := x509.NewCertPool()
	if response, err := request(newClient(wrongRoot, endpoint.Advertised, nil), url, false); err == nil {
		_ = response.Body.Close()
		t.Fatal("client accepted wrong CA")
	}
	if response, err := request(newClient(roots, "wrong.example.com", nil), url, false); err == nil {
		_ = response.Body.Close()
		t.Fatal("client accepted wrong server name")
	}
	other, _, err := controlpki.Generate(endpoint, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	invalidClient := issueTestClient(t, other)
	if response, err := request(newClient(roots, endpoint.Advertised, []tls.Certificate{invalidClient}), url, false); err == nil {
		_ = response.Body.Close()
		t.Fatal("server accepted client certificate from another CA")
	}
}

func TestRuntimeRejectsUnsafeConstruction(t *testing.T) {
	endpoint := loopbackEndpoint(t)
	material, pin, err := controlpki.Generate(endpoint, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(material, endpoint, pin, nil, []Route{{ID: "test", Method: "GET", Path: "/control/v1/test", Handler: http.NotFoundHandler()}}); err == nil {
		t.Fatal("route without authorizer accepted")
	}
	for _, bad := range []string{"0.0.0.0", "192.0.2.1"} {
		changed := endpoint
		changed.BindIP = bad
		if _, err := New(material, changed, pin, nil, nil); err == nil {
			t.Fatalf("non-loopback bind %q accepted", bad)
		}
	}
	material.ServerKey = []byte("broken")
	if _, err := New(material, endpoint, pin, nil, nil); err == nil {
		t.Fatal("broken identity accepted")
	}
}

func TestRuntimeStopsAtIdentityExpiry(t *testing.T) {
	endpoint := loopbackEndpoint(t)
	material, pin, err := controlpki.Generate(endpoint, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := New(material, endpoint, pin, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime.expiresAt = time.Now().Add(2 * time.Second)
	result := make(chan error, 1)
	go func() { result <- runtime.Serve(context.Background()) }()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(material.CACert) {
		t.Fatal("parse root")
	}
	address := net.JoinHostPort(endpoint.BindIP, strconv.Itoa(endpoint.Port))
	url := "https://" + address + "/control/v1/no-route"
	clientConfig := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: endpoint.Advertised}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientConfig}, Timeout: time.Second}
	waitForTLS(t, client, url)
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	select {
	case err := <-result:
		if !errors.Is(err, controlpki.ErrExpired) {
			t.Fatalf("expiry result = %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("runtime stayed open after identity expiry")
	}
	if _, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
		t.Fatal("listener remained open after identity expiry")
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var one [1]byte
	if _, err := conn.Read(one[:]); err == nil {
		t.Fatal("existing connection remained open after identity expiry")
	} else {
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			t.Fatal("existing connection was not closed after identity expiry")
		}
	}
	if err := runtime.Serve(context.Background()); !errors.Is(err, controlpki.ErrExpired) {
		t.Fatalf("expired identity was accepted: %v", err)
	}
}

func loopbackEndpoint(t *testing.T) controlpki.Endpoint {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback unavailable: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: port}
}

func waitForTLS(t *testing.T, client *http.Client, url string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(url)
		if err == nil {
			_ = response.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("control runtime did not start")
}

func issueTestClient(t *testing.T, material controlpki.Material) tls.Certificate {
	t.Helper()
	block, _ := pem.Decode(material.CACert)
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	keyBlock, _ := pem.Decode(material.CAKey)
	parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: "test node"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, key.Public(), parsed.(ed25519.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
