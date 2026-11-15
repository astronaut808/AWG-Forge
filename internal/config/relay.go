package config

import (
	"errors"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var relayIDPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)
var dnsLabelPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// Relay describes one externally installed UDP forwarding rule for a tunnel.
type Relay struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	TargetIPv4 string `json:"target_ipv4,omitempty"`
	TargetHost string `json:"target_host,omitempty"`
}

func (r Relay) Target() string {
	if r.TargetHost != "" {
		return r.TargetHost
	}
	return r.TargetIPv4
}

type Endpoint struct {
	Host string
	Port int
}

func (e Endpoint) String() string { return e.Host + ":" + strconv.Itoa(e.Port) }

func (r Relay) Validate() error {
	if !relayIDPattern.MatchString(r.ID) {
		return errors.New("invalid relay ID")
	}
	if r.Name == "" || len(r.Name) > 64 || strings.ContainsFunc(r.Name, unicode.IsControl) {
		return errors.New("relay name must contain 1 to 64 bytes without control characters")
	}
	if r.Port < 1 || r.Port > 65535 || !validRelayHost(r.Host) {
		return errors.New("relay requires a DNS name or IPv4 address and a valid UDP port")
	}
	if r.TargetHost != "" {
		if r.TargetIPv4 != "" || !IsRelayDNSName(r.TargetHost) {
			return errors.New("relay target must be a DNS name or a unicast IPv4 address")
		}
		return nil
	}
	ip, err := netip.ParseAddr(r.TargetIPv4)
	if err != nil || !IsRelayIPv4(ip) {
		return errors.New("relay target must be a unicast IPv4 address")
	}
	return nil
}

func IsRelayIPv4(ip netip.Addr) bool {
	if !ip.Is4() || !ip.IsGlobalUnicast() {
		return false
	}
	first := ip.As4()[0]
	return first > 0 && first < 224
}

func validRelayHost(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		return IsRelayIPv4(ip)
	}
	return IsRelayDNSName(host)
}

func IsRelayDNSName(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if len(host) > 253 || !strings.ContainsAny(host, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if !dnsLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}

func (t Tunnel) RelayByID(id string) (Relay, bool) {
	for _, relay := range t.Relays {
		if relay.ID == id {
			return relay, true
		}
	}
	return Relay{}, false
}

func (t Tunnel) ClientEndpoint(serverHost string, c Client) (Endpoint, error) {
	if c.RelayID == "" {
		host := strings.TrimSpace(t.ServerHost)
		if host == "" {
			host = serverHost
		}
		return Endpoint{Host: host, Port: t.ListenPort}, nil
	}
	relay, ok := t.RelayByID(c.RelayID)
	if !ok {
		return Endpoint{}, errors.New("client relay is not configured for this tunnel")
	}
	if err := relay.Validate(); err != nil {
		return Endpoint{}, err
	}
	return Endpoint{Host: relay.Host, Port: relay.Port}, nil
}

func (t Tunnel) NeedsClientConfig(c Client) bool {
	return (t.ConfigRevision > 0 && c.ConfigRevision < t.ConfigRevision) || c.DeliveredEndpointRevision != c.EndpointRevision
}

func ValidateRelays(state State) error {
	ids := make(map[string]bool)
	endpoints := make(map[Endpoint]bool)
	for _, tunnel := range state.Tunnels {
		for _, relay := range tunnel.Relays {
			if err := relay.Validate(); err != nil {
				return err
			}
			endpoint := Endpoint{Host: strings.ToLower(strings.TrimSuffix(relay.Host, ".")), Port: relay.Port}
			if ids[relay.ID] || endpoints[endpoint] {
				return errors.New("duplicate relay ID or public endpoint")
			}
			ids[relay.ID] = true
			endpoints[endpoint] = true
		}
		for _, client := range tunnel.Clients {
			if _, err := tunnel.ClientEndpoint(state.ServerHost, client); err != nil {
				return err
			}
		}
	}
	return nil
}
