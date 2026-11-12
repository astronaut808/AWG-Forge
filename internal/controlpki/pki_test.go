package controlpki

import (
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"
)

func TestGeneratedIdentityValidationAndSerialUniqueness(t *testing.T) {
	endpoint, err := NormalizeEndpoint("127.0.0.1", "control.example.com", 8443, 51821)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		material, pin, err := Generate(endpoint, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := Validate(material, endpoint, pin, now, false); err != nil {
			t.Fatal(err)
		}
		for _, certPEM := range [][]byte{material.CACert, material.ServerCert} {
			cert, err := parseCertificate(certPEM)
			if err != nil {
				t.Fatal(err)
			}
			serial := cert.SerialNumber.String()
			if seen[serial] || cert.SerialNumber.Sign() <= 0 {
				t.Fatalf("duplicate or non-positive certificate serial: %s", serial)
			}
			seen[serial] = true
		}
	}
}

func TestGeneratedIdentityHasExactIPSAN(t *testing.T) {
	for _, advertised := range []string{"192.0.2.10", "2001:db8::10"} {
		t.Run(advertised, func(t *testing.T) {
			endpoint, err := NormalizeEndpoint("127.0.0.1", advertised, 8443, 51821)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			material, pin, err := Generate(endpoint, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(material, endpoint, pin, now, false); err != nil {
				t.Fatalf("validate IP SAN: %v", err)
			}
			wrong := endpoint
			wrong.Advertised = "192.0.2.11"
			if err := Validate(material, wrong, pin, now, false); err == nil {
				t.Fatal("mismatched IP SAN was accepted")
			}
		})
	}
}

func TestValidationRejectsMismatchesAndExpiredLiveIdentity(t *testing.T) {
	endpoint, _ := NormalizeEndpoint("127.0.0.1", "control.example.com", 8443, 51821)
	now := time.Now().UTC()
	material, pin, err := Generate(endpoint, now)
	if err != nil {
		t.Fatal(err)
	}
	other, otherPin, err := Generate(endpoint, now)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		material Material
		endpoint Endpoint
		pin      string
		now      time.Time
	}{
		{"wrong pin", material, endpoint, otherPin, now},
		{"wrong CA key", Material{other.CAKey, material.CACert, material.ServerKey, material.ServerCert}, endpoint, pin, now},
		{"wrong server key", Material{material.CAKey, material.CACert, other.ServerKey, material.ServerCert}, endpoint, pin, now},
		{"wrong chain", Material{other.CAKey, other.CACert, material.ServerKey, material.ServerCert}, endpoint, otherPin, now},
		{"wrong SAN", material, Endpoint{BindIP: endpoint.BindIP, Advertised: "other.example.com", Port: endpoint.Port}, pin, now},
		{"expired", material, endpoint, pin, now.Add(31 * 24 * time.Hour)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Validate(tt.material, tt.endpoint, tt.pin, tt.now, false); err == nil {
				t.Fatal("invalid identity was accepted")
			}
		})
	}
	if err := Validate(material, endpoint, pin, now.Add(31*24*time.Hour), true); err != nil {
		t.Fatalf("expired but consistent backup was rejected: %v", err)
	}
	if err := Validate(material, endpoint, pin, now.Add(31*24*time.Hour), false); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired identity diagnostic = %v", err)
	}
}

func TestValidationRejectsLeafClientUsageAndCADelegation(t *testing.T) {
	endpoint, _ := NormalizeEndpoint("127.0.0.1", "control.example.com", 8443, 51821)
	now := time.Now().UTC()
	material, pin, err := Generate(endpoint, now)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := parseCertificate(material.CACert)
	caKey, _ := parseKey(material.CAKey)
	serverKey, _ := parseKey(material.ServerKey)
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(777), Subject: pkix.Name{CommonName: "control server"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		DNSNames:    []string{endpoint.Advertised},
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, serverKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	material.ServerCert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := Validate(material, endpoint, pin, now, false); err == nil {
		t.Fatal("client-auth server leaf was accepted")
	}
	material, _, err = Generate(endpoint, now)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ = parseCertificate(material.CACert)
	caKey, _ = parseKey(material.CAKey)
	caTemplate := &x509.Certificate{
		SerialNumber: ca.SerialNumber, Subject: ca.Subject,
		NotBefore: ca.NotBefore, NotAfter: ca.NotAfter,
		IsCA: true, BasicConstraintsValid: true, MaxPathLen: 1,
		KeyUsage: x509.KeyUsageCertSign,
	}
	der, err = x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	material.CACert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := Validate(material, endpoint, PinMustParse(t, material.CACert), now, false); err == nil {
		t.Fatal("delegating CA was accepted")
	}
}

func PinMustParse(t *testing.T, certPEM []byte) string {
	t.Helper()
	cert, err := parseCertificate(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	return Pin(cert)
}

func TestValidationRejectsUnknownAdditionalSAN(t *testing.T) {
	endpoint, _ := NormalizeEndpoint("127.0.0.1", "control.example.com", 8443, 51821)
	now := time.Now().UTC()
	material, pin, err := Generate(endpoint, now)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := parseCertificate(material.CACert)
	caKey, _ := parseKey(material.CAKey)
	serverKey, _ := parseKey(material.ServerKey)
	san, err := asn1.Marshal([]asn1.RawValue{
		{Class: asn1.ClassContextSpecific, Tag: 2, Bytes: []byte(endpoint.Advertised)},
		{Class: asn1.ClassContextSpecific, Tag: 8, Bytes: []byte{0x2a}},
	})
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(999), Subject: pkix.Name{CommonName: "control server"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		ExtraExtensions: []pkix.Extension{{Id: asn1.ObjectIdentifier{2, 5, 29, 17}, Value: san}},
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, serverKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	material.ServerCert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := Validate(material, endpoint, pin, now, false); err == nil {
		t.Fatal("leaf with additional registeredID SAN was accepted")
	}
}
