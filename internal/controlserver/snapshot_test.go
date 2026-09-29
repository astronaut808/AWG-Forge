package controlserver

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
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
)

func testLeafDeadline(t *testing.T, m controlpki.Material, end time.Time) controlpki.Material {
	t.Helper()
	b, _ := pem.Decode(m.CACert)
	ca, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = pem.Decode(m.CAKey)
	parsed, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = pem.Decode(m.ServerCert)
	leaf, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	leaf.NotAfter = end
	b, _ = pem.Decode(m.ServerKey)
	key, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, key.(ed25519.PrivateKey).Public(), parsed)
	if err != nil {
		t.Fatal(err)
	}
	m.ServerCert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return m
}

func startSnapshotRuntime(t *testing.T, m controlpki.Material, endpoint controlpki.Endpoint, pin string) (*Runtime, *tls.Config, string, func()) {
	t.Helper()
	runtime, err := New(m, endpoint, pin, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(m.CACert) {
		t.Fatal("CA")
	}
	clientTLS := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: endpoint.Advertised, VerifyConnection: func(cs tls.ConnectionState) error {
		chain := cs.VerifiedChains[0]
		if controlpki.Pin(chain[len(chain)-1]) != pin {
			return errors.New("pin mismatch")
		}
		return nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Serve(ctx) }()
	address := net.JoinHostPort(endpoint.BindIP, strconv.Itoa(endpoint.Port))
	url := "https://" + address + "/control/v1/no-route"
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS.Clone()}, Timeout: time.Second}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			runtime.Close()
			select {
			case err := <-done:
				if err != nil && !errors.Is(err, controlpki.ErrExpired) {
					t.Error(err)
				}
			case <-time.After(3 * time.Second):
				t.Error("runtime did not terminate")
			}
			client.CloseIdleConnections()
		})
	}
	t.Cleanup(stop)
	waitForTLS(t, client, url)
	return runtime, clientTLS, address, stop
}

func TestSnapshotRealReloadWithoutSNIAndWithDNS(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "control.example.com"} {
		t.Run(host, func(t *testing.T) {
			endpoint := loopbackEndpoint(t)
			endpoint.Advertised = host
			now := time.Now().UTC()
			old, pin, err := controlpki.Generate(endpoint, now)
			if err != nil {
				t.Fatal(err)
			}
			old = testLeafDeadline(t, old, time.Now().Add(3*time.Second))
			runtime, clientTLS, address, _ := startSnapshotRuntime(t, old, endpoint, pin)
			if len(runtime.tlsConfig.Certificates) != 0 || runtime.tlsConfig.GetCertificate == nil {
				t.Fatal("SNI-free fallback can select a fixed leaf")
			}
			conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, clientTLS)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			oldDER := conn.ConnectionState().PeerCertificates[0].Raw
			bad := old
			bad.ServerKey = []byte("invalid")
			if err := runtime.Reload(bad); err == nil {
				t.Fatal("invalid candidate accepted")
			}
			unchanged, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, clientTLS)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(unchanged.ConnectionState().PeerCertificates[0].Raw, oldDER) {
				t.Fatal("candidate failure disturbed current snapshot")
			}
			_ = unchanged.Close()
			candidate, err := controlpki.RotateServerLeaf(old, endpoint, pin, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err := runtime.Reload(candidate); err != nil {
				t.Fatal(err)
			}
			assertSnapshotConnectionClosed(t, conn)
			// Passing the predecessor's real X.509 deadline must not close the new leaf.
			block, _ := pem.Decode(old.ServerCert)
			leaf, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Until(leaf.NotAfter) + 100*time.Millisecond)
			replacement, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, clientTLS)
			if err != nil {
				t.Fatal("predecessor timer closed replacement", err)
			}
			defer func() { _ = replacement.Close() }()
			block, _ = pem.Decode(candidate.ServerCert)
			if !bytes.Equal(replacement.ConnectionState().PeerCertificates[0].Raw, block.Bytes) {
				t.Fatal("new handshake did not see new complete leaf")
			}
		})
	}
}

