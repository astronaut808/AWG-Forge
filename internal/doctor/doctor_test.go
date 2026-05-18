package doctor

import "testing"

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
