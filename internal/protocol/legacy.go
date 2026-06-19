package protocol

import (
	"fmt"
	"net"
	"strconv"

	"github.com/astronaut808/awg-forge/internal/config"
)

var legacyKeys = []string{"Jc", "Jmin", "Jmax", "S1", "S2", "H1", "H2", "H3", "H4"}

type Legacy10 struct{}

func (Legacy10) ID() string          { return "awg_legacy_1_0" }
func (Legacy10) DisplayName() string { return "AmneziaWG Legacy / 1.0" }
func (Legacy10) Version() string     { return "1.0" }

func (Legacy10) GenerateDefaults() (config.ProtocolParams, error) {
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
	s1, s2, err := validateJunkAndBasePadding(params)
	if err != nil {
		return err
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
	lines, err := baseInterfaceLines(ctx)
	if err != nil {
		return nil, err
	}
	return appendParams(lines, ctx.Tunnel.ProtocolParams), nil
}

func baseInterfaceLines(ctx RenderContext) ([]ConfigLine, error) {
	_, ipnet, err := net.ParseCIDR(ctx.Tunnel.IPv4Subnet)
	if err != nil {
		return nil, err
	}
	if ctx.Tunnel.ServerAddress == "" || ctx.Tunnel.ServerAddress == "<nil>" {
		return nil, fmt.Errorf("server address is required")
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 32 {
		return nil, fmt.Errorf("IPv4 subnet required")
	}
	egressInterface := ctx.State.ExternalInterface
	if ctx.Tunnel.EgressMode == config.EgressWarp {
		egressInterface = ctx.State.Warp.RuntimeInterface()
	}
	postUp := fmt.Sprintf("iptables -t nat -C POSTROUTING -s %s -o %s -j MASQUERADE || iptables -t nat -I POSTROUTING 1 -s %s -o %s -j MASQUERADE; iptables -C INPUT -p udp -m udp --dport %d -j ACCEPT || iptables -I INPUT 1 -p udp -m udp --dport %d -j ACCEPT; iptables -C FORWARD -i %s -j ACCEPT || iptables -I FORWARD 1 -i %s -j ACCEPT; iptables -C FORWARD -o %s -j ACCEPT || iptables -I FORWARD 1 -o %s -j ACCEPT;",
		ctx.Tunnel.IPv4Subnet, egressInterface, ctx.Tunnel.IPv4Subnet, egressInterface, ctx.Tunnel.ListenPort, ctx.Tunnel.ListenPort, ctx.Tunnel.InterfaceName, ctx.Tunnel.InterfaceName, ctx.Tunnel.InterfaceName, ctx.Tunnel.InterfaceName)
	postDown := fmt.Sprintf("while iptables -t nat -C POSTROUTING -s %s -o %s -j MASQUERADE; do iptables -t nat -D POSTROUTING -s %s -o %s -j MASQUERADE; done; while iptables -C INPUT -p udp -m udp --dport %d -j ACCEPT; do iptables -D INPUT -p udp -m udp --dport %d -j ACCEPT; done; while iptables -C FORWARD -i %s -j ACCEPT; do iptables -D FORWARD -i %s -j ACCEPT; done; while iptables -C FORWARD -o %s -j ACCEPT; do iptables -D FORWARD -o %s -j ACCEPT; done;",
		ctx.Tunnel.IPv4Subnet, egressInterface, ctx.Tunnel.IPv4Subnet, egressInterface, ctx.Tunnel.ListenPort, ctx.Tunnel.ListenPort, ctx.Tunnel.InterfaceName, ctx.Tunnel.InterfaceName, ctx.Tunnel.InterfaceName, ctx.Tunnel.InterfaceName)
	lines := []ConfigLine{
		{"PrivateKey", ctx.Tunnel.ServerPrivateKey},
		{"Address", fmt.Sprintf("%s/%d", ctx.Tunnel.ServerAddress, ones)},
		{"ListenPort", strconv.Itoa(ctx.Tunnel.ListenPort)},
		{"PreUp", ""},
		{"PostUp", postUp},
		{"PreDown", ""},
		{"PostDown", postDown},
	}
	if ctx.Tunnel.MTU > 0 {
		lines = append(lines[:3], append([]ConfigLine{{"MTU", strconv.Itoa(ctx.Tunnel.MTU)}}, lines[3:]...)...)
	}
	return lines, nil
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
		{"Endpoint", fmt.Sprintf("%s:%d", ctx.EndpointHost(), ctx.Tunnel.ListenPort)},
	}, nil
}

func appendParams(lines []ConfigLine, params config.ProtocolParams) []ConfigLine {
	return appendParamKeys(lines, params, legacyKeys)
}

func appendParamKeys(lines []ConfigLine, params config.ProtocolParams, keys []string) []ConfigLine {
	for _, k := range keys {
		if params[k] == "" {
			continue
		}
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

func u32() (uint32, error) {
	return randomUint32Below(1<<32 - 1)
}

func validateJunkAndBasePadding(params config.ProtocolParams) (int, int, error) {
	if err := validateIntParam(params, "Jc", 0, 10); err != nil {
		return 0, 0, err
	}
	jmin, err := intParam(params, "Jmin")
	if err != nil || jmin < 64 || jmin > 1024 {
		return 0, 0, fmt.Errorf("parameter Jmin must be 64..1024")
	}
	jmax, err := intParam(params, "Jmax")
	if err != nil || jmax < jmin || jmax > 1024 {
		return 0, 0, fmt.Errorf("parameter Jmax must be greater than or equal to Jmin and <= 1024")
	}
	s1, err := intParam(params, "S1")
	if err != nil || s1 < 0 || s1 > 64 {
		return 0, 0, fmt.Errorf("S1 must be 0..64")
	}
	s2, err := intParam(params, "S2")
	if err != nil || s2 < 0 || s2 > 64 {
		return 0, 0, fmt.Errorf("S2 must be 0..64")
	}
	return s1, s2, nil
}

func validateIntParam(params config.ProtocolParams, key string, min, max int) error {
	n, err := intParam(params, key)
	if err != nil || n < min || n > max {
		return fmt.Errorf("%s must be %d..%d", key, min, max)
	}
	return nil
}

func intParam(params config.ProtocolParams, key string) (int, error) {
	return strconv.Atoi(params[key])
}
