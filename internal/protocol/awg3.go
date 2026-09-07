package protocol

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/keys"
)

var awg3Keys = []string{
	"Jc", "Jmin", "Jmax", "S1", "S2", "S3", "S4", "H1", "H2", "H3", "H4",
	"I1", "I2", "I3", "I4", "I5",
	"ContentPaddingAddition", "RekeyAfterTime", "RekeyTimeout", "RejectAfterTime",
	"KeepaliveTimeout", "MaxHandshakeAttempts", "PersistentKeepalive",
	"RandomTrailers", "DisableCookies",
}

var awg3ServerParamKeys = []string{
	"Jc", "Jmin", "Jmax", "S1", "S2", "S3", "S4", "H1", "H2", "H3", "H4",
	"ContentPaddingAddition", "RekeyAfterTime", "RekeyTimeout", "RejectAfterTime",
	"KeepaliveTimeout", "MaxHandshakeAttempts",
}

var awg3ClientParamKeys = []string{
	"Jc", "Jmin", "Jmax", "S1", "S2", "S3", "S4", "H1", "H2", "H3", "H4",
	"I1", "I2", "I3", "I4", "I5",
	"ContentPaddingAddition", "RekeyAfterTime", "RekeyTimeout", "RejectAfterTime",
	"KeepaliveTimeout", "MaxHandshakeAttempts",
}

var awg3ToggleKeys = []string{"RandomTrailers", "DisableCookies"}

type AWG3 struct{}

func (AWG3) ID() string          { return "awg_3" }
func (AWG3) DisplayName() string { return "AmneziaWG 3.x" }
func (AWG3) Version() string     { return "3.x" }
func (AWG3) ParameterKeys() []string {
	return cloneParameterKeys(awg3Keys)
}

func (AWG3) GenerateDefaults() (config.ProtocolParams, error) {
	i1, err := defaultQUICLikeI1()
	if err != nil {
		return nil, err
	}
	jc, err := randomInt(4, 6)
	if err != nil {
		return nil, err
	}
	s1, err := randomInt(12, 149)
	if err != nil {
		return nil, err
	}
	s2, err := randomInt(12, 149)
	if err != nil {
		return nil, err
	}
	s3, err := randomInt(12, 63)
	if err != nil {
		return nil, err
	}
	for awg3DefaultPacketSizesConflict(s1, s2, s3, 12) {
		s2, err = randomInt(12, 149)
		if err != nil {
			return nil, err
		}
		s3, err = randomInt(12, 63)
		if err != nil {
			return nil, err
		}
	}

	return config.ProtocolParams{
		"Jc":                     strconv.Itoa(jc),
		"Jmin":                   "10",
		"Jmax":                   "50",
		"S1":                     strconv.Itoa(s1),
		"S2":                     strconv.Itoa(s2),
		"S3":                     strconv.Itoa(s3),
		"S4":                     "12",
		"H1":                     "1",
		"H2":                     "2",
		"H3":                     "3",
		"H4":                     "4",
		"I1":                     i1,
		"I2":                     "",
		"I3":                     "",
		"I4":                     "",
		"I5":                     "",
		"ContentPaddingAddition": "10-100",
		"RekeyAfterTime":         "100-120",
		"RekeyTimeout":           "3-7",
		"RejectAfterTime":        "150-180",
		"KeepaliveTimeout":       "5-15",
		"MaxHandshakeAttempts":   "15-20",
		"PersistentKeepalive":    "25-35",
		"RandomTrailers":         "off",
		"DisableCookies":         "off",
	}, nil
}

func (AWG3) GenerateSecrets() (config.ProtocolSecrets, error) {
	key, _, err := keys.PrivateKey()
	if err != nil {
		return config.ProtocolSecrets{}, err
	}
	return config.ProtocolSecrets{HeaderProtectionKey: key}, nil
}

func (AWG3) Validate(params config.ProtocolParams) error {
	for _, key := range awg3Keys {
		if _, ok := params[key]; !ok {
			return fmt.Errorf("missing protocol parameter %s", key)
		}
	}
	if err := validateAWG3JunkParams(params); err != nil {
		return err
	}
	s1, err := awg3Padding(params, "S1")
	if err != nil {
		return err
	}
	s2, err := awg3Padding(params, "S2")
	if err != nil {
		return err
	}
	s3, err := awg3Padding(params, "S3")
	if err != nil {
		return err
	}
	s4, err := awg3Padding(params, "S4")
	if err != nil {
		return err
	}
	if awg3PacketSizesCollide(s1, s2, s3, s4) {
		return fmt.Errorf("S1-S4 produce colliding packet sizes")
	}
	if err := validateHeaderRanges(params); err != nil {
		return err
	}
	for _, key := range signatureKeys {
		if err := validateSignatureParam(key, params[key]); err != nil {
			return err
		}
	}
	for _, key := range []string{"ContentPaddingAddition", "RekeyAfterTime", "RekeyTimeout", "RejectAfterTime", "KeepaliveTimeout", "MaxHandshakeAttempts", "PersistentKeepalive"} {
		if err := validateUint16Range(key, params[key]); err != nil {
			return err
		}
	}
	for _, key := range []string{"RandomTrailers", "DisableCookies"} {
		if err := validateAWG3Bool(key, params[key]); err != nil {
			return err
		}
	}
	return nil
}

