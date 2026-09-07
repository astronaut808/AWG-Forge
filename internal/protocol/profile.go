package protocol

import (
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
	ParameterKeys() []string
	GenerateDefaults() (config.ProtocolParams, error)
	Validate(config.ProtocolParams) error
	RenderServerInterface(RenderContext) ([]ConfigLine, error)
	RenderServerPeer(RenderContext, config.Client) ([]ConfigLine, error)
	RenderClientInterface(RenderContext, config.Client) ([]ConfigLine, error)
	RenderClientPeer(RenderContext, config.Client) ([]ConfigLine, error)
}

var profileRegistry = [...]ProtocolProfile{
	Legacy10{},
	AWG15{},
	AWG20{},
	AWG3{},
}

type SecretGeneratingProfile interface {
	GenerateSecrets() (config.ProtocolSecrets, error)
}

type SecretValidatingProfile interface {
	ValidateSecrets(config.ProtocolSecrets) error
}

func GenerateSecrets(profile ProtocolProfile) (config.ProtocolSecrets, error) {
	if profile, ok := profile.(SecretGeneratingProfile); ok {
		return profile.GenerateSecrets()
	}
	return config.ProtocolSecrets{}, nil
}

func ValidateSecrets(profile ProtocolProfile, secrets config.ProtocolSecrets) error {
	if profile, ok := profile.(SecretValidatingProfile); ok {
		return profile.ValidateSecrets(secrets)
	}
	return nil
}

func ByID(id string) (ProtocolProfile, bool) {
	for _, profile := range profileRegistry {
		if profile.ID() == id {
			return profile, true
		}
	}
	return nil, false
}

func All() []ProtocolProfile {
	return append([]ProtocolProfile(nil), profileRegistry[:]...)
}

func cloneParameterKeys(keys []string) []string {
	return append([]string(nil), keys...)
}
