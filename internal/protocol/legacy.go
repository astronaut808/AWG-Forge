package protocol

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math/big"
	"strconv"

	"github.com/astronaut808/awg-forge/internal/config"
)

var legacyKeys = []string{"Jc", "Jmin", "Jmax", "S1", "S2", "H1", "H2", "H3", "H4"}

type Legacy10 struct{}

func (Legacy10) ID() string          { return "awg_legacy_1_0" }
func (Legacy10) DisplayName() string { return "AmneziaWG Legacy / 1.0" }
func (Legacy10) Version() string     { return "1.0" }

func (Legacy10) GenerateDefaults() (config.ProtocolParams, error) {
	u32 := func() (uint32, error) {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		return binary.BigEndian.Uint32(b[:]), nil
	}
	randomInt := func(min, max int) (int, error) {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(max-min+1)))
		if err != nil {
			return 0, err
		}
		return min + int(n.Int64()), nil
	}

	jc, err := randomInt(4, 10)
	if err != nil {
		return nil, err
	}
	jmin, err := randomInt(64, 256)
	if err != nil {
		return nil, err
	}
	jmax, err := randomInt(768, 1024)
	if err != nil {
		return nil, err
	}
	if jmax <= jmin {
		jmax = jmin + 64
	}
	s1, err := randomInt(15, 64)
	if err != nil {
		return nil, err
	}
	s2, err := randomInt(15, 64)
	if err != nil {
		return nil, err
	}
	for s1+56 == s2 {
		s2, err = randomInt(15, 64)
		if err != nil {
			return nil, err
		}
	}
	headers := map[uint32]bool{}
	nextHeader := func() (uint32, error) {
		for {
			v, err := u32()
			if err != nil {
				return 0, err
			}
			// Keep generated values inside the upstream recommended non-zero range.
			v = 5 + (v % 2147483643)
			if !headers[v] {
				headers[v] = true
				return v, nil
			}
		}
	}
	h1, err := nextHeader()
	if err != nil {
		return nil, err
	}
	h2, err := nextHeader()
	if err != nil {
		return nil, err
	}
	h3, err := nextHeader()
	if err != nil {
		return nil, err
	}
	h4, err := nextHeader()
	if err != nil {
		return nil, err
	}

	return config.ProtocolParams{
		"Jc":   strconv.Itoa(jc),
		"Jmin": strconv.Itoa(jmin),
		"Jmax": strconv.Itoa(jmax),
		"S1":   strconv.Itoa(s1),
		"S2":   strconv.Itoa(s2),
		"H1":   strconv.FormatUint(uint64(h1), 10),
		"H2":   strconv.FormatUint(uint64(h2), 10),
		"H3":   strconv.FormatUint(uint64(h3), 10),
		"H4":   strconv.FormatUint(uint64(h4), 10),
	}, nil
}

func (Legacy10) Validate(params config.ProtocolParams) error {
	for _, k := range legacyKeys {
		if params[k] == "" {
			return fmt.Errorf("missing protocol parameter %s", k)
		}
	}
	jc, err := strconv.Atoi(params["Jc"])
	if err != nil || jc < 0 || jc > 10 {
		return fmt.Errorf("Jc must be 0..10")
	}
	jmin, err := strconv.Atoi(params["Jmin"])
	if err != nil || jmin < 64 || jmin > 1024 {
		return fmt.Errorf("Jmin must be 64..1024")
	}
	jmax, err := strconv.Atoi(params["Jmax"])
	if err != nil || jmax <= jmin || jmax > 1024 {
		return fmt.Errorf("Jmax must be greater than Jmin and <= 1024")
	}
	s1, err := strconv.Atoi(params["S1"])
	if err != nil || s1 < 0 || s1 > 64 {
		return fmt.Errorf("S1 must be 0..64")
	}
	s2, err := strconv.Atoi(params["S2"])
	if err != nil || s2 < 0 || s2 > 64 {
		return fmt.Errorf("S2 must be 0..64")
	}
	if s1+56 == s2 {
		return fmt.Errorf("S1 + 56 must not equal S2")
	}
	seenHeaders := map[uint64]string{}
	for _, k := range []string{"H1", "H2", "H3", "H4"} {
		n, err := strconv.ParseUint(params[k], 10, 32)
		if err != nil || n > 4294967295 {
			return fmt.Errorf("%s must be 0..4294967295", k)
		}
		if prev, ok := seenHeaders[n]; ok {
			return fmt.Errorf("%s must be unique, duplicates %s", k, prev)
		}
		seenHeaders[n] = k
	}
	return nil
}

