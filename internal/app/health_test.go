package app

import (
	"testing"
	"time"
)

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

func TestParseHandshakeAge(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want time.Duration
		ok   bool
	}{
		{name: "seconds", in: "37 seconds ago", want: 37 * time.Second, ok: true},
		{name: "mixed", in: "1 hour, 28 minutes, 26 seconds ago", want: time.Hour + 28*time.Minute + 26*time.Second, ok: true},
		{name: "days", in: "2 days, 1 hour ago", want: 49 * time.Hour, ok: true},
		{name: "empty", in: "", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseHandshakeAge(tt.in)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("age = %s, want %s", got, tt.want)
			}
		})
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
