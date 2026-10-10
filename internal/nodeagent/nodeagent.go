// Package nodeagent implements the outbound node-side enrollment and presence loop.
package nodeagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/buildinfo"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/google/uuid"
)

const maxEnrollmentPoll = 30 * time.Second

// EnrollmentService admits either first enrollment or a captured offline root
// recovery session, sharing the same pinned handshake and approval protocol.
type EnrollmentService interface {
	PreflightNodeEnrollment(context.Context) error
	InstallNodeEnrollment(context.Context, controlapi.Invitation, controlapi.EnrollmentStatus, []byte) error
	BootID() (string, error)
}

func Enroll(ctx context.Context, service EnrollmentService, invitation controlapi.Invitation, name string, onComparison func(string) error) error {
	if err := validateInvitation(invitation); err != nil {
		return err
	}
	if err := service.PreflightNodeEnrollment(ctx); err != nil {
		return errors.New("node enrollment is unavailable")
	}
	if deadline, ok := ctx.Deadline(); !ok || invitation.ExpiresAt.Before(deadline) {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, invitation.ExpiresAt)
		defer cancel()
	}
	_, private, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		return errors.New("generate node key failed")
	}
	csrDER, err := x509.CreateCertificateRequest(crand.Reader, &x509.CertificateRequest{}, private)
	if err != nil {
		return errors.New("create enrollment request failed")
	}
	client, err := enrollmentClient(invitation)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	bootID, err := service.BootID()
	if err != nil {
		return errors.New("node identity unavailable")
	}
	if name == "" {
		name = "node"
	}
	request := controlapi.ClaimRequest{RequestedName: name, CSRPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})), BootID: bootID, ApplicationVersion: buildinfo.Current().Version, ContractVersions: []int{1}, Capabilities: []string{"presence", "snapshot.v1"}}
	var accepted controlapi.ClaimAccepted
	if err := requestJSON(ctx, client, invitation.ControllerURL, http.MethodPost, "/control/v1/enrollments/"+invitation.InvitationID+"/claim", invitation.Secret, request, &accepted); err != nil {
		return errors.New("enrollment claim failed")
	}
	if !comparisonCodeRE.MatchString(accepted.VerificationCode) || !validUUID(accepted.EnrollmentID) || !validToken(accepted.ClaimToken) || !accepted.ExpiresAt.After(time.Now()) || accepted.ExpiresAt.After(invitation.ExpiresAt) || accepted.PollAfterSeconds < 1 || accepted.PollAfterSeconds > 30 {
		return errors.New("invalid enrollment response")
	}
	if onComparison != nil {
		if err := onComparison(accepted.VerificationCode); err != nil {
			return err
		}
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return errors.New("encode node key failed")
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	for {
		var status controlapi.EnrollmentStatus
		if err := requestJSON(ctx, client, invitation.ControllerURL, http.MethodGet, "/control/v1/enrollments/"+accepted.EnrollmentID, accepted.ClaimToken, nil, &status); err != nil {
			return errors.New("enrollment status failed")
		}
		switch status.Status {
		case "approved":
			if status.ContractVersion != 1 {
				return errors.New("invalid enrollment status")
			}
			return service.InstallNodeEnrollment(ctx, invitation, status, keyPEM)
		case "pending":
			if status.VerificationCode != accepted.VerificationCode || status.ExpiresAt == nil || !status.ExpiresAt.Equal(accepted.ExpiresAt) {
				return errors.New("invalid enrollment status")
			}
		default:
			return errors.New("enrollment was not approved")
		}
		wait := time.Duration(status.PollAfterSeconds) * time.Second
		if wait <= 0 || wait > maxEnrollmentPoll {
			wait = time.Second
		}
		if wait > 30*time.Second {
			wait = 30 * time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Run returns only safe errors; the caller logs its stable category. A 403
// stops retries because local recovery/re-enrollment is required.
func Run(ctx context.Context, service *app.Service, cfg config.Config) error {
	state, err := service.NodeAgentState(ctx)
	if err != nil {
		return err
	}
	if state.EffectiveMode() != config.ModeNode || state.ManagedNode == nil || state.NodeConnection == nil {
		return nil
	}
	identity, err := storage.New(cfg.ConfigDir).LoadNodeIdentity(state.NodeConnection.CredentialGeneration)
	if err != nil {
		return errors.New("node credentials unavailable")
	}
	client, err := mtlsClient(state.NodeConnection, identity)
	if err != nil {
		return errors.New("node credentials invalid")
	}
	defer func() { client.CloseIdleConnections() }()
	boot, err := service.StartManagedNodeBootContext(ctx)
	if err != nil {
		return err
	}
	delay := time.Second
	var snapshotSequence uint64
	certificate, err := strictCertificate(identity.Certificate)
	if err != nil {
		return errors.New("node identity invalid")
	}
	connection := *state.NodeConnection
	managed := *state.ManagedNode
	for {
		if !time.Now().Before(certificate.NotAfter) {
			return errors.New("node certificate expired")
		}
		state, err = service.NodeAgentState(ctx)
		if err != nil {
			return err
		}
		if state.NodeConnection == nil || *state.NodeConnection != connection || state.ManagedNode == nil || state.ManagedNode.ControllerID != managed.ControllerID || state.ManagedNode.NodeID != managed.NodeID || state.ManagedNode.BindingEpoch != managed.BindingEpoch || state.ManagedNode.StateEpoch != boot.StateEpoch {
			return errors.New("node binding changed")
		}
		p := controlapi.Presence{BootID: boot.BootID, BootSequence: boot.BootSequence, ApplicationVersion: buildinfo.Current().Version, ContractVersion: 1, StateEpoch: boot.StateEpoch, BindingEpoch: state.ManagedNode.BindingEpoch, DesiredGeneration: state.ManagedNode.DesiredGeneration, Capabilities: []string{"presence", "snapshot.v1"}, ObservedAt: time.Now().UTC()}
		var accepted controlapi.PresenceAccepted
		err = requestJSON(ctx, client, state.NodeConnection.ControllerURL, http.MethodPut, "/control/v1/node/presence", "", p, &accepted)
		presenceSucceeded := err == nil
		if err == nil {
			if accepted.ControllerID != state.ManagedNode.ControllerID || !validUUID(accepted.SessionID) || !accepted.SessionExpiresAt.After(time.Now()) {
				return errors.New("controller identity rejected")
			}
			delay = time.Duration(accepted.NextPollSeconds) * time.Second
			if delay < time.Second || delay > 30*time.Second {
				delay = 15 * time.Second
			}
			snapshotSequence++
			snapshot, collectErr := collectSnapshot(ctx, service, cfg, accepted.SessionID, snapshotSequence)
			if collectErr != nil {
				err = collectErr
			} else {
				var ack controlapi.SnapshotAccepted
				err = requestJSON(ctx, client, connection.ControllerURL, http.MethodPut, "/control/v1/node/snapshot", "", snapshot, &ack)
				if err == nil && (ack.Sequence != snapshotSequence || ack.ReceivedAt.IsZero()) {
					return errors.New("invalid snapshot acknowledgement")
				}
				if errors.Is(err, errForbidden) {
					return errors.New("node authorization revoked")
				}
				// A session may expire during a bounded network failure. Refresh
				// presence before retry; never resend a snapshot under an old fence.
			}
		} else if errors.Is(err, errForbidden) || errors.Is(err, errFenced) {
			return errors.New("node authorization revoked")
		}
		if presenceSucceeded && !time.Now().Before(certificate.NotBefore.Add(certificate.NotAfter.Sub(certificate.NotBefore)*2/3)) {
			renewal, prepareErr := service.PrepareNodeCertificateRenewal(ctx, connection)
			if prepareErr != nil {
				return errors.New("node renewal preparation failed")
			}
			var issued controlapi.IssuedCertificate
			renewalErr := requestJSON(ctx, client, connection.ControllerURL, http.MethodPost, "/control/v1/node/certificate-renewals", "", controlapi.CertificateRenewalRequest{BootID: boot.BootID, CurrentSerial: renewal.CurrentSerial, CSRPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: renewal.CSRDER}))}, &issued)
			if errors.Is(renewalErr, errForbidden) || errors.Is(renewalErr, errFenced) {
				return errors.New("node renewal authorization denied")
			}
			if renewalErr == nil {
				if err := service.InstallNodeCertificateRenewal(ctx, connection, renewal.Generation, issued); err != nil {
					return errors.New("node renewal installation failed")
				}
				nextState, err := service.NodeAgentState(ctx)
				if err != nil || nextState.NodeConnection == nil || nextState.NodeConnection.CredentialGeneration != renewal.Generation || nextState.NodeConnection.ControllerURL != connection.ControllerURL || nextState.NodeConnection.CAPin != connection.CAPin {
					return errors.New("node renewal state changed")
				}
				nextIdentity, err := storage.New(cfg.ConfigDir).LoadNodeIdentity(renewal.Generation)
				if err != nil {
					return errors.New("renewed identity unavailable")
				}
				nextClient, err := mtlsClient(nextState.NodeConnection, nextIdentity)
				if err != nil {
					return errors.New("renewed identity invalid")
				}
				client.CloseIdleConnections()
				client = nextClient
				connection = *nextState.NodeConnection
				certificate, err = strictCertificate(nextIdentity.Certificate)
				if err != nil {
					return errors.New("renewed identity invalid")
				}
				delay = time.Second
			} else {
				// Keep refreshing presence with the predecessor while retrying
				// the durable CSR; its server-side overlap bounds admission.
				err = renewalErr
			}
		}
		wait := delay
		if err != nil {
			if jitter, jitterErr := crand.Int(crand.Reader, big.NewInt(int64(delay/4+1))); jitterErr == nil {
				wait += time.Duration(jitter.Int64())
			}
			if delay < 30*time.Second {
				delay *= 2
				if delay > 30*time.Second {
					delay = 30 * time.Second
				}
			}
		}
		if wait > 30*time.Second {
			wait = 30 * time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

var errForbidden = errors.New("forbidden")
var errFenced = errors.New("fenced")
var errRenewalNotDue = errors.New("renewal not due")
var comparisonCodeRE = regexp.MustCompile(`^[A-Z0-9]{4}-[A-Z0-9]{4}$`)

func validUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value
}
func validToken(value string) bool {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) == 32
}

func validateInvitation(i controlapi.Invitation) error {
	if !validUUID(i.InvitationID) || i.Secret == "" || i.CAPin == "" || i.CACertPEM == "" || i.ExpiresAt.IsZero() || !i.ExpiresAt.After(time.Now()) {
		return errors.New("invalid enrollment invitation")
	}
	if secret, err := base64.RawURLEncoding.Strict().DecodeString(i.Secret); err != nil || len(secret) != 32 {
		return errors.New("invalid enrollment invitation")
	}
	if err := controlpki.ValidateControllerURL(i.ControllerURL); err != nil {
		return errors.New("invalid enrollment endpoint")
	}
	ca, err := strictCertificate([]byte(i.CACertPEM))
	if err != nil {
		return errors.New("invalid enrollment invitation")
	}
	if !ca.IsCA || !ca.BasicConstraintsValid || ca.CheckSignatureFrom(ca) != nil || ca.KeyUsage&x509.KeyUsageCertSign == 0 || time.Now().Before(ca.NotBefore) || !time.Now().Before(ca.NotAfter) || controlpki.Pin(ca) != i.CAPin {
		return errors.New("invalid enrollment invitation")
	}
	return nil
}
func enrollmentClient(i controlapi.Invitation) (*http.Client, error) {
	if err := validateInvitation(i); err != nil {
		return nil, err
	}
	return clientForController(i.ControllerURL, i.CAPin, []byte(i.CACertPEM))
}
func clientForController(endpoint, pin string, caPEM []byte) (*http.Client, error) {
	i := controlapi.Invitation{InvitationID: uuid.Nil.String(), Secret: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ControllerURL: endpoint, CAPin: pin, CACertPEM: string(caPEM), ExpiresAt: time.Now().Add(time.Minute)}
	if err := validateInvitation(i); err != nil {
		return nil, err
	}
	ca, err := strictCertificate(caPEM)
	if err != nil {
		return nil, errors.New("invalid enrollment invitation")
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	return &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}, Proxy: nil, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 15 * time.Second, IdleConnTimeout: 15 * time.Second, MaxConnsPerHost: 2, ForceAttemptHTTP2: false}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func strictCertificate(body []byte) (*x509.Certificate, error) {
	block, rest := pem.Decode(body)
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) > 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("invalid certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}
func mtlsClient(c *config.NodeConnectionState, m storage.NodeIdentityMaterial) (*http.Client, error) {
	if app.ValidateNodeIdentity(c, m, false) != nil {
		return nil, errors.New("invalid node identity")
	}
	base, err := clientForController(c.ControllerURL, c.CAPin, m.CACert)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(m.Certificate, m.PrivateKey)
	if err != nil {
		return nil, err
	}
	base.Transport.(*http.Transport).TLSClientConfig.Certificates = []tls.Certificate{cert}
	return base, nil
}
func requestJSON(ctx context.Context, client *http.Client, base, method, route, token string, input, output any) error {
	u, err := url.Parse(base)
	if err != nil {
		return err
	}
	u.Path = route
	var body *bytes.Reader
	if input == nil {
		body = bytes.NewReader(nil)
	} else {
		b, e := json.Marshal(input)
		if e != nil {
			return e
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusForbidden {
		return errForbidden
	}
	if res.StatusCode == http.StatusConflict {
		var problem struct {
			Code string `json:"code"`
		}
		raw, readErr := io.ReadAll(io.LimitReader(res.Body, (64<<10)+1))
		if route == "/control/v1/node/certificate-renewals" && readErr == nil && len(raw) <= 64<<10 && json.Unmarshal(raw, &problem) == nil && problem.Code == "renewal_not_due" {
			return errRenewalNotDue
		}
		return errFenced
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("control response %d", res.StatusCode)
	}
	if output != nil {
		raw, err := io.ReadAll(io.LimitReader(res.Body, (64<<10)+1))
		if err != nil || len(raw) > 64<<10 {
			return errors.New("invalid control response")
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(output); err != nil {
			return errors.New("invalid control response")
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return errors.New("invalid control response")
		}
		return nil
	}
	return nil
}
