package doctor

import (
	"strings"
	"testing"
)

func TestParseAWGShow(t *testing.T) {
	got := parseAWGShow(`interface: awg20
  public key: server-key
  private key: (hidden)
  listening port: 7445
  jc: 8
  s3: 36

peer: client-key
  preshared key: (hidden)
  endpoint: 185.2.104.24:8225
  allowed ips: 10.20.0.2/32
  latest handshake: 8 seconds ago
  transfer: 236.24 KiB received, 3.90 MiB sent
`)
	if got.ListenPort != 7445 {
		t.Fatalf("listen port = %d, want 7445", got.ListenPort)
	}
	peer, ok := got.Peers["client-key"]
	if !ok {
		t.Fatal("missing client peer")
	}
	if peer.AllowedIPs != "10.20.0.2/32" {
		t.Fatalf("allowed IPs = %q", peer.AllowedIPs)
	}
	if peer.LatestHandshake != "8 seconds ago" {
		t.Fatalf("handshake = %q", peer.LatestHandshake)
	}
	if peer.Transfer != "236.24 KiB received, 3.90 MiB sent" {
		t.Fatalf("transfer = %q", peer.Transfer)
	}
}

func TestParseAWGShowPeerWithoutHandshake(t *testing.T) {
	got := parseAWGShow(`interface: awg15
  listening port: 7444

peer: client-key
  preshared key: (hidden)
  allowed ips: 10.15.0.2/32
`)
	peer := got.Peers["client-key"]
	if peer.LatestHandshake != "" {
		t.Fatalf("handshake = %q, want empty", peer.LatestHandshake)
	}
}

func TestParseRouteDev(t *testing.T) {
	got := parseRouteDev(`1.1.1.1 via 203.0.113.1 dev ens3 src 203.0.113.10 uid 0
    cache
`)
	if got != "ens3" {
		t.Fatalf("dev = %q, want ens3", got)
	}
}

func TestProtocolNotSupportedDetection(t *testing.T) {
	if !isProtocolNotSupported("awg show awg15 failed: Unable to access interface: Protocol not supported") {
		t.Fatal("expected Protocol not supported to be detected")
	}
	if isProtocolNotSupported("awg show awg15 failed: operation not permitted") {
		t.Fatal("did not expect unrelated error to match")
	}
}

func TestRedactProcessLine(t *testing.T) {
	got := redactProcessLine(`UNCONN 0 0 0.0.0.0:7443 0.0.0.0:* users:(("amneziawg-go",pid=12345,fd=7))`)
	if strings.Contains(got, "pid=12345") {
		t.Fatalf("pid was not redacted: %q", got)
	}
	if !strings.Contains(got, "pid=<pid>") {
		t.Fatalf("redacted pid marker missing: %q", got)
	}
}
