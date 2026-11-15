package server

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"

	"github.com/astronaut808/awg-forge/internal/app"
)

const (
	amneziaVPNQRPackMagic     uint32 = 0x07C00100
	amneziaVPNQRPackHeaderLen        = 12
	amneziaVPNQRMaxInputBytes        = 1 << 20
)

type amneziaVPNConfig struct {
	Containers       []amneziaVPNContainer `json:"containers"`
	DefaultContainer string                `json:"defaultContainer"`
	Description      string                `json:"description"`
	DNS1             string                `json:"dns1,omitempty"`
	DNS2             string                `json:"dns2,omitempty"`
	HostName         string                `json:"hostName"`
}

type amneziaVPNContainer struct {
	AWG       amneziaVPNAWG `json:"awg"`
	Container string        `json:"container"`
}

type amneziaVPNAWG struct {
	IsThirdPartyConfig bool   `json:"isThirdPartyConfig"`
	LastConfig         string `json:"last_config"`
	Port               string `json:"port"`
	ProtocolVersion    string `json:"protocol_version"`
	TransportProto     string `json:"transport_proto"`
}

type amneziaVPNLastConfig struct {
	AllowedIPs          []string `json:"allowed_ips"`
	ClientIP            string   `json:"client_ip"`
	ClientPrivateKey    string   `json:"client_priv_key"`
	Config              string   `json:"config"`
	HostName            string   `json:"hostName"`
	MTU                 string   `json:"mtu,omitempty"`
	PersistentKeepalive string   `json:"persistent_keep_alive"`
	Port                int      `json:"port"`
	PresharedKey        string   `json:"psk_key"`
	ServerPublicKey     string   `json:"server_pub_key"`

	Jc   string `json:"Jc,omitempty"`
	Jmin string `json:"Jmin,omitempty"`
	Jmax string `json:"Jmax,omitempty"`
	S1   string `json:"S1,omitempty"`
	S2   string `json:"S2,omitempty"`
	S3   string `json:"S3,omitempty"`
	S4   string `json:"S4,omitempty"`
	H1   string `json:"H1,omitempty"`
	H2   string `json:"H2,omitempty"`
	H3   string `json:"H3,omitempty"`
	H4   string `json:"H4,omitempty"`
	I1   string `json:"I1,omitempty"`
	I2   string `json:"I2,omitempty"`
	I3   string `json:"I3,omitempty"`
	I4   string `json:"I4,omitempty"`
	I5   string `json:"I5,omitempty"`

	HeaderProtectionKey    string `json:"HeaderProtectionKey,omitempty"`
	ContentPaddingAddition string `json:"ContentPaddingAddition,omitempty"`
	RekeyAfterTime         string `json:"RekeyAfterTime,omitempty"`
	RekeyTimeout           string `json:"RekeyTimeout,omitempty"`
	RejectAfterTime        string `json:"RejectAfterTime,omitempty"`
	KeepaliveTimeout       string `json:"KeepaliveTimeout,omitempty"`
	MaxHandshakeAttempts   string `json:"MaxHandshakeAttempts,omitempty"`
	RandomTrailers         string `json:"RandomTrailers,omitempty"`
	DisableCookies         string `json:"DisableCookies,omitempty"`
}

