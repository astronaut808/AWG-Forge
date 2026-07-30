package protocol

import (
	"fmt"
	"strings"

	"github.com/astronaut808/awg-forge/internal/config"
)

type ConfigLine struct {
	Key   string
	Value string
}

type RenderContext struct {
	State  config.State
	Tunnel config.Tunnel
}

func (ctx RenderContext) EndpointHost() string {
	if host := strings.TrimSpace(ctx.Tunnel.ServerHost); host != "" {
		return host
	}
	return ctx.State.ServerHost
}

type ProtocolProfile interface {
	ID() string
	DisplayName() string
	Version() string
	GenerateDefaults() (config.ProtocolParams, error)
	Validate(config.ProtocolParams) error
	RenderServerInterface(RenderContext) ([]ConfigLine, error)
	RenderServerPeer(RenderContext, config.Client) ([]ConfigLine, error)
	RenderClientInterface(RenderContext, config.Client) ([]ConfigLine, error)
	RenderClientPeer(RenderContext, config.Client) ([]ConfigLine, error)
}

type SecretProfile interface {
	GenerateSecrets() (config.ProtocolSecrets, error)
	ValidateSecrets(config.ProtocolSecrets) error
}

type ParamKeyProfile interface {
	ParamKeys() []string
}

func GenerateSecrets(p ProtocolProfile) (config.ProtocolSecrets, error) {
	if p, ok := p.(SecretProfile); ok {
		return p.GenerateSecrets()
	}
	return config.ProtocolSecrets{}, nil
}

func ValidateSecrets(p ProtocolProfile, secrets config.ProtocolSecrets) error {
	if p, ok := p.(SecretProfile); ok {
		return p.ValidateSecrets(secrets)
	}
	if len(secrets) != 0 {
		return fmt.Errorf("protocol profile %q does not support protocol secrets", p.ID())
	}
	return nil
}

func ParamKeys(p ProtocolProfile) []string {
	if p, ok := p.(ParamKeyProfile); ok {
		return append([]string(nil), p.ParamKeys()...)
	}
	return nil
}

func IsExperimental(id string) bool {
	return id == "awg_3_0"
}

func SanitizeRuntimeOutput(output string) string {
	lines := strings.Split(output, "\n")
	filtered := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "header protection key:") {
			continue
		}
		filtered = append(filtered, line)
	}
	return strings.Join(filtered, "\n")
}

func ByID(id string) (ProtocolProfile, bool) {
	switch id {
	case "awg_legacy_1_0":
		return Legacy10{}, true
	case "awg_1_5":
		return AWG15{}, true
	case "awg_2_0":
		return AWG20{}, true
	case "awg_3_0":
		return AWG30{}, true
	default:
		return nil, false
	}
}

func All() []ProtocolProfile {
	return []ProtocolProfile{Legacy10{}, AWG15{}, AWG20{}, AWG30{}}
}
