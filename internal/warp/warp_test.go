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
	if parsed.AddressV6 != "2606:4700:110:abcd::2" {
		t.Fatalf("address v6 = %q", parsed.AddressV6)
	}
}

func TestParseWireGuardConfigPreservesAWGParams(t *testing.T) {
	parsed, err := ParseWireGuardConfig(`
[Interface]
PrivateKey = private
Jc = 4
Jmin = 40
Jmax = 70
S1 = 0
S2 = 0
H1 = 1
H2 = 2
H3 = 3
H4 = 4
I1 = <b 0xc200>
Address = 172.16.0.2, 2606:4700:110:abcd::2

[Peer]
PublicKey = peer
Endpoint = 162.159.195.4:500
PersistentKeepalive = 25
`)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"Jc", "Jmin", "Jmax", "S1", "S2", "H1", "H2", "H3", "H4", "I1"} {
		if parsed.ProtocolParams[key] == "" {
			t.Fatalf("missing protocol param %s: %+v", key, parsed.ProtocolParams)
		}
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

func TestRenderConfigIncludesAWGParams(t *testing.T) {
	parsed, err := ParseWireGuardConfig(sampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	parsed.ProtocolParams = map[string]string{
		"Jc": "4",
		"I1": "<b 0xc200>",
	}
	rendered, err := RenderConfig(parsed, []TunnelRoute{{InterfaceName: "awg20", Subnet: "10.20.0.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Jc = 4", "I1 = <b 0xc200>"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered config missing %q:\n%s", want, rendered)
		}
	}
}
