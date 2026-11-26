package warp

import (
	"bufio"
	"strings"
	"testing"

	"github.com/astronaut808/awg-forge/internal/config"
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

func TestImportedAWGParamsValidateBeforeRender(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
		params           config.ProtocolParams
	}{
		{name: "negative random length", key: "I1", value: "<r -1>"},
		{name: "negative character count", key: "I2", value: "<rc -1>"},
		{name: "negative digit count", key: "I3", value: "<rd -1>"},
		{name: "negative data size length", key: "I4", value: "<dz -1>"},
		{name: "kernel-only checksum tag", key: "I3", value: "<c>"},
		{name: "invalid byte count", key: "I5", value: "<b 0x123>"},
		{name: "oversized chain", key: "I5", value: "<r 65535><t>"},
		{name: "oversized number", key: "I1", value: "<r 18446744073709551616>"},
		{name: "out of range junk count", key: "Jc", value: "65536"},
		{name: "reversed junk bounds", key: "Jmin", value: "41", params: config.ProtocolParams{"Jmax": "40"}},
		{name: "missing junk maximum", key: "Jmin", value: "40"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := ParseWireGuardConfig(sampleConfig)
			if err != nil {
				t.Fatal(err)
			}
			parsed.ProtocolParams = tc.params
			if parsed.ProtocolParams == nil {
				parsed.ProtocolParams = config.ProtocolParams{}
			}
			parsed.ProtocolParams[tc.key] = tc.value
			input := ""
			for key, value := range parsed.ProtocolParams {
				input += key + " = " + value + "\n"
			}
			invalidConfig := strings.Replace(sampleConfig, "[Interface]\n", "[Interface]\n"+input, 1)
			_, err = ParseWireGuardConfig(invalidConfig)
			if err == nil || !strings.Contains(err.Error(), tc.key) || strings.Contains(err.Error(), tc.value) {
				t.Fatal("expected an import error identifying the field without its value")
			}
			rendered, err := RenderConfig(parsed, []TunnelRoute{{InterfaceName: "awg20", Subnet: "10.20.0.0/24"}})
			if err == nil || !strings.Contains(err.Error(), tc.key) || rendered != "" {
				t.Fatal("expected rendering to reject the stored WARP field")
			}
		})
	}
}

func TestImportedSignatureUAPILineLimit(t *testing.T) {
	const maxUAPILineSize = 64 * 1024
	// A space inside the tag adds one serialized byte without changing the packet size.
	tag := "<b 0x" + strings.Repeat("00", 32763)
	routes := []TunnelRoute{{InterfaceName: "awg20", Subnet: "10.20.0.0/24"}}
	for _, tc := range []struct {
		name     string
		value    string
		lineSize int
	}{
		{name: "at limit", value: tag + ">", lineSize: maxUAPILineSize},
		{name: "one byte over", value: tag + " >", lineSize: maxUAPILineSize + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := "i1=" + tc.value + "\n"
			if len(line) != tc.lineSize {
				t.Fatalf("UAPI line length = %d, want %d", len(line), tc.lineSize)
			}
			valid := tc.lineSize <= maxUAPILineSize
			scanner := bufio.NewScanner(strings.NewReader(line + "\n"))
			if scanner.Scan() != valid {
				t.Fatal("unexpected result from scanning the UAPI line")
			}
			raw := tc.value + " # excluded from UAPI"
			input := strings.Replace(sampleConfig, "[Interface]\n", "[Interface]\nI1 = "+raw+"\n", 1)
			parsed, err := ParseWireGuardConfig(input)
			if !valid {
				if err == nil || !strings.Contains(err.Error(), "I1 serialized signature") {
					t.Fatal("expected an import error identifying the oversized serialized I1")
				}
				stored, err := ParseWireGuardConfig(sampleConfig)
				if err != nil {
					t.Fatal(err)
				}
				stored.ProtocolParams = config.ProtocolParams{"I1": raw}
				if rendered, err := RenderConfig(stored, routes); err == nil || rendered != "" {
					t.Fatal("expected rendering to reject the oversized serialized I1")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			rendered, err := RenderConfig(parsed, routes)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(rendered, "\nI1 = "+tc.value+"\n") {
				t.Fatal("expected the rendered CPS value to match the validated value")
			}
		})
	}
}

func TestRenderConfigRejectsMultilineStoredValue(t *testing.T) {
	parsed, err := ParseWireGuardConfig(sampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	parsed.ProtocolParams = config.ProtocolParams{"H1": "1 # comment\nI2 = <r 1>"}
	if rendered, err := RenderConfig(parsed, []TunnelRoute{{InterfaceName: "awg20", Subnet: "10.20.0.0/24"}}); err == nil || rendered != "" {
		t.Fatal("expected rendering to reject a stored value with a line break")
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
		"Endpoint = engage.cloudflareclient.com:2408",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered config missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "iptables") {
		t.Fatalf("WARP runtime config must not manage firewall rules:\n%s", rendered)
	}
}

func TestRenderConfigIncludesAWGParams(t *testing.T) {
	parsed, err := ParseWireGuardConfig(sampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	parsed.ProtocolParams = map[string]string{
		"Jc":   "4",
		"Jmin": "40 # fixed size",
		"Jmax": "40",
		"I1":   "<b 0xc200> <r 1500>",
		"I2":   "<d><ds><dz 2>",
		"I3":   "<t>",
		"I4":   "# unused",
	}
	rendered, err := RenderConfig(parsed, []TunnelRoute{{InterfaceName: "awg20", Subnet: "10.20.0.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Jc = 4", "Jmin = 40 # fixed size", "Jmax = 40", "I1 = <b 0xc200> <r 1500>", "I2 = <d><ds><dz 2>", "I3 = <t>"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered config missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "I4 =") {
		t.Fatal("a comment alone should not render an AWG setting")
	}
}
