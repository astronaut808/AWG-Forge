package nodeagent

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlpki"
)

var pinRE = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// ValidateBootstrapParameters runs before networking or reading a secret.
func ValidateBootstrapParameters(endpoint, pin, id, name string) error {
	if controlpki.ValidateControllerURL(endpoint) != nil || !pinRE.MatchString(pin) || !validUUID(id) || !ValidNodeName(name) {
		return errors.New("invalid public enrollment parameters")
	}
	return nil
}

func ValidNodeName(name string) bool {
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 128 || strings.HasPrefix(name, "-") {
		return false
	}
	for _, c := range name {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

// BootstrapInvitation retrieves public CA material without sending any secret.
// The first response is deliberately untrusted. Only a matching self-signed CA
// is used for a second, normally verified TLS request. Credentials are admitted
// later by Enroll, on the same pinned transport policy, never on this client.
func BootstrapInvitation(ctx context.Context, endpoint, pin, id string) (controlapi.Invitation, error) {
	if err := ValidateBootstrapParameters(endpoint, pin, id, "node"); err != nil {
		return controlapi.Invitation{}, err
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		// Public CA retrieval has no authentication/credential input. Below we
		// prove CA SPKI and repeat the request with normal chain/SAN/time checks.
		// nosemgrep: go.lang.security.audit.crypto.tls.tls-with-insecure-skip-verify.tls-with-insecure-skip-verify, problem-based-packs.insecure-transport.go-stdlib.bypass-tls-verification.bypass-tls-verification
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true}, // #nosec G402 -- secret-free trust discovery; verified below
		Proxy:           nil, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxConnsPerHost: 1,
	}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	var untrusted controlapi.Bootstrap
	if requestJSON(ctx, client, endpoint, http.MethodGet, "/control/v1/bootstrap", "", nil, &untrusted) != nil || len(untrusted.CACertPEM) > 8192 {
		return controlapi.Invitation{}, errors.New("public enrollment bootstrap unavailable")
	}
	i := controlapi.Invitation{InvitationID: id, ControllerURL: endpoint, CAPin: pin, CACertPEM: untrusted.CACertPEM, ExpiresAt: time.Now().UTC().Add(10 * time.Minute)}
	// A synthetic credential is used only for local validation. It is never sent.
	i.Secret = base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	verified, err := enrollmentClient(i)
	if err != nil {
		return controlapi.Invitation{}, errors.New("controller CA pin rejected")
	}
	defer verified.CloseIdleConnections()
	var confirmed controlapi.Bootstrap
	if requestJSON(ctx, verified, endpoint, http.MethodGet, "/control/v1/bootstrap", "", nil, &confirmed) != nil || confirmed.CACertPEM != i.CACertPEM {
		return controlapi.Invitation{}, errors.New("controller TLS identity rejected")
	}
	i.Secret = ""
	return i, nil
}
