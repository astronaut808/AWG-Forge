package nodeagent

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/google/uuid"
)

func TestEnrollmentRejectsUnpinnedAndAdditionalCABeforeSendingSecret(t *testing.T) {
	endpoint := controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 9443}
	pinned, pin, err := controlpki.Generate(endpoint, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := controlpki.Generate(endpoint, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(other.ServerCert, other.ServerKey)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}}
	server.StartTLS()
	defer server.Close()
	invitation := controlapi.Invitation{InvitationID: uuid.NewString(), Secret: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ControllerURL: server.URL, CAPin: pin, CACertPEM: string(pinned.CACert), ExpiresAt: time.Now().Add(time.Minute)}
	for _, name := range []string{"unpinned-server", "additional-ca", "wrong-pin"} {
		t.Run(name, func(t *testing.T) {
			candidate := invitation
			if name == "additional-ca" {
				candidate.CACertPEM += string(other.CACert)
			}
			if name == "wrong-pin" {
				candidate.CAPin = "sha256:" + strings.Repeat("0", 64)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := Enroll(ctx, app.New(config.Config{ConfigDir: t.TempDir()}), candidate, "node", nil)
			if err == nil {
				t.Fatal("invalid pinned enrollment succeeded")
			}
			if strings.Contains(err.Error(), candidate.Secret) || strings.Contains(err.Error(), candidate.ControllerURL) {
				t.Fatal("enrollment error disclosed bootstrap material")
			}
			if requests.Load() != 0 {
				t.Fatal("bootstrap secret sent before pinned TLS verification")
			}
		})
	}
}

func TestEnrollmentCancellationDoesNotCreateState(t *testing.T) {
	material, pin, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 9443}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	invitation := controlapi.Invitation{InvitationID: uuid.NewString(), Secret: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ControllerURL: "https://127.0.0.1:9443", CAPin: pin, CACertPEM: string(material.CACert), ExpiresAt: time.Now().Add(time.Minute)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service := app.New(config.Config{ConfigDir: t.TempDir()})
	if err := Enroll(ctx, service, invitation, "node", nil); err == nil {
		t.Fatal("canceled enrollment succeeded")
	}
}
