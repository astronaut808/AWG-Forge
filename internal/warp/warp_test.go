package warp

import (
	"strings"
	"testing"
)

const sampleConfig = `
[Interface]
PrivateKey = private
Address = 172.16.0.2/32, 2606:4700:110:abcd::2/128
MTU = 1280

[Peer]
PublicKey = peer
PresharedKey = psk
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = engage.cloudflareclient.com:2408
PersistentKeepalive = 25
`

func TestParseWireGuardConfig(t *testing.T) {
	parsed, err := ParseWireGuardConfig(sampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.PrivateKey != "private" {
		t.Fatalf("private key = %q", parsed.PrivateKey)
	}
	if parsed.AddressV4 != "172.16.0.2" {
		t.Fatalf("address = %q", parsed.AddressV4)
	}
	if parsed.PeerPublicKey != "peer" || parsed.PresharedKey != "psk" {
		t.Fatalf("peer fields = %+v", parsed)
	}
	if parsed.Endpoint != "engage.cloudflareclient.com:2408" {
		t.Fatalf("endpoint = %q", parsed.Endpoint)
	}
}

func TestRenderConfigIncludesPolicyRoutes(t *testing.T) {
	parsed, err := ParseWireGuardConfig(sampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderConfig(parsed, []TunnelRoute{{InterfaceName: "awg20", Subnet: "10.20.0.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Table = 200",
		"ip route replace 10.20.0.0/24 dev awg20 table 200",
		"ip rule add from 10.20.0.0/24 lookup 200",
		"iptables -t nat -C POSTROUTING -s 10.20.0.0/24 -o %i -j MASQUERADE",
		"Endpoint = engage.cloudflareclient.com:2408",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered config missing %q:\n%s", want, rendered)
		}
	}
}
