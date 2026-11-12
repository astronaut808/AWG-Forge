package controlpki

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/netip"
	"time"
)

const (
	CAKeyFile      = "key.pem"
	CACertFile     = "cert.pem"
	ServerKeyFile  = "key.pem"
	ServerCertFile = "cert.pem"
)

var ErrExpired = errors.New("control identity certificate expired")

type Material struct {
	CAKey, CACert, ServerKey, ServerCert []byte
}

func Generate(endpoint Endpoint, now time.Time) (Material, string, error) {
	if now.IsZero() {
		return Material{}, "", errors.New("control certificate time is required")
	}
	now = now.UTC()
	_, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Material{}, "", err
	}
	caSerial, err := randomSerial()
	if err != nil {
		return Material{}, "", err
	}
	caTemplate := &x509.Certificate{
		SerialNumber: caSerial,
		Subject:      pkix.Name{CommonName: "AWG-Forge control CA"},
		NotBefore:    now.Add(-5 * time.Minute), NotAfter: now.AddDate(5, 0, 0),
		IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caKey.Public(), caKey)
	if err != nil {
		return Material{}, "", err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return Material{}, "", err
	}
	_, serverKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Material{}, "", err
	}
	serverSerial, err := randomSerial()
	if err != nil {
		return Material{}, "", err
	}
	for serverSerial.Cmp(caSerial) == 0 {
		serverSerial, err = randomSerial()
		if err != nil {
			return Material{}, "", err
		}
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: serverSerial,
		Subject:      pkix.Name{CommonName: "AWG-Forge control server"},
		NotBefore:    now.Add(-5 * time.Minute), NotAfter: now.Add(30 * 24 * time.Hour),
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if address, err := netip.ParseAddr(endpoint.Advertised); err == nil {
		serverTemplate.IPAddresses = append(serverTemplate.IPAddresses, address.AsSlice())
	} else {
		serverTemplate.DNSNames = []string{endpoint.Advertised}
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, ca, serverKey.Public(), caKey)
	if err != nil {
		return Material{}, "", err
	}
	caKeyDER, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		return Material{}, "", err
	}
	serverKeyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		return Material{}, "", err
	}
	material := Material{
		CAKey:      pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: caKeyDER}),
		CACert:     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		ServerKey:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: serverKeyDER}),
		ServerCert: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}),
	}
	pin := Pin(ca)
	if err := Validate(material, endpoint, pin, now, false); err != nil {
		return Material{}, "", err
	}
	return material, pin, nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	for {
		serial, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return nil, err
		}
		if serial.Sign() > 0 {
			return serial, nil
		}
	}
}

func Pin(certificate *x509.Certificate) string {
	sum := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Validate checks a committed identity. allowExpired is only for archive
// recovery and intact expired-leaf rotation; live validation uses false.
func Validate(material Material, endpoint Endpoint, expectedPin string, now time.Time, allowExpired bool) error {
	if now.IsZero() {
		return errors.New("control certificate time is required")
	}
	ca, _, err := validateCA(material, expectedPin, now, allowExpired)
	if err != nil {
		return err
	}
	leaf, err := parseCertificate(material.ServerCert)
	if err != nil {
		return fmt.Errorf("invalid control server certificate: %w", err)
	}
	leafKey, err := parseKey(material.ServerKey)
	if err != nil {
		return errors.New("invalid control server key")
	}
	if !keyMatches(leaf, leafKey) {
		return errors.New("control certificate pin or private key mismatch")
	}
	if leaf.IsCA || !leaf.BasicConstraintsValid || leaf.KeyUsage != x509.KeyUsageDigitalSignature || len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth || len(leaf.UnknownExtKeyUsage) != 0 {
		return errors.New("invalid control server certificate usage")
	}
	if ca.SerialNumber.Sign() <= 0 || leaf.SerialNumber.Sign() <= 0 || ca.SerialNumber.Cmp(leaf.SerialNumber) == 0 {
		return errors.New("invalid control certificate serial")
	}
	if len(leaf.EmailAddresses) != 0 || len(leaf.URIs) != 0 {
		return errors.New("control server certificate has extra SAN entries")
	}
	if !hasExactSAN(leaf, endpoint.Advertised) {
		return errors.New("control server certificate has unexpected SAN entries")
	}
	if address, err := netip.ParseAddr(endpoint.Advertised); err == nil {
		if len(leaf.DNSNames) != 0 || len(leaf.IPAddresses) != 1 || !bytes.Equal(leaf.IPAddresses[0], address.AsSlice()) {
			return errors.New("control server IP SAN mismatch")
		}
	} else if len(leaf.IPAddresses) != 0 || len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != endpoint.Advertised {
		return errors.New("control server DNS SAN mismatch")
	}
	verifyAt := now.UTC()
	expired := !verifyAt.Before(leaf.NotAfter) || !verifyAt.Before(ca.NotAfter)
	if expired {
		verifyAt = leaf.NotAfter
		if ca.NotAfter.Before(verifyAt) {
			verifyAt = ca.NotAfter
		}
		verifyAt = verifyAt.Add(-time.Second)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: endpoint.Advertised, CurrentTime: verifyAt, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return errors.New("control server certificate chain or validity failed")
	}
	if expired && !allowExpired {
		return ErrExpired
	}
	return nil
}

func parseCertificate(data []byte) (*x509.Certificate, error) {
	block, trailing := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(trailing)) != 0 {
		return nil, errors.New("invalid PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseKey(data []byte) (ed25519.PrivateKey, error) {
	block, trailing := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(trailing)) != 0 {
		return nil, errors.New("invalid PEM key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("control key must be Ed25519")
	}
	return key, nil
}

func keyMatches(certificate *x509.Certificate, key ed25519.PrivateKey) bool {
	public, ok := certificate.PublicKey.(ed25519.PublicKey)
	return ok && bytes.Equal(public, key.Public().(ed25519.PublicKey))
}

func hasExactSAN(certificate *x509.Certificate, advertised string) bool {
	var expectedTag int
	var expected []byte
	if address, err := netip.ParseAddr(advertised); err == nil {
		expectedTag = 7 // iPAddress
		expected = address.AsSlice()
	} else {
		expectedTag = 2 // dNSName
		expected = []byte(advertised)
	}
	count := 0
	for _, extension := range certificate.Extensions {
		if !extension.Id.Equal(asn1.ObjectIdentifier{2, 5, 29, 17}) {
			continue
		}
		count++
		var sequence asn1.RawValue
		trailing, err := asn1.Unmarshal(extension.Value, &sequence)
		if err != nil || len(trailing) != 0 || sequence.Class != asn1.ClassUniversal || sequence.Tag != asn1.TagSequence || !sequence.IsCompound {
			return false
		}
		var name asn1.RawValue
		trailing, err = asn1.Unmarshal(sequence.Bytes, &name)
		if err != nil || len(trailing) != 0 || name.Class != asn1.ClassContextSpecific || name.Tag != expectedTag || name.IsCompound || !bytes.Equal(name.Bytes, expected) {
			return false
		}
	}
	return count == 1
}
