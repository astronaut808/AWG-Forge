package nodeagent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/google/uuid"
)

func TestBootstrapPublicDiscoveryProvesPinAndNormalTLSWithoutCredentials(t *testing.T) {
	for _, scenario := range []string{"valid", "wrong-pin", "wrong-san", "other-leaf", "redirect", "oversize", "unknown-field", "additional-ca"} {
		t.Run(scenario, func(t *testing.T) {
			host := "127.0.0.1"
			if scenario == "wrong-san" {
				host = "wrong.example.test"
			}
			material, pin, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: host, Port: 9443}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			other, _, err := controlpki.Generate(controlpki.Endpoint{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 9443}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			leaf := material
			if scenario == "other-leaf" {
				leaf = other
			}
			certificate, err := tls.X509KeyPair(leaf.ServerCert, leaf.ServerKey)
			if err != nil {
				t.Fatal(err)
			}
			var requests, credentials atomic.Int64
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Authorization") != "" || r.ContentLength > 0 || r.URL.Path != "/control/v1/bootstrap" || r.URL.RawQuery != "" {
					credentials.Add(1)
				}
				if scenario == "redirect" {
					http.Redirect(w, r, "/other", http.StatusFound)
					return
				}
				if scenario == "oversize" {
					// Deliberately oversized non-HTML body for the bounded input test.
					// nosemgrep: go.lang.security.audit.xss.no-direct-write-to-responsewriter.no-direct-write-to-responsewriter
					_, _ = w.Write([]byte(strings.Repeat("x", 65537)))
					return
				}
				if scenario == "unknown-field" {
					_, _ = w.Write([]byte(`{"ca_cert_pem":"x","secret":"CANARY"}`))
					return
				}
				ca := string(material.CACert)
				if scenario == "additional-ca" {
					ca += string(other.CACert)
				}
				_ = json.NewEncoder(w).Encode(controlapi.Bootstrap{CACertPEM: ca})
			}))
			server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}}
			server.StartTLS()
			defer server.Close()
			if scenario == "wrong-pin" {
				pin = "sha256:" + strings.Repeat("0", 64)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			invitation, err := BootstrapInvitation(ctx, server.URL, pin, uuid.NewString())
			if scenario == "valid" {
				if err != nil || invitation.Secret != "" || invitation.CACertPEM != string(material.CACert) || requests.Load() != 2 {
					t.Fatal("valid public trust bootstrap failed")
				}
			} else if err == nil || requests.Load() != 1 {
				t.Fatal("unsafe trust material reached verified HTTP")
			}
			if credentials.Load() != 0 {
				t.Fatal("bootstrap transmitted credential")
			}
		})
	}
}

func TestBootstrapRejectsEndpointConfusionBeforeNetwork(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:9443", "https://user@127.0.0.1:9443", "https://127.0.0.1:9443/path", "https://127.0.0.1:9443/?secret=x", "https://127.0.0.1:9443/#x", "https://127.0.0.1:9443/%2f", "https://127.0.0.1:09443", "https://127.0.0.1", "https://127.0.0.1:9443\n"} {
		if _, err := BootstrapInvitation(context.Background(), endpoint, "sha256:"+strings.Repeat("0", 64), uuid.NewString()); err == nil {
			t.Fatal("ambiguous endpoint admitted")
		}
	}
	for _, name := range []string{"", "-option", "x\n", "x\t", "x\x00", strings.Repeat("x", 129)} {
		if ValidNodeName(name) {
			t.Fatal("unsafe name admitted")
		}
	}
	for _, name := range []string{"node", "Узел 1", "x';$(touch marker)"} {
		if !ValidNodeName(name) {
			t.Fatal("public printable name rejected")
		}
	}
}
