package controlpki

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

type Endpoint struct {
	BindIP     string `json:"bind_ip"`
	Advertised string `json:"advertised"`
	Port       int    `json:"port"`
}

// ValidateControllerURL shares endpoint rules between invitation admission and
// persisted node connections. It never resolves DNS or relaxes TLS verification.
func ValidateControllerURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("invalid control URL")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return errors.New("invalid control URL port")
	}
	endpoint, err := NormalizeEndpoint("127.0.0.1", u.Hostname(), port, 0)
	if err != nil || u.Host != net.JoinHostPort(endpoint.Advertised, strconv.Itoa(port)) {
		return errors.New("invalid control URL endpoint")
	}
	return nil
}

// NormalizeEndpoint accepts one literal bind address and one unambiguous SAN.
func NormalizeEndpoint(bind, advertised string, port, webPort int) (Endpoint, error) {
	if bind == "" || advertised == "" || bind != strings.TrimSpace(bind) || advertised != strings.TrimSpace(advertised) {
		return Endpoint{}, errors.New("control endpoint must contain literal addresses without surrounding whitespace")
	}
	bindAddr, err := netip.ParseAddr(bind)
	if err != nil || bindAddr.Is4In6() || bindAddr.Zone() != "" || bindAddr.IsUnspecified() || bindAddr.IsMulticast() {
		return Endpoint{}, errors.New("control bind address must be a specific IP address")
	}
	if port < 1 || port > 65535 || port == webPort || port == 80 {
		return Endpoint{}, errors.New("control port is invalid or conflicts with an existing listener")
	}
	if address, err := netip.ParseAddr(advertised); err == nil {
		if address.Is4In6() || address.Zone() != "" || address.IsUnspecified() || address.IsMulticast() {
			return Endpoint{}, errors.New("control advertised IP address is invalid")
		}
		return Endpoint{BindIP: bindAddr.String(), Advertised: address.String(), Port: port}, nil
	}
	if len(advertised) > 253 || !strings.Contains(advertised, ".") || strings.ContainsAny(advertised, ":/%*[]@") {
		return Endpoint{}, errors.New("control advertised DNS name is invalid")
	}
	labels := strings.Split(strings.ToLower(advertised), ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return Endpoint{}, errors.New("control advertised DNS name is invalid")
		}
		for _, c := range label {
			if c == '-' {
				continue
			}
			if c < 'a' || c > 'z' {
				if c < '0' || c > '9' {
					return Endpoint{}, errors.New("control advertised DNS name is invalid")
				}
			}
		}
	}
	last := labels[len(labels)-1]
	if len(last) < 2 || strings.Trim(last, "0123456789") == "" {
		return Endpoint{}, errors.New("control advertised DNS name is ambiguous")
	}
	return Endpoint{BindIP: bindAddr.String(), Advertised: strings.ToLower(advertised), Port: port}, nil
}
