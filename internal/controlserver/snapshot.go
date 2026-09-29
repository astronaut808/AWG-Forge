package controlserver

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
)

// Pair and deadline are immutable and always published together under mu.
type certificateSnapshot struct {
	certificate tls.Certificate
	expiresAt   time.Time
}

func makeCertificateSnapshot(material controlpki.Material, endpoint controlpki.Endpoint, pin string) (*certificateSnapshot, error) {
	if err := controlpki.Validate(material, endpoint, pin, time.Now(), false); err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(material.ServerCert, material.ServerKey)
	if err != nil {
		return nil, errors.New("invalid control server key pair")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, errors.New("invalid control server certificate")
	}
	block, _ := pem.Decode(material.CACert)
	if block == nil {
		return nil, errors.New("invalid control CA certificate")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.New("invalid control CA certificate")
	}
	expiry := leaf.NotAfter
	if ca.NotAfter.Before(expiry) {
		expiry = ca.NotAfter
	}
	pair.Leaf = leaf
	return &certificateSnapshot{certificate: pair, expiresAt: expiry}, nil
}

func (runtime *Runtime) getCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed || !time.Now().Before(runtime.snapshot.expiresAt) {
		return nil, controlpki.ErrExpired
	}
	return &runtime.snapshot.certificate, nil
}

func (runtime *Runtime) identityValid(now time.Time) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return !runtime.closed && now.Before(runtime.snapshot.expiresAt)
}

// Reload validates a candidate before publication. Invalid pre-commit candidates
// leave the active runtime untouched. The caller must use ReloadCommitted once
// state.json has switched; no production runtime owner exists yet.
func (runtime *Runtime) Reload(material controlpki.Material) error {
	snapshot, err := makeCertificateSnapshot(material, runtime.endpoint, runtime.pin)
	if err != nil {
		return err
	}
	if !bytes.Equal(material.CACert, runtime.caPEM) {
		return errors.New("control CA rotation is not supported")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed {
		return errors.New("control runtime is closed")
	}
	if !time.Now().Before(snapshot.expiresAt) {
		return controlpki.ErrExpired
	}
	runtime.snapshot = snapshot
	// All connections registered before publication, including incomplete
	// handshakes, close before success. Accept registers under the same lock.
	runtime.closeConnectionsLocked()
	select {
	case runtime.changed <- struct{}{}:
	default:
	}
	return nil
}

// ReloadCommitted fails closed on any publication error. The predecessor must
// never remain available after an uncertain or durable state commit.
func (runtime *Runtime) ReloadCommitted(material controlpki.Material) error {
	if err := runtime.Reload(material); err != nil {
		runtime.Close()
		return err
	}
	return nil
}

// Close permanently closes admission and all accepted sockets, including TLS
// handshakes not yet registered by net/http. A restart needs a new Runtime.
func (runtime *Runtime) Close() {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.closeLocked(nil)
}

func (runtime *Runtime) closeLocked(reason error) {
	if runtime.closed {
		return
	}
	runtime.closed = true
	runtime.closeReason = reason
	if runtime.listener != nil {
		_ = runtime.listener.Close()
	}
	runtime.closeConnectionsLocked()
	select {
	case runtime.changed <- struct{}{}:
	default:
	}
}

func (runtime *Runtime) closeConnectionsLocked() {
	for conn := range runtime.connections {
		_ = conn.Conn.Close()
		delete(runtime.connections, conn)
	}
}

type trackingListener struct {
	net.Listener
	runtime *Runtime
}

func (listener *trackingListener) Accept() (net.Conn, error) {
	conn, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	runtime := listener.runtime
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed || !time.Now().Before(runtime.snapshot.expiresAt) {
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	tracked := &trackedConnection{Conn: conn, runtime: runtime}
	runtime.connections[tracked] = struct{}{}
	return tracked, nil
}

type trackedConnection struct {
	net.Conn
	runtime *Runtime
}

func (conn *trackedConnection) Close() error {
	err := conn.Conn.Close()
	conn.runtime.mu.Lock()
	delete(conn.runtime.connections, conn)
	conn.runtime.mu.Unlock()
	return err
}
