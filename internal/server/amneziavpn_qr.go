package server

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/astronaut808/awg-forge/internal/app"
)

const amneziaVPNQRPackMagic uint32 = 0x07C00100

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
	AllowedIPs          string `json:"allowed_ips"`
	ClientIP            string `json:"client_ip"`
	ClientPrivateKey    string `json:"client_priv_key"`
	Config              string `json:"config"`
	HostName            string `json:"hostName"`
	MTU                 string `json:"mtu,omitempty"`
	PersistentKeepalive string `json:"persistent_keep_alive"`
	Port                string `json:"port"`
	PresharedKey        string `json:"psk_key"`
	ServerPublicKey     string `json:"server_pub_key"`

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
}

func buildAmneziaVPNClientConfig(ctx app.ClientExportContext) ([]byte, error) {
	host := strings.TrimSpace(ctx.Tunnel.ServerHost)
	if host == "" {
		host = strings.TrimSpace(ctx.ServerHost)
	}
	if host == "" {
		return nil, fmt.Errorf("empty endpoint host")
	}

	port := strconv.Itoa(ctx.Tunnel.ListenPort)
	params := ctx.Tunnel.ProtocolParams
	last := amneziaVPNLastConfig{
		AllowedIPs:          ctx.Tunnel.AllowedIPs,
		ClientIP:            ctx.Client.IPv4Address + "/32",
		ClientPrivateKey:    ctx.Client.PrivateKey,
		Config:              ctx.RenderedConf,
		HostName:            host,
		PersistentKeepalive: strconv.Itoa(ctx.Tunnel.Keepalive),
		Port:                port,
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
	if ctx.Tunnel.MTU > 0 {
		last.MTU = strconv.Itoa(ctx.Tunnel.MTU)
	}
	lastConfigJSON, err := json.Marshal(last)
	if err != nil {
		return nil, err
	}

	protocolVersion := "1"
	if ctx.Tunnel.ProtocolProfileID == "awg_2_0" || (params["S3"] != "" && params["S4"] != "") {
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
	buf := make([]byte, 12+len(zlibBytes))
	binary.BigEndian.PutUint32(buf[0:4], amneziaVPNQRPackMagic)
	binary.BigEndian.PutUint32(buf[4:8], uint32(len(zlibBytes)+4))
	binary.BigEndian.PutUint32(buf[8:12], uint32(len(jsonBytes)))
	copy(buf[12:], zlibBytes)
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
