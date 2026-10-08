package controlpki

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"net/netip"
	"time"
)

const ServerCertificateTTL = 30 * 24 * time.Hour

// ValidateForServerRotation allows only the old server leaf to be expired.
// Missing, malformed or mismatched committed material is never replaced.
func ValidateForServerRotation(material Material, endpoint Endpoint, pin string, now time.Time) error {
	if now.IsZero() {
		return errors.New("control certificate time is required")
	}
	if _, _, err := validateCA(material, pin, now, false); err != nil {
		return err
	}
	return Validate(material, endpoint, pin, now, true)
}

func validateCA(material Material, pin string, now time.Time, allowExpired bool) (*x509.Certificate, ed25519.PrivateKey, error) {
	ca, err := parseCertificate(material.CACert)
	if err != nil {
		return nil, nil, errors.New("invalid control CA certificate")
	}
	key, err := parseKey(material.CAKey)
	if err != nil || !keyMatches(ca, key) || Pin(ca) != pin {
		return nil, nil, errors.New("control CA pin or key mismatch")
	}
	if !ca.IsCA || !ca.BasicConstraintsValid || !ca.MaxPathLenZero || ca.MaxPathLen != 0 || ca.KeyUsage&x509.KeyUsageCertSign == 0 || len(ca.ExtKeyUsage) != 0 || len(ca.UnknownExtKeyUsage) != 0 || ca.SerialNumber.Sign() <= 0 || ca.CheckSignatureFrom(ca) != nil {
		return nil, nil, errors.New("invalid control CA constraints or signature")
	}
	if now.Before(ca.NotBefore) {
		return nil, nil, errors.New("control CA is not yet valid")
	}
	if !allowExpired && !now.Before(ca.NotAfter) {
		return nil, nil, ErrExpired
	}
	return ca, key, nil
}

// RotateServerLeaf signs a fresh server-only pair under the committed CA.
// It neither changes trust nor publishes any filesystem or runtime state.
func RotateServerLeaf(material Material, endpoint Endpoint, pin string, now time.Time) (Material, error) {
	if err := ValidateForServerRotation(material, endpoint, pin, now); err != nil {
		return Material{}, err
	}
	ca, caKey, err := validateCA(material, pin, now, false)
	if err != nil {
		return Material{}, err
	}
	old, err := parseCertificate(material.ServerCert)
	if err != nil {
		return Material{}, err
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Material{}, err
	}
	serial, err := randomSerial()
	if err != nil {
		return Material{}, err
	}
	for serial.Cmp(ca.SerialNumber) == 0 || serial.Cmp(old.SerialNumber) == 0 {
		serial, err = randomSerial()
		if err != nil {
			return Material{}, err
		}
	}
	end := now.UTC().Add(ServerCertificateTTL)
	if ca.NotAfter.Before(end) {
		end = ca.NotAfter
	}
	// X.509 validity has one-second precision.
	if !now.Before(end.Truncate(time.Second)) {
		return Material{}, ErrExpired
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "AWG-Forge control server"},
		NotBefore: now.UTC().Add(-5 * time.Minute), NotAfter: end,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if address, err := netip.ParseAddr(endpoint.Advertised); err == nil {
		template.IPAddresses = append(template.IPAddresses, address.AsSlice())
	} else {
		template.DNSNames = []string{endpoint.Advertised}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, key.Public(), caKey)
	if err != nil {
		return Material{}, errors.New("issue control server certificate failed")
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return Material{}, err
	}
	candidate := Material{
		CAKey: append([]byte(nil), material.CAKey...), CACert: append([]byte(nil), material.CACert...),
		ServerKey:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		ServerCert: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
	if err := Validate(candidate, endpoint, pin, now, false); err != nil {
		return Material{}, err
	}
	return candidate, nil
}