func buildAmneziaVPNClientConfig(ctx app.ClientExportContext) ([]byte, error) {
	endpoint, err := ctx.Tunnel.ClientEndpoint(ctx.ServerHost, ctx.Client)
	if err != nil {
		return nil, err
	}
	host := strings.TrimSpace(endpoint.Host)
	if host == "" {
		return nil, fmt.Errorf("empty endpoint host")
	}

	// Do not change these JSON types casually.
	// AmneziaVPN imports the QR successfully even when visible .conf text matches,
	// but it builds the runnable profile from structured last_config fields.
	// In particular, port must stay a JSON number and allowed_ips must stay an
	// array; changing them to strings can produce profiles that fail to connect.
	// The types intentionally match the wg-easy AmneziaVPN QR reference and the
	// AmneziaVPN compatibility investigation.
	port := strconv.Itoa(endpoint.Port)
	params := ctx.Tunnel.ProtocolParams
	persistentKeepalive := strconv.Itoa(ctx.Tunnel.Keepalive)
	if ctx.Tunnel.ProtocolProfileID == "awg_3" {
		persistentKeepalive = params["PersistentKeepalive"]
	}
	last := amneziaVPNLastConfig{
		AllowedIPs:          []string{ctx.Tunnel.AllowedIPs},
		ClientIP:            ctx.Client.IPv4Address + "/32",
		ClientPrivateKey:    ctx.Client.PrivateKey,
		Config:              ctx.RenderedConf,
		HostName:            host,
		PersistentKeepalive: persistentKeepalive,
		Port:                endpoint.Port,
		PresharedKey:        ctx.Client.PresharedKey,
		ServerPublicKey:     ctx.Tunnel.ServerPublicKey,
		Jc:                  params["Jc"],
		Jmin:                params["Jmin"],
		Jmax:                params["Jmax"],
		S1:                  params["S1"],
		S2:                  params["S2"],
		S3:                  params["S3"],
		S4:                  params["S4"],
		H1:                  params["H1"],
		H2:                  params["H2"],
		H3:                  params["H3"],
		H4:                  params["H4"],
		I1:                  params["I1"],
		I2:                  params["I2"],
		I3:                  params["I3"],
		I4:                  params["I4"],
		I5:                  params["I5"],
	}
	if ctx.Tunnel.ProtocolProfileID == "awg_3" {
		last.HeaderProtectionKey = ctx.Tunnel.ProtocolSecrets.HeaderProtectionKey
		last.ContentPaddingAddition = params["ContentPaddingAddition"]
		last.RekeyAfterTime = params["RekeyAfterTime"]
		last.RekeyTimeout = params["RekeyTimeout"]
		last.RejectAfterTime = params["RejectAfterTime"]
		last.KeepaliveTimeout = params["KeepaliveTimeout"]
		last.MaxHandshakeAttempts = params["MaxHandshakeAttempts"]
		last.RandomTrailers = enabledAWGToggle(params["RandomTrailers"])
		last.DisableCookies = enabledAWGToggle(params["DisableCookies"])
	}
	if ctx.Tunnel.MTU > 0 {
		last.MTU = strconv.Itoa(ctx.Tunnel.MTU)
	}
	lastConfigJSON, err := json.Marshal(last)
	if err != nil {
		return nil, err
	}

	protocolVersion := "1"
	if ctx.Tunnel.ProtocolProfileID == "awg_3" {
		protocolVersion = "3.1"
	} else if ctx.Tunnel.ProtocolProfileID == "awg_2_0" || (params["S3"] != "" && params["S4"] != "") {
		protocolVersion = "2"
	}
	outer := amneziaVPNConfig{
		Containers: []amneziaVPNContainer{{
			AWG: amneziaVPNAWG{
				IsThirdPartyConfig: true,
				LastConfig:         string(lastConfigJSON),
				Port:               port,
				ProtocolVersion:    protocolVersion,
				TransportProto:     "udp",
			},
			Container: "amnezia-awg",
		}},
		DefaultContainer: "amnezia-awg",
		Description:      ctx.Client.Name,
		HostName:         host,
	}
	outer.DNS1, outer.DNS2 = firstIPv4DNS(ctx.Tunnel.DNS)
	return json.Marshal(outer)
}

func enabledAWGToggle(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "on") {
		return "on"
	}
	return ""
}

func buildAmneziaVPNQRPayload(ctx app.ClientExportContext) (string, error) {
	jsonBytes, err := buildAmneziaVPNClientConfig(ctx)
	if err != nil {
		return "", err
	}
	return buildAmneziaVPNQRPack(jsonBytes)
}

func buildAmneziaVPNQRPack(jsonBytes []byte) (string, error) {
	if len(jsonBytes) == 0 {
		return "", fmt.Errorf("empty AmneziaVPN config")
	}
	if len(jsonBytes) > amneziaVPNQRMaxInputBytes {
		return "", fmt.Errorf("amneziavpn config is too large")
	}
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write(jsonBytes); err != nil {
		_ = zw.Close()
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}

	zlibBytes := compressed.Bytes()
	zlibLen := len(zlibBytes)
	jsonLen := len(jsonBytes)
	if zlibLen > amneziaVPNQRMaxInputBytes {
		return "", fmt.Errorf("compressed amneziavpn config is too large")
	}
	if zlibLen > math.MaxInt-amneziaVPNQRPackHeaderLen {
		return "", fmt.Errorf("amneziavpn QR payload is too large")
	}
	if uint64(zlibLen) > math.MaxUint32-4 || uint64(jsonLen) > math.MaxUint32 {
		return "", fmt.Errorf("amneziavpn QR payload exceeds qCompress length limit")
	}
	buf := make([]byte, amneziaVPNQRPackHeaderLen+zlibLen)
	binary.BigEndian.PutUint32(buf[0:4], amneziaVPNQRPackMagic)
	binary.BigEndian.PutUint32(buf[4:8], uint32(zlibLen)+4)
	binary.BigEndian.PutUint32(buf[8:amneziaVPNQRPackHeaderLen], uint32(jsonLen))
	copy(buf[amneziaVPNQRPackHeaderLen:], zlibBytes)
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func firstIPv4DNS(raw string) (string, string) {
	values := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, 2)
	for _, value := range values {
		ip := net.ParseIP(strings.TrimSpace(value))
		if ip == nil || ip.To4() == nil {
			continue
		}
		out = append(out, ip.String())
		if len(out) == 2 {
			return out[0], out[1]
		}
	}
	if len(out) == 1 {
		return out[0], ""
	}
	return "", ""
}
