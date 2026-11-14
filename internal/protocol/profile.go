package protocol

import "github.com/astronaut808/awg-forge/internal/config"

type ConfigLine struct {
	Key   string
	Value string
}

type RenderContext struct {
	State  config.State
	Tunnel config.Tunnel
}

func (ctx RenderContext) ClientEndpoint(client config.Client) (config.Endpoint, error) {
	return ctx.Tunnel.ClientEndpoint(ctx.State.ServerHost, client)
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
	switch id {
	case "awg_legacy_1_0":
		return Legacy10{}, true
	case "awg_1_5":
		return AWG15{}, true
	case "awg_2_0":
		return AWG20{}, true
	case "awg_3":
		return AWG3{}, true
	default:
		return nil, false
	}
}

func All() []ProtocolProfile {
	return []ProtocolProfile{Legacy10{}, AWG15{}, AWG20{}, AWG3{}}
}
