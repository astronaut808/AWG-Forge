package controlpki

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"testing"
	"time"
)

func TestIssueNodeCertificateValidatesSignedCSRAndUsage(t *testing.T) {
	now := time.Now().UTC()
	material, _, err := Generate(Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 8443}, now)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := IssueNodeCertificate(material, csrDER, now)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(issued.DER)
	if err != nil {
		t.Fatal(err)
	}
	caBlock, _ := pem.Decode(material.CACert)
	ca, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := certificate.CheckSignatureFrom(ca); err != nil {
		t.Fatal(err)
	}
	if _, ok := certificate.PublicKey.(ed25519.PublicKey); !ok ||
		certificate.IsCA || certificate.KeyUsage != x509.KeyUsageDigitalSignature ||
		len(certificate.ExtKeyUsage) != 1 || certificate.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth ||
		len(certificate.DNSNames) != 0 || len(certificate.IPAddresses) != 0 ||
		!certificate.NotAfter.Equal(now.Add(NodeCertificateTTL).Truncate(time.Second)) {
		t.Fatalf("unexpected node certificate profile: %+v", certificate)
	}
	if _, _, err := ParseNodeCSR(nil); err == nil {
		t.Fatal("empty CSR accepted")
	}
	if _, _, err := ParseNodeCSR(make([]byte, MaxCSRBytes+1)); err == nil {
		t.Fatal("oversized CSR accepted")
	}
	invalid := append([]byte(nil), csrDER...)
	invalid[len(invalid)-1] ^= 1
	if _, _, err := ParseNodeCSR(invalid); err == nil {
		t.Fatal("tampered CSR accepted")
	}
	csrWithIdentity, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "forged-node"}, DNSNames: []string{"example.com"},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseNodeCSR(csrWithIdentity); err == nil {
		t.Fatal("identity-bearing CSR accepted")
	}
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecdsaCSR, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, ecdsaKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseNodeCSR(ecdsaCSR); err == nil {
		t.Fatal("unsupported key type accepted")
	}
	if _, err := IssueNodeCertificate(material, csrDER, ca.NotAfter.Add(-12*time.Hour)); err == nil {
		t.Fatal("near-expired CA issued a node certificate")
	}
}
