package controlserver

import (
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
)

func TestRuntimeExternalOptInRetainsEndpointAndPKIValidation(t *testing.T) {
	endpoint := controlpki.Endpoint{BindIP: "192.0.2.10", Advertised: "control.example.test", Port: 8443}
	material, pin, err := controlpki.Generate(endpoint, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(material, endpoint, pin, nil, nil); err == nil {
		t.Fatal("default transport accepted external bind")
	}
	runtime, err := NewWithOptions(material, endpoint, pin, nil, nil, Options{AllowNonLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	runtime.Close()
	for _, bind := range []string{"0.0.0.0", "::", "224.0.0.1", "ff02::1", "::ffff:192.0.2.10", "fe80::1%eth0", "control.example.test", " 192.0.2.10"} {
		changed := endpoint
		changed.BindIP = bind
		if _, err := NewWithOptions(material, changed, pin, nil, nil, Options{AllowNonLoopback: true}); err == nil {
			t.Fatalf("consent bypassed bind validation: %s", bind)
		}
	}
	changed := endpoint
	changed.Advertised = "wrong.example.test"
	if _, err := NewWithOptions(material, changed, pin, nil, nil, Options{AllowNonLoopback: true}); err == nil {
		t.Fatal("consent bypassed SAN verification")
	}
	material.ServerKey = []byte("broken")
	if _, err := NewWithOptions(material, endpoint, pin, nil, nil, Options{AllowNonLoopback: true}); err == nil {
		t.Fatal("consent bypassed PKI validation")
	}
}
