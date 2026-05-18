package protocol

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/astronaut808/awg-forge/internal/config"
)

const defaultDNSLikeI1 = "<r 2><b 0x8580000100010000000004796162730679616e6465780272750000010001c00c000100010000026d000457fa27d1>"
const defaultTimedNoiseI2 = "<r 8><t><r 16>"
const defaultDigitsNoiseI3 = "<rd 12><r 12>"
const defaultCharsNoiseI4 = "<rc 16><r 10>"
const defaultRandomNoiseI5 = "<r 32>"

type AWG15 struct{}

func (AWG15) ID() string          { return "awg_1_5" }
func (AWG15) DisplayName() string { return "AmneziaWG 1.5" }
func (AWG15) Version() string     { return "1.5" }

func (AWG15) GenerateDefaults() (config.ProtocolParams, error) {
	params, err := defaultLegacyParams()
	if err != nil {
		return nil, err
	}
	params["I1"] = defaultDNSLikeI1
	params["I2"] = defaultTimedNoiseI2
	params["I3"] = defaultDigitsNoiseI3
	params["I4"] = defaultCharsNoiseI4
	params["I5"] = defaultRandomNoiseI5
	return params, nil
}

func (AWG15) Validate(params config.ProtocolParams) error {
	if err := validateLegacyParams(params); err != nil {
		return err
	}
	for _, k := range []string{"I1", "I2", "I3", "I4", "I5"} {
		if err := validateSignatureParam(k, params[k]); err != nil {
			return err
		}
	}
	return nil
}

func (p AWG15) RenderServerInterface(ctx RenderContext) ([]ConfigLine, error) {
	if err := p.Validate(ctx.Tunnel.ProtocolParams); err != nil {
		return nil, err
	}
	return Legacy10{}.RenderServerInterface(ctx)
}

func (AWG15) RenderServerPeer(ctx RenderContext, client config.Client) ([]ConfigLine, error) {
	return Legacy10{}.RenderServerPeer(ctx, client)
}

func (p AWG15) RenderClientInterface(ctx RenderContext, client config.Client) ([]ConfigLine, error) {
	if err := p.Validate(ctx.Tunnel.ProtocolParams); err != nil {
		return nil, err
	}
	lines, err := Legacy10{}.RenderClientInterface(ctx, client)
	if err != nil {
		return nil, err
	}
	for _, k := range []string{"I1", "I2", "I3", "I4", "I5"} {
		if ctx.Tunnel.ProtocolParams[k] != "" {
			lines = append(lines, ConfigLine{k, ctx.Tunnel.ProtocolParams[k]})
		}
	}
	return lines, nil
}

func (AWG15) RenderClientPeer(ctx RenderContext, client config.Client) ([]ConfigLine, error) {
	return Legacy10{}.RenderClientPeer(ctx, client)
}

var signatureTokenRE = regexp.MustCompile(`^<(b 0x[0-9a-fA-F]+|r [0-9]+|rd [0-9]+|rc [0-9]+|t)>`)

func validateSignatureParam(key, value string) error {
	if value == "" {
		return nil
	}
	rest := value
	totalSize := 0
	for rest != "" {
		loc := signatureTokenRE.FindStringIndex(rest)
		if loc == nil || loc[0] != 0 {
			return fmt.Errorf("%s has invalid signature token syntax", key)
		}
		token := rest[loc[0]:loc[1]]
		size, err := signatureTokenSize(token)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		totalSize += size
		rest = strings.TrimSpace(rest[loc[1]:])
	}
	if totalSize > 1024 {
		return fmt.Errorf("%s signature packet is too large", key)
	}
	return nil
}

func signatureTokenSize(token string) (int, error) {
	body := strings.TrimSuffix(strings.TrimPrefix(token, "<"), ">")
	if body == "t" {
		return 4, nil
	}
	if strings.HasPrefix(body, "b 0x") {
		hex := strings.TrimPrefix(body, "b 0x")
		if len(hex)%2 != 0 {
			return 0, fmt.Errorf("hex blob must have even length")
		}
		return len(hex) / 2, nil
	}
	parts := strings.Split(body, " ")
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid token")
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n < 0 || n > 1024 {
		return 0, fmt.Errorf("random token size must be 0..1024")
	}
	return n, nil
}
