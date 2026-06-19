package warp

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/astronaut808/awg-forge/internal/config"
)

const (
	DefaultInterfaceName = "warp0"
	RoutingTable         = "200"
)

type TunnelRoute struct {
	InterfaceName string
	Subnet        string
}

func ParseWireGuardConfig(text string) (config.Warp, error) {
	var section string
	out := config.Warp{
		InterfaceName:       DefaultInterfaceName,
		MTU:                 1280,
		PersistentKeepalive: 25,
	}

	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch section + "." + strings.ToLower(key) {
		case "interface.privatekey":
			out.PrivateKey = value
		case "interface.address":
			out.AddressV4 = firstIPv4Address(value)
		case "interface.mtu":
			if n, err := strconv.Atoi(value); err == nil {
				out.MTU = n
			}
		case "peer.publickey":
			out.PeerPublicKey = value
		case "peer.presharedkey":
			out.PresharedKey = value
		case "peer.endpoint":
			out.Endpoint = value
		case "peer.persistentkeepalive":
			if n, err := strconv.Atoi(value); err == nil {
				out.PersistentKeepalive = n
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return config.Warp{}, err
	}
	if err := Validate(out); err != nil {
		return config.Warp{}, err
	}
	return out, nil
}

func Validate(w config.Warp) error {
	if strings.TrimSpace(w.PrivateKey) == "" {
		return errors.New("WARP private key is required")
	}
	if strings.TrimSpace(w.PeerPublicKey) == "" {
		return errors.New("WARP peer public key is required")
	}
	if strings.TrimSpace(w.Endpoint) == "" {
		return errors.New("WARP endpoint is required")
	}
	if strings.TrimSpace(w.AddressV4) == "" {
		return errors.New("WARP IPv4 address is required")
	}
	if ip := net.ParseIP(strings.TrimSuffix(w.AddressV4, "/32")); ip == nil || ip.To4() == nil {
		return errors.New("WARP IPv4 address is invalid")
	}
	if w.MTU != 0 && (w.MTU < 576 || w.MTU > 1500) {
		return errors.New("WARP MTU must be between 576 and 1500")
	}
	if w.PersistentKeepalive < 0 || w.PersistentKeepalive > 65535 {
		return errors.New("WARP persistent keepalive must be between 0 and 65535")
	}
	return nil
}

func RenderConfig(w config.Warp, routes []TunnelRoute) (string, error) {
	if err := Validate(w); err != nil {
		return "", err
	}
	if len(routes) == 0 {
		return "", errors.New("at least one WARP tunnel route is required")
	}
	interfaceName := w.RuntimeInterface()
	var postUp []string
	var postDown []string
	for _, route := range routes {
		if route.InterfaceName == "" || route.Subnet == "" {
			return "", errors.New("WARP route interface and subnet are required")
		}
		if _, _, err := net.ParseCIDR(route.Subnet); err != nil {
			return "", fmt.Errorf("invalid WARP route subnet %q: %w", route.Subnet, err)
		}
		postUp = append(postUp,
			fmt.Sprintf("ip route replace %s dev %s table %s", route.Subnet, route.InterfaceName, RoutingTable),
			fmt.Sprintf("ip rule del from %s lookup %s 2>/dev/null || true", route.Subnet, RoutingTable),
			fmt.Sprintf("ip rule add from %s lookup %s", route.Subnet, RoutingTable),
			fmt.Sprintf("iptables -t nat -C POSTROUTING -s %s -o %%i -j MASQUERADE || iptables -t nat -I POSTROUTING 1 -s %s -o %%i -j MASQUERADE", route.Subnet, route.Subnet),
		)
		postDown = append(postDown,
			fmt.Sprintf("ip rule del from %s lookup %s 2>/dev/null || true", route.Subnet, RoutingTable),
			fmt.Sprintf("ip route del %s dev %s table %s 2>/dev/null || true", route.Subnet, route.InterfaceName, RoutingTable),
			fmt.Sprintf("while iptables -t nat -C POSTROUTING -s %s -o %%i -j MASQUERADE; do iptables -t nat -D POSTROUTING -s %s -o %%i -j MASQUERADE; done", route.Subnet, route.Subnet),
		)
	}

	address := strings.TrimSuffix(w.AddressV4, "/32") + "/32"
	var b strings.Builder
	b.WriteString("[Interface]\n")
	writeLine(&b, "PrivateKey", w.PrivateKey)
	writeLine(&b, "Address", address)
	if w.MTU > 0 {
		writeLine(&b, "MTU", strconv.Itoa(w.MTU))
	}
	writeLine(&b, "Table", RoutingTable)
	writeLine(&b, "PostUp", strings.Join(postUp, "; ")+";")
	writeLine(&b, "PostDown", strings.Join(postDown, "; ")+";")
	b.WriteString("\n[Peer]\n")
	writeLine(&b, "PublicKey", w.PeerPublicKey)
	if w.PresharedKey != "" {
		writeLine(&b, "PresharedKey", w.PresharedKey)
	}
	writeLine(&b, "AllowedIPs", "0.0.0.0/0")
	if w.PersistentKeepalive > 0 {
		writeLine(&b, "PersistentKeepalive", strconv.Itoa(w.PersistentKeepalive))
	}
	writeLine(&b, "Endpoint", w.Endpoint)
	_ = interfaceName
	return b.String(), nil
}

func RoutesForState(state config.State) []TunnelRoute {
	var routes []TunnelRoute
	for _, tunnel := range state.Tunnels {
		if tunnel.Enabled && tunnel.EgressMode == config.EgressWarp {
			routes = append(routes, TunnelRoute{InterfaceName: tunnel.InterfaceName, Subnet: tunnel.IPv4Subnet})
		}
	}
	return routes
}

func EnabledTunnelCount(state config.State) int {
	count := 0
	for _, tunnel := range state.Tunnels {
		if tunnel.Enabled && tunnel.EgressMode == config.EgressWarp {
			count++
		}
	}
	return count
}

func firstIPv4Address(value string) string {
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		host := strings.TrimSuffix(part, "/32")
		if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
			return host
		}
	}
	return ""
}

func writeLine(b *strings.Builder, key, value string) {
	b.WriteString(key)
	b.WriteString(" = ")
	b.WriteString(value)
	b.WriteByte('\n')
}
