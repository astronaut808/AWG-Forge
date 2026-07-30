package protocol

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/astronaut808/awg-forge/internal/config"
)

const headerProtectionKey = "HeaderProtectionKey"

const (
	awg30DefaultContentPaddingAddition = "0"
	awg30DefaultRekeyAfterTime         = "120"
	awg30DefaultRekeyTimeout           = "5"
	awg30DefaultRejectAfterTime        = "180"
	awg30DefaultKeepaliveTimeout       = "10"
	awg30DefaultMaxHandshakeAttempts   = "18"
)

var awg30Keys = append(append([]string(nil), awg20Keys...),
	"ContentPaddingAddition",
	"RekeyAfterTime",
	"RekeyTimeout",
	"RejectAfterTime",
	"KeepaliveTimeout",
	"MaxHandshakeAttempts",
)

var awg30PeerKeys = []string{"PersistentKeepalive"}

// AWG30 is experimental until the pinned runtime image uses AWG 3-capable tools.
// HeaderProtectionKey is intentionally kept in ProtocolSecrets, never ProtocolParams.
type AWG30 struct{}

func (AWG30) ID() string          { return "awg_3_0" }
func (AWG30) DisplayName() string { return "AmneziaWG 3.0 (experimental)" }
func (AWG30) Version() string     { return "3" }
func (AWG30) ParamKeys() []string {
	return append(append([]string(nil), awg30Keys...), awg30PeerKeys...)
}

func (AWG30) GenerateDefaults() (config.ProtocolParams, error) {
	params, err := AWG20{}.GenerateDefaults()
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"S1", "S2", "S3", "S4"} {
		value, err := intParam(params, key)
		if err != nil {
			return nil, err
		}
		if value < 12 {
			value, err = randomInt(12, 32)
			if err != nil {
				return nil, err
			}
			params[key] = strconv.Itoa(value)
		}
	}
	params["ContentPaddingAddition"] = awg30DefaultContentPaddingAddition
	params["RekeyAfterTime"] = awg30DefaultRekeyAfterTime
	params["RekeyTimeout"] = awg30DefaultRekeyTimeout
	params["RejectAfterTime"] = awg30DefaultRejectAfterTime
	params["KeepaliveTimeout"] = awg30DefaultKeepaliveTimeout
	params["MaxHandshakeAttempts"] = awg30DefaultMaxHandshakeAttempts
	params["PersistentKeepalive"] = ""
	return params, nil
}

func (AWG30) GenerateSecrets() (config.ProtocolSecrets, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate header protection key: %w", err)
	}
	return config.ProtocolSecrets{headerProtectionKey: base64.StdEncoding.EncodeToString(key)}, nil
}

func (AWG30) Validate(params config.ProtocolParams) error {
	for _, key := range awg20Keys {
		if _, ok := params[key]; !ok {
			return fmt.Errorf("missing protocol parameter %s", key)
		}
	}
	if err := (AWG20{}).Validate(params); err != nil {
		return err
	}
	for _, key := range []string{"S1", "S2", "S3", "S4"} {
		if err := validateIntParam(params, key, 12, 64); err != nil {
			return err
		}
	}
	for _, key := range awg30Keys[len(awg20Keys):] {
		if err := validateUintRange(key, params[key]); err != nil {
			return err
		}
	}
	if err := validateUintRange("PersistentKeepalive", params["PersistentKeepalive"]); err != nil {
		return err
	}
	return nil
}

func (AWG30) ValidateSecrets(secrets config.ProtocolSecrets) error {
	if len(secrets) != 1 || strings.TrimSpace(secrets[headerProtectionKey]) == "" {
		return fmt.Errorf("missing protocol secret %s", headerProtectionKey)
	}
	key, err := base64.StdEncoding.DecodeString(secrets[headerProtectionKey])
	if err != nil || len(key) != 32 {
		return fmt.Errorf("%s must be a base64-encoded 32-byte key", headerProtectionKey)
	}
	return nil
}

func (p AWG30) RenderServerInterface(ctx RenderContext) ([]ConfigLine, error) {
	if err := p.Validate(ctx.Tunnel.ProtocolParams); err != nil {
		return nil, err
	}
	if err := p.ValidateSecrets(ctx.Tunnel.ProtocolSecrets); err != nil {
		return nil, err
	}
	lines, err := baseInterfaceLines(ctx)
	if err != nil {
		return nil, err
	}
	lines = appendParamKeys(lines, ctx.Tunnel.ProtocolParams, awg20Keys)
	return append(lines, ConfigLine{Key: headerProtectionKey, Value: ctx.Tunnel.ProtocolSecrets[headerProtectionKey]}), nil
}

func (AWG30) RenderServerPeer(ctx RenderContext, client config.Client) ([]ConfigLine, error) {
	return Legacy10{}.RenderServerPeer(ctx, client)
}

func (p AWG30) RenderClientInterface(ctx RenderContext, client config.Client) ([]ConfigLine, error) {
	if err := p.Validate(ctx.Tunnel.ProtocolParams); err != nil {
		return nil, err
	}
	if err := p.ValidateSecrets(ctx.Tunnel.ProtocolSecrets); err != nil {
		return nil, err
	}
	lines := []ConfigLine{{Key: "PrivateKey", Value: client.PrivateKey}, {Key: "Address", Value: client.IPv4Address + "/32"}, {Key: "DNS", Value: ctx.Tunnel.DNS}}
	if ctx.Tunnel.MTU > 0 {
		lines = append(lines, ConfigLine{Key: "MTU", Value: strconv.Itoa(ctx.Tunnel.MTU)})
	}
	lines = appendParamKeys(lines, ctx.Tunnel.ProtocolParams, awg20Keys)
	lines = append(lines, ConfigLine{Key: headerProtectionKey, Value: ctx.Tunnel.ProtocolSecrets[headerProtectionKey]})
	return appendParamKeys(lines, ctx.Tunnel.ProtocolParams, awg30Keys[len(awg20Keys):]), nil
}

func (AWG30) RenderClientPeer(ctx RenderContext, client config.Client) ([]ConfigLine, error) {
	keepalive := strconv.Itoa(ctx.Tunnel.Keepalive)
	if configured := strings.TrimSpace(ctx.Tunnel.ProtocolParams["PersistentKeepalive"]); configured != "" {
		keepalive = configured
	}
	return []ConfigLine{
		{"PublicKey", ctx.Tunnel.ServerPublicKey},
		{"PresharedKey", client.PresharedKey},
		{"AllowedIPs", ctx.Tunnel.AllowedIPs},
		{"PersistentKeepalive", keepalive},
		{"Endpoint", fmt.Sprintf("%s:%d", ctx.EndpointHost(), ctx.Tunnel.ListenPort)},
	}, nil
}

func validateUintRange(key, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, "-")
	if len(parts) > 2 || len(parts) == 0 {
		return fmt.Errorf("%s must be an unsigned 32-bit value or range", key)
	}
	first, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 32)
	if err != nil {
		return fmt.Errorf("%s must be an unsigned 32-bit value or range", key)
	}
	if len(parts) == 2 {
		last, err := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 32)
		if err != nil || last < first {
			return fmt.Errorf("%s must be an ascending unsigned 32-bit range", key)
		}
	}
	return nil
}
