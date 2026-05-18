package render

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/astronaut808/awg-forge/internal/config"
)

const (
	amneziaQRMagic     uint16 = 1984
	amneziaQRChunkSize        = 850
)

func AmneziaImportConfig(state config.State, tunnel config.Tunnel, client config.Client) ([]byte, error) {
	rawConfig, err := ClientConfig(state, tunnel, client)
	if err != nil {
		return nil, err
	}
	lastConfig := map[string]any{
		"config":                rawConfig,
		"hostName":              state.ServerHost,
		"port":                  tunnel.ListenPort,
		"client_priv_key":       client.PrivateKey,
		"client_ip":             client.IPv4Address + "/32",
		"server_pub_key":        tunnel.ServerPublicKey,
		"allowed_ips":           splitAllowedIPs(tunnel.AllowedIPs),
		"persistent_keep_alive": tunnel.Keepalive,
	}
	if client.PresharedKey != "" {
		lastConfig["psk_key"] = client.PresharedKey
	}
	if tunnel.MTU > 0 {
		lastConfig["mtu"] = tunnel.MTU
	}
	for key, value := range tunnel.ProtocolParams {
		if value != "" {
			lastConfig[key] = value
		}
	}
	lastConfigBytes, err := json.Marshal(lastConfig)
	if err != nil {
		return nil, err
	}
	awgConfig := map[string]any{
		"last_config":        string(lastConfigBytes),
		"isThirdPartyConfig": true,
		"port":               strconv.Itoa(tunnel.ListenPort),
		"transport_proto":    "udp",
	}
	if tunnel.ProtocolProfileID == "awg_1_5" {
		awgConfig["protocol_version"] = "1.5"
	}
	if tunnel.ProtocolProfileID == "awg_2_0" {
		awgConfig["protocol_version"] = "2"
	}
	payload := map[string]any{
		"containers": []map[string]any{{
			"container": "amnezia-awg",
			"awg":       awgConfig,
		}},
		"defaultContainer": "amnezia-awg",
		"description":      client.Name,
		"hostName":         state.ServerHost,
	}
	dns := splitAllowedIPs(tunnel.DNS)
	if len(dns) > 0 {
		payload["dns1"] = dns[0]
	}
	if len(dns) > 1 {
		payload["dns2"] = dns[1]
	}
	return json.Marshal(payload)
}

func AmneziaQRTexts(payload []byte) ([]string, error) {
	compressed, err := qCompress(payload)
	if err != nil {
		return nil, err
	}
	if len(compressed) <= amneziaQRChunkSize {
		return []string{base64.RawURLEncoding.EncodeToString(compressed)}, nil
	}
	chunks := splitChunks(compressed, amneziaQRChunkSize)
	out := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		var buf bytes.Buffer
		_ = binary.Write(&buf, binary.BigEndian, amneziaQRMagic)
		buf.WriteByte(byte(len(chunks)))
		buf.WriteByte(byte(i))
		_ = binary.Write(&buf, binary.BigEndian, uint32(len(chunk)))
		buf.Write(chunk)
		out = append(out, base64.RawURLEncoding.EncodeToString(buf.Bytes()))
	}
	return out, nil
}

func qCompress(payload []byte) ([]byte, error) {
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(payload)))
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func splitChunks(data []byte, size int) [][]byte {
	var chunks [][]byte
	for len(data) > 0 {
		n := size
		if len(data) < n {
			n = len(data)
		}
		chunks = append(chunks, data[:n])
		data = data[n:]
	}
	return chunks
}

func splitAllowedIPs(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