func (p Legacy10) RenderServerInterface(ctx RenderContext) ([]ConfigLine, error) {
	if err := p.Validate(ctx.Tunnel.ProtocolParams); err != nil {
		return nil, err
	}
	postUp := fmt.Sprintf("iptables -t nat -A POSTROUTING -s %s -o %s -j MASQUERADE; iptables -A INPUT -p udp -m udp --dport %d -j ACCEPT; iptables -A FORWARD -i %s -j ACCEPT; iptables -A FORWARD -o %s -j ACCEPT;",
		ctx.Tunnel.IPv4Subnet, ctx.State.ExternalInterface, ctx.Tunnel.ListenPort, ctx.Tunnel.InterfaceName, ctx.Tunnel.InterfaceName)
	postDown := fmt.Sprintf("iptables -t nat -D POSTROUTING -s %s -o %s -j MASQUERADE; iptables -D INPUT -p udp -m udp --dport %d -j ACCEPT; iptables -D FORWARD -i %s -j ACCEPT; iptables -D FORWARD -o %s -j ACCEPT;",
		ctx.Tunnel.IPv4Subnet, ctx.State.ExternalInterface, ctx.Tunnel.ListenPort, ctx.Tunnel.InterfaceName, ctx.Tunnel.InterfaceName)
	lines := []ConfigLine{
		{"PrivateKey", ctx.Tunnel.ServerPrivateKey},
		{"Address", ctx.Tunnel.ServerAddress + "/24"},
		{"ListenPort", strconv.Itoa(ctx.Tunnel.ListenPort)},
		{"PreUp", ""},
		{"PostUp", postUp},
		{"PreDown", ""},
		{"PostDown", postDown},
	}
	if ctx.Tunnel.MTU > 0 {
		lines = append(lines[:3], append([]ConfigLine{{"MTU", strconv.Itoa(ctx.Tunnel.MTU)}}, lines[3:]...)...)
	}
	return appendParams(lines, ctx.Tunnel.ProtocolParams), nil
}

func (Legacy10) RenderServerPeer(_ RenderContext, client config.Client) ([]ConfigLine, error) {
	return []ConfigLine{
		{"PublicKey", client.PublicKey},
		{"PresharedKey", client.PresharedKey},
		{"AllowedIPs", client.IPv4Address + "/32"},
	}, nil
}

func (p Legacy10) RenderClientInterface(ctx RenderContext, client config.Client) ([]ConfigLine, error) {
	if err := p.Validate(ctx.Tunnel.ProtocolParams); err != nil {
		return nil, err
	}
	lines := []ConfigLine{
		{"PrivateKey", client.PrivateKey},
		{"Address", client.IPv4Address + "/32"},
		{"DNS", ctx.Tunnel.DNS},
	}
	if ctx.Tunnel.MTU > 0 {
		lines = append(lines, ConfigLine{"MTU", strconv.Itoa(ctx.Tunnel.MTU)})
	}
	return appendParams(lines, ctx.Tunnel.ProtocolParams), nil
}

func (Legacy10) RenderClientPeer(ctx RenderContext, client config.Client) ([]ConfigLine, error) {
	return []ConfigLine{
		{"PublicKey", ctx.Tunnel.ServerPublicKey},
		{"PresharedKey", client.PresharedKey},
		{"AllowedIPs", ctx.Tunnel.AllowedIPs},
		{"PersistentKeepalive", strconv.Itoa(ctx.Tunnel.Keepalive)},
		{"Endpoint", fmt.Sprintf("%s:%d", ctx.State.ServerHost, ctx.Tunnel.ListenPort)},
	}, nil
}

func appendParams(lines []ConfigLine, params config.ProtocolParams) []ConfigLine {
	for _, k := range legacyKeys {
		lines = append(lines, ConfigLine{k, params[k]})
	}
	return lines
}

func defaultLegacyParams() (config.ProtocolParams, error) {
	return Legacy10{}.GenerateDefaults()
}

func validateLegacyParams(params config.ProtocolParams) error {
	return Legacy10{}.Validate(params)
}
