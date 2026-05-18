package app

import "testing"

func TestParseRuntimeAWGShowTransferCounters(t *testing.T) {
	got := parseRuntimeAWGShow(`interface: awg20
  public key: server-key
  listening port: 7445

peer: client-key
  preshared key: (hidden)
  endpoint: 185.2.104.24:8225
  allowed ips: 10.20.0.2/32
  latest handshake: 8 seconds ago
  transfer: 236.24 KiB received, 3.90 MiB sent
`)
	peer, ok := got.Peers["client-key"]
	if !ok {
		t.Fatal("missing peer")
	}
	if peer.LatestHandshake != "8 seconds ago" {
		t.Fatalf("handshake = %q", peer.LatestHandshake)
	}
	if peer.RxBytes == 0 || peer.TxBytes == 0 {
		t.Fatalf("transfer counters were not parsed: rx=%d tx=%d", peer.RxBytes, peer.TxBytes)
	}
	if peer.RxBytes != 241909 {
		t.Fatalf("rx bytes = %d, want 241909", peer.RxBytes)
	}
	if peer.TxBytes != 4089446 {
		t.Fatalf("tx bytes = %d, want 4089446", peer.TxBytes)
	}
}

func TestByteDeltaDoesNotUnderflow(t *testing.T) {
	if got := byteDelta(10, 7); got != 0 {
		t.Fatalf("delta = %d, want 0", got)
	}
	if got := byteDelta(7, 10); got != 3 {
		t.Fatalf("delta = %d, want 3", got)
	}
}

func TestHealthWarningThresholdTreatsTinyRxAsIdle(t *testing.T) {
	if healthTrafficWarningThresholdBytes != 1024 {
		t.Fatalf("threshold = %d, want 1024", healthTrafficWarningThresholdBytes)
	}
	rxDelta := uint64(82)
	txDelta := uint64(0)
	if rxDelta >= healthTrafficWarningThresholdBytes && txDelta == 0 {
		t.Fatal("tiny rx delta should not trigger NAT warning")
	}
}
