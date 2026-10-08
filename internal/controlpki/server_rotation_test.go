package controlpki

import (
	"bytes"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
	"time"
)

func TestServerLeafRotationContract(t *testing.T) {
	for _, host := range []string{"control.example.com", "127.0.0.1", "::1"} {
		t.Run(host, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			endpoint := Endpoint{BindIP: "127.0.0.1", Advertised: host, Port: 8443}
			old, pin, err := Generate(endpoint, now.Add(-31*24*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			ca, _ := parseCertificate(old.CACert)
			oldLeaf, _ := parseCertificate(old.ServerCert)
			candidate, err := RotateServerLeaf(old, endpoint, pin, now)
			if err != nil {
				t.Fatal(err)
			}
			leaf, _ := parseCertificate(candidate.ServerCert)
			if !bytes.Equal(old.CAKey, candidate.CAKey) || !bytes.Equal(old.CACert, candidate.CACert) || Pin(ca) != pin {
				t.Fatal("rotation changed CA")
			}
			if bytes.Equal(old.ServerKey, candidate.ServerKey) || bytes.Equal(oldLeaf.RawSubjectPublicKeyInfo, leaf.RawSubjectPublicKeyInfo) || oldLeaf.SerialNumber.Cmp(leaf.SerialNumber) == 0 || leaf.SerialNumber.Sign() <= 0 {
				t.Fatal("rotation reused key or serial")
			}
			if leaf.IsCA || leaf.KeyUsage != x509.KeyUsageDigitalSignature || len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth || !hasExactSAN(leaf, host) {
				t.Fatal("wrong leaf profile")
			}
			if !leaf.NotAfter.Equal(now.Add(ServerCertificateTTL)) || !leaf.NotBefore.Equal(now.Add(-5*time.Minute)) {
				t.Fatal("wrong lifetime")
			}
			if err := Validate(candidate, endpoint, pin, now, false); err != nil {
				t.Fatal(err)
			}
			if err := Validate(old, endpoint, pin, now, false); !errors.Is(err, ErrExpired) {
				t.Fatal("old leaf not expired")
			}
		})
	}
}

func TestServerRotationRejectsDamagedTrustAndOldPair(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	endpoint := Endpoint{BindIP: "127.0.0.1", Advertised: "control.example.com", Port: 8443}
	old, pin, err := Generate(endpoint, now)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := Generate(endpoint, now)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		edit func(*Material)
		pin  string
		at   time.Time
	}{
		{"missing CA key", func(m *Material) { m.CAKey = nil }, pin, now},
		{"corrupt CA cert", func(m *Material) { m.CACert = []byte("broken") }, pin, now},
		{"wrong CA key", func(m *Material) { m.CAKey = other.CAKey }, pin, now},
		{"missing server key", func(m *Material) { m.ServerKey = nil }, pin, now},
		{"corrupt old leaf", func(m *Material) { m.ServerCert = []byte("broken") }, pin, now},
		{"wrong old key", func(m *Material) { m.ServerKey = other.ServerKey }, pin, now},
		{"wrong chain", func(m *Material) { m.ServerCert = other.ServerCert; m.ServerKey = other.ServerKey }, pin, now},
		{"wrong pin", func(*Material) {}, "sha256:invalid", now},
		{"expired CA", func(*Material) {}, pin, now.AddDate(5, 0, 0)},
		{"not yet valid", func(*Material) {}, pin, now.Add(-time.Hour)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := old
			tt.edit(&m)
			if _, err := RotateServerLeaf(m, endpoint, tt.pin, tt.at); err == nil {
				t.Fatal("unsafe rotation accepted")
			}
		})
	}
	endpoint.Advertised = "other.example.com"
	if _, err := RotateServerLeaf(old, endpoint, pin, now); err == nil {
		t.Fatal("wrong SAN accepted")
	}
}

func TestServerRotationCapsAtCAExpiry(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	endpoint := Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 8443}
	old, pin, err := Generate(endpoint, now)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := parseCertificate(old.CACert)
	key, _ := parseKey(old.CAKey)
	ca.NotAfter = now.Add(10 * time.Second)
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	old.CACert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	candidate, err := RotateServerLeaf(old, endpoint, pin, now)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := parseCertificate(candidate.ServerCert)
	if !leaf.NotAfter.Equal(ca.NotAfter) {
		t.Fatal("leaf outlives CA")
	}
	if _, err := RotateServerLeaf(candidate, endpoint, pin, ca.NotAfter); !errors.Is(err, ErrExpired) {
		t.Fatalf("CA expiry boundary: %v", err)
	}
}
