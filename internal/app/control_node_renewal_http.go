package app

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/controlserver"
	"github.com/astronaut808/awg-forge/internal/sqldb"
)

func (s *Service) nodeCertificateRenewalHTTP(w http.ResponseWriter, r *http.Request) {
	var request controlapi.CertificateRenewalRequest
	if readControlJSON(w, r, &request) != nil || !validControlUUID(request.BootID) ||
		len(request.CurrentSerial) == 0 || len(request.CurrentSerial) > 128 || len(request.CSRPEM) == 0 || len(request.CSRPEM) > 8192 {
		controlRenewalProblem(w, http.StatusBadRequest, "invalid_renewal")
		return
	}
	block, rest := pem.Decode([]byte(request.CSRPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(rest) != 0 {
		controlRenewalProblem(w, http.StatusBadRequest, "invalid_renewal")
		return
	}
	peer := renewalPeer(r)
	if peer == nil || peer.SerialNumber == nil || request.CurrentSerial != peer.SerialNumber.String() {
		controlRenewalProblem(w, http.StatusBadRequest, "invalid_renewal")
		return
	}
	csr, _, err := controlpki.ParseNodeCSR(block.Bytes)
	if err != nil || bytes.Equal(csr.RawSubjectPublicKeyInfo, peer.RawSubjectPublicKeyInfo) {
		controlRenewalProblem(w, http.StatusBadRequest, "invalid_renewal")
		return
	}
	if err := s.lockControlRequest(r.Context()); err != nil {
		controlRenewalProblem(w, http.StatusServiceUnavailable, "control_unavailable")
		return
	}
	defer s.unlockStateMutation()
	if _, ok := controlserver.IdentityFromContext(r.Context()); !ok {
		controlRenewalProblem(w, http.StatusForbidden, "node_denied")
		return
	}
	now := time.Now().UTC()
	if now.Before(peer.NotBefore.Add(peer.NotAfter.Sub(peer.NotBefore) * 2 / 3)) {
		controlRenewalProblem(w, http.StatusConflict, "renewal_not_due")
		return
	}
	issuedPEM, err := s.renewNodeCertificateLocked(r.Context(), peer, block.Bytes, now, true)
	if err != nil {
		switch {
		case errors.Is(err, sqldb.ErrNodeCertificateConflict):
			controlRenewalProblem(w, http.StatusConflict, "renewal_conflict")
		case errors.Is(err, sqldb.ErrNodeCertificateDenied):
			controlRenewalProblem(w, http.StatusForbidden, "node_denied")
		default:
			controlRenewalProblem(w, http.StatusServiceUnavailable, "control_unavailable")
		}
		return
	}
	certificateBlock, _ := pem.Decode(issuedPEM)
	if certificateBlock == nil {
		controlRenewalProblem(w, http.StatusServiceUnavailable, "control_unavailable")
		return
	}
	certificate, err := x509.ParseCertificate(certificateBlock.Bytes)
	if err != nil {
		controlRenewalProblem(w, http.StatusServiceUnavailable, "control_unavailable")
		return
	}
	state, err := s.store.Load()
	if err != nil || state.Controller == nil || state.Controller.Control == nil {
		controlRenewalProblem(w, http.StatusServiceUnavailable, "control_unavailable")
		return
	}
	material, err := s.store.LoadControlIdentity(state.Controller.Control.CAGeneration, state.Controller.Control.ServerGeneration)
	if err != nil {
		controlRenewalProblem(w, http.StatusServiceUnavailable, "control_unavailable")
		return
	}
	controlJSON(w, http.StatusOK, controlapi.IssuedCertificate{CertificatePEM: string(issuedPEM), ChainPEM: string(material.CACert), Serial: certificate.SerialNumber.String(), NotBefore: certificate.NotBefore, NotAfter: certificate.NotAfter})
}

func renewalPeer(r *http.Request) *x509.Certificate {
	if r == nil || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return nil
	}
	return r.TLS.PeerCertificates[0]
}

func controlRenewalProblem(w http.ResponseWriter, status int, code string) {
	controlJSON(w, status, map[string]any{"type": "about:blank", "title": "Control request failed", "status": status, "code": code})
}
