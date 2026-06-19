package warp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/astronaut808/awg-forge/internal/config"
)

func TestAPIClientRegister(t *testing.T) {
	var request registerRequest
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/reg" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q", r.Method)
		}
		if got := r.Header.Get("CF-Client-Version"); got != "test-version" {
			t.Fatalf("CF-Client-Version = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{
			"id": "device-id",
			"token": "access-token",
			"account": {"license": "license-key"},
			"config": {
				"client_id": "client-id",
				"interface": {"addresses": {"v4": "172.16.0.2/32", "v6": "2606:4700:110::2/128"}},
				"peers": [{"public_key": "peer-public-key", "endpoint": {"host": "engage.cloudflareclient.com:2408"}}]
			}
		}`))
	}))
	defer server.Close()

	registered, err := (APIClient{
		BaseURL:       server.URL,
		ClientVersion: "test-version",
		HTTPClient:    server.Client(),
	}).Register(context.Background(), "private-key", "public-key")
	if err != nil {
		t.Fatal(err)
	}
	if request.Key != "public-key" || request.Type != "PC" || request.Model != "awg-forge" {
		t.Fatalf("unexpected request body: %+v", request)
	}
	if !registered.Registered() {
		t.Fatalf("expected registered WARP: %+v", registered)
	}
	if registered.PrivateKey != "private-key" {
		t.Fatalf("private key = %q", registered.PrivateKey)
	}
	if registered.PeerPublicKey != "peer-public-key" {
		t.Fatalf("peer public key = %q", registered.PeerPublicKey)
	}
	if registered.Endpoint != "engage.cloudflareclient.com:2408" {
		t.Fatalf("endpoint = %q", registered.Endpoint)
	}
	if registered.AddressV4 != "172.16.0.2" {
		t.Fatalf("address v4 = %q", registered.AddressV4)
	}
	if registered.MTU != 1280 || registered.PersistentKeepalive != 25 {
		t.Fatalf("defaults = mtu %d keepalive %d", registered.MTU, registered.PersistentKeepalive)
	}
}

func TestAPIClientUnregister(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/reg/device-id" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Method != http.MethodDelete {
			t.Fatalf("method = %q", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("authorization = %q", got)
		}
		if got := r.Header.Get("CF-Client-Version"); got != "test-version" {
			t.Fatalf("CF-Client-Version = %q", got)
		}
		rw.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := (APIClient{
		BaseURL:       server.URL,
		ClientVersion: "test-version",
		HTTPClient:    server.Client(),
	}).Unregister(context.Background(), registeredWarpForTest())
	if err != nil {
		t.Fatal(err)
	}
}

func TestAPIClientUnregisterErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusForbidden)
		_, _ = rw.Write([]byte(`{"errors":[{"message":"invalid token"}]}`))
	}))
	defer server.Close()

	err := (APIClient{
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	}).Unregister(context.Background(), registeredWarpForTest())
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got != "WARP unregister failed: invalid token" {
		t.Fatalf("error = %q", got)
	}
}

func registeredWarpForTest() config.Warp {
	return config.Warp{DeviceID: "device-id", AccessToken: "access-token"}
}

func TestAPIClientRegisterErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusTooManyRequests)
		_, _ = rw.Write([]byte(`{"errors":[{"message":"rate limited"}]}`))
	}))
	defer server.Close()

	_, err := (APIClient{
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	}).Register(context.Background(), "private-key", "public-key")
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got != "WARP registration failed: rate limited" {
		t.Fatalf("error = %q", got)
	}
}