func TestSnapshotConcurrentAcceptReloadAndClose(t *testing.T) {
	endpoint := loopbackEndpoint(t)
	old, pin, err := controlpki.Generate(endpoint, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	runtime, clientTLS, address, stop := startSnapshotRuntime(t, old, endpoint, pin)
	var wg sync.WaitGroup
	var completed atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 12; j++ {
				conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, clientTLS)
				if err == nil {
					completed.Add(1)
					_ = conn.Close()
				}
			}
		}()
	}
	current := old
	for i := 0; i < 8; i++ {
		candidate, err := controlpki.RotateServerLeaf(current, endpoint, pin, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := runtime.Reload(candidate); err != nil {
			t.Fatal(err)
		}
		current = candidate
	}
	wg.Wait()
	if completed.Load() == 0 {
		t.Fatal("no concurrent handshake completed")
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(current.ServerCert)
	if !bytes.Equal(conn.ConnectionState().PeerCertificates[0].Raw, block.Bytes) {
		t.Fatal("final snapshot differs")
	}
	wg.Add(3)
	for i := 0; i < 3; i++ {
		go func() { defer wg.Done(); runtime.Close() }()
	}
	wg.Wait()
	assertSnapshotConnectionClosed(t, conn)
	_ = conn.Close()
	stop()
	if err := runtime.Reload(current); err == nil {
		t.Fatal("closed runtime reopened")
	}
}

func TestSnapshotCommittedInvalidPublicationClosesAdmission(t *testing.T) {
	endpoint := loopbackEndpoint(t)
	old, pin, err := controlpki.Generate(endpoint, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	runtime, clientTLS, address, stop := startSnapshotRuntime(t, old, endpoint, pin)
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	// Different committed trust also fails closed; no CA update is allowed here.
	other, _, err := controlpki.Generate(endpoint, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ReloadCommitted(other); err == nil {
		t.Fatal("different CA published")
	}
	assertSnapshotConnectionClosed(t, conn)
	stop()
	if c, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
		_ = c.Close()
		t.Fatal("closed runtime still accepts")
	}
}

func TestSnapshotRealExpiryClosesRawHandshakeAndTLSConnection(t *testing.T) {
	for _, boundary := range []string{"leaf", "ca"} {
		t.Run(boundary, func(t *testing.T) {
			endpoint := loopbackEndpoint(t)
			m, pin, err := controlpki.Generate(endpoint, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(2 * time.Second)
			if boundary == "leaf" {
				m = testLeafDeadline(t, m, deadline)
			} else {
				block, _ := pem.Decode(m.CACert)
				ca, err := x509.ParseCertificate(block.Bytes)
				if err != nil {
					t.Fatal(err)
				}
				block, _ = pem.Decode(m.CAKey)
				key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
				if err != nil {
					t.Fatal(err)
				}
				ca.NotAfter = deadline
				der, err := x509.CreateCertificate(rand.Reader, ca, ca, key.(ed25519.PrivateKey).Public(), key)
				if err != nil {
					t.Fatal(err)
				}
				m.CACert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
			}
			runtime, clientTLS, address, stop := startSnapshotRuntime(t, m, endpoint, pin)
			conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, clientTLS)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			raw, err := net.DialTimeout("tcp", address, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = raw.Close() }()
			cert := m.ServerCert
			if boundary == "ca" {
				cert = m.CACert
			}
			block, _ := pem.Decode(cert)
			leaf, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Until(leaf.NotAfter) + 100*time.Millisecond)
			if _, err := runtime.getCertificate(nil); !errors.Is(err, controlpki.ErrExpired) {
				t.Fatal("expired handshake accepted", err)
			}
			assertSnapshotConnectionClosed(t, conn)
			assertSnapshotConnectionClosed(t, raw)
			stop()
			if c, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
				_ = c.Close()
				t.Fatal("expiry left listener open")
			}
		})
	}
}

func assertSnapshotConnectionClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	_, err := conn.Read(b[:])
	var timeout net.Error
	if err == nil || (errors.As(err, &timeout) && timeout.Timeout()) {
		t.Fatal("old connection remained open")
	}
}

type snapshotAuthorizer struct{}

func (snapshotAuthorizer) Authorize(context.Context, *x509.Certificate, string) (NodeIdentity, error) {
	return NodeIdentity{ControllerID: "controller", NodeID: "node", BindingEpoch: 1}, nil
}

func TestSnapshotCancellationStillClosesAtRealExpiry(t *testing.T) {
	endpoint := loopbackEndpoint(t)
	m, pin, err := controlpki.Generate(endpoint, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	m = testLeafDeadline(t, m, time.Now().Add(2*time.Second))
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	runtime, err := New(m, endpoint, pin, snapshotAuthorizer{}, []Route{{ID: "node.hold", Method: "GET", Path: "/control/v1/hold", Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusNoContent)
	})}})
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(m.CACert) {
		t.Fatal("CA")
	}
	clientTLS := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: endpoint.Advertised, Certificates: []tls.Certificate{issueTestClient(t, m)}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Serve(ctx) }()
	address := net.JoinHostPort(endpoint.BindIP, strconv.Itoa(endpoint.Port))
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS.Clone()}, Timeout: time.Second}
	t.Cleanup(func() { cancel(); runtime.Close(); client.CloseIdleConnections() })
	waitForTLS(t, client, "https://"+address+"/control/v1/no-route")
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("GET /control/v1/hold HTTP/1.1\r\nHost: " + endpoint.Advertised + "\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, controlpki.ErrExpired) {
			t.Fatalf("expiry during graceful shutdown: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("graceful shutdown outlived expiry")
	}
	assertSnapshotConnectionClosed(t, conn)
}
