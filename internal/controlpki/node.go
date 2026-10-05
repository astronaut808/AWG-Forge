package controlpki

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"time"
)

const (
	MaxCSRBytes        = 16 << 10
	NodeCertificateTTL = 30 * 24 * time.Hour
)

// NodeCertificate contains only public material. The node's private key never
// enters the controller; node identity is supplied by the registry, not the CSR.
type NodeCertificate struct {
	DER                 []byte
	CSRHash             [32]byte
	CertificateHash     [32]byte
	PublicKeyHash       [32]byte
	Serial              string
	NotBefore, NotAfter time.Time
}

// ParseNodeCSR accepts a signed, extension-free Ed25519 request. No identity
// field from a CSR is copied into the issued certificate.
func ParseNodeCSR(der []byte) (*x509.CertificateRequest, [32]byte, error) {
	if len(der) == 0 || len(der) > MaxCSRBytes {
		return nil, [32]byte{}, errors.New("invalid node CSR size")
	}
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil || csr.CheckSignature() != nil || csr.PublicKeyAlgorithm != x509.Ed25519 {
		return nil, [32]byte{}, errors.New("invalid node CSR signature or key")
	}
	key, ok := csr.PublicKey.(ed25519.PublicKey)
	if !ok || len(key) != ed25519.PublicKeySize || len(csr.Extensions) != 0 ||
		len(csr.ExtraExtensions) != 0 ||
		len(csr.DNSNames) != 0 || len(csr.IPAddresses) != 0 ||
		len(csr.EmailAddresses) != 0 || len(csr.URIs) != 0 ||
		!bytes.Equal(csr.RawSubject, []byte{0x30, 0x00}) {
		return nil, [32]byte{}, errors.New("node CSR contains unsupported fields")
	}
	return csr, sha256.Sum256(der), nil
}

// IssueNodeCertificate signs public CSR material with the prepared control CA.
// Callers must commit the certificate registry row before returning it to a node.
func IssueNodeCertificate(material Material, csrDER []byte, now time.Time) (NodeCertificate, error) {
	csr, digest, err := ParseNodeCSR(csrDER)
	if err != nil {
		return NodeCertificate{}, err
	}
	if now.IsZero() {
		return NodeCertificate{}, errors.New("node certificate time is required")
	}
	now = now.UTC()
	ca, err := parseCertificate(material.CACert)
	if err != nil {
		return NodeCertificate{}, errors.New("invalid control CA certificate")
	}
	key, err := parseKey(material.CAKey)
	if err != nil || !keyMatches(ca, key) || !ca.IsCA || !ca.BasicConstraintsValid ||
		ca.KeyUsage&x509.KeyUsageCertSign == 0 || len(ca.ExtKeyUsage) != 0 ||
		ca.CheckSignatureFrom(ca) != nil || now.Before(ca.NotBefore) ||
		!now.Add(24*time.Hour).Before(ca.NotAfter) {
		return NodeCertificate{}, errors.New("control CA cannot issue node certificates")
	}
	serial, err := randomSerial()
	if err != nil {
		return NodeCertificate{}, err
	}
	for serial.Cmp(ca.SerialNumber) == 0 {
		serial, err = randomSerial()
		if err != nil {
			return NodeCertificate{}, err
		}
	}
	end := now.Add(NodeCertificateTTL)
	if ca.NotAfter.Before(end) {
		end = ca.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "AWG-Forge control node"},
		NotBefore:    now.Add(-5 * time.Minute), NotAfter: end,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, csr.PublicKey, key)
	if err != nil {
		return NodeCertificate{}, errors.New("issue node certificate failed")
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return NodeCertificate{}, errors.New("issued node certificate is invalid")
	}
	publicHash := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	if !bytes.Equal(certificate.RawSubjectPublicKeyInfo, csr.RawSubjectPublicKeyInfo) {
		return NodeCertificate{}, errors.New("issued node certificate key mismatch")
	}
	return NodeCertificate{
		DER: der, CSRHash: digest, CertificateHash: sha256.Sum256(der),
		PublicKeyHash: publicHash, Serial: serial.String(),
		NotBefore: certificate.NotBefore, NotAfter: certificate.NotAfter,
	}, nil
}