func (AWG3) ValidateSecrets(secrets config.ProtocolSecrets) error {
	key, err := base64.StdEncoding.DecodeString(secrets.HeaderProtectionKey)
	if err != nil || len(key) != 32 {
		return fmt.Errorf("HeaderProtectionKey must be a base64-encoded 32-byte key")
	}
	return nil
}

func (p AWG3) RenderServerInterface(ctx RenderContext) ([]ConfigLine, error) {
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
	lines = appendParamKeys(lines, ctx.Tunnel.ProtocolParams, awg3ServerParamKeys)
	for _, key := range awg3ToggleKeys {
		lines = append(lines, ConfigLine{key, strings.ToLower(strings.TrimSpace(ctx.Tunnel.ProtocolParams[key]))})
	}
	lines = append(lines, ConfigLine{"HeaderProtectionKey", ctx.Tunnel.ProtocolSecrets.HeaderProtectionKey})
	for _, key := range signatureKeys {
		if value := ctx.Tunnel.ProtocolParams[key]; value != "" {
			lines = append(lines, ConfigLine{"# " + key, value})
		}
	}
	return lines, nil
}

func (AWG3) RenderServerPeer(ctx RenderContext, client config.Client) ([]ConfigLine, error) {
	return Legacy10{}.RenderServerPeer(ctx, client)
}

func (p AWG3) RenderClientInterface(ctx RenderContext, client config.Client) ([]ConfigLine, error) {
	if err := p.Validate(ctx.Tunnel.ProtocolParams); err != nil {
		return nil, err
	}
	if err := p.ValidateSecrets(ctx.Tunnel.ProtocolSecrets); err != nil {
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
	lines = appendParamKeys(lines, ctx.Tunnel.ProtocolParams, awg3ClientParamKeys)
	for _, key := range awg3ToggleKeys {
		if strings.EqualFold(strings.TrimSpace(ctx.Tunnel.ProtocolParams[key]), "on") {
			lines = append(lines, ConfigLine{key, "on"})
		}
	}
	return append(lines, ConfigLine{"HeaderProtectionKey", ctx.Tunnel.ProtocolSecrets.HeaderProtectionKey}), nil
}

func (AWG3) RenderClientPeer(ctx RenderContext, client config.Client) ([]ConfigLine, error) {
	return []ConfigLine{
		{"PublicKey", ctx.Tunnel.ServerPublicKey},
		{"PresharedKey", client.PresharedKey},
		{"AllowedIPs", ctx.Tunnel.AllowedIPs},
		{"PersistentKeepalive", ctx.Tunnel.ProtocolParams["PersistentKeepalive"]},
		{"Endpoint", fmt.Sprintf("%s:%d", ctx.EndpointHost(), ctx.Tunnel.ListenPort)},
	}, nil
}

func validateAWG3Bool(key, value string) error {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "on") || strings.EqualFold(value, "off") {
		return nil
	}
	return fmt.Errorf("%s must be on or off", key)
}

func validateAWG3JunkParams(params config.ProtocolParams) error {
	jc, err := awg3Uint16(params, "Jc")
	if err != nil {
		return err
	}
	if jc == 0 {
		return fmt.Errorf("protocol parameter Jc must be greater than zero")
	}
	jmin, err := awg3Uint16(params, "Jmin")
	if err != nil {
		return err
	}
	jmax, err := awg3Uint16(params, "Jmax")
	if err != nil {
		return err
	}
	if jmin > jmax {
		return fmt.Errorf("protocol parameter Jmin must be less than or equal to Jmax")
	}
	return nil
}

func awg3Padding(params config.ProtocolParams, key string) (int, error) {
	value, err := awg3Uint16(params, key)
	if err != nil {
		return 0, err
	}
	if value < 12 {
		return 0, fmt.Errorf("%s must be at least 12", key)
	}
	return value, nil
}

func awg3Uint16(params config.ProtocolParams, key string) (int, error) {
	value, err := strconv.ParseUint(strings.TrimSpace(params[key]), 10, 16)
	if err != nil {
		return 0, fmt.Errorf("%s must be a uint16 value", key)
	}
	return int(value), nil
}

func awg3PacketSizesCollide(s1, s2, s3, s4 int) bool {
	sizes := []int{148 + s1, 92 + s2, 64 + s3, 32 + s4}
	seen := make(map[int]struct{}, len(sizes))
	for _, size := range sizes {
		if _, ok := seen[size]; ok {
			return true
		}
		seen[size] = struct{}{}
	}
	return false
}

func awg3DefaultPacketSizesConflict(s1, s2, s3, s4 int) bool {
	return s2 == s1 || s2 == s4 || 148+s1 == 92+s2 ||
		s3 == s1 || s3 == s2 || s3 == s4 ||
		148+s1 == 64+s3 || 92+s2 == 64+s3
}

func validateUint16Range(key, value string) error {
	parts := strings.Split(value, "-")
	if len(parts) == 0 || len(parts) > 2 || strings.TrimSpace(parts[0]) == "" {
		return fmt.Errorf("%s must be a uint16 value or range", key)
	}
	start, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 16)
	if err != nil {
		return fmt.Errorf("%s must be a uint16 value or range", key)
	}
	if len(parts) == 1 {
		return nil
	}
	end, err := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 16)
	if err != nil || start > end {
		return fmt.Errorf("%s must be an ascending uint16 range", key)
	}
	return nil
}
