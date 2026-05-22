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
	GenerateDefaults() (config.ProtocolParams, error)
	Validate(config.ProtocolParams) error
	RenderServerInterface(RenderContext) ([]ConfigLine, error)
	RenderServerPeer(RenderContext, config.Client) ([]ConfigLine, error)
	RenderClientInterface(RenderContext, config.Client) ([]ConfigLine, error)
	RenderClientPeer(RenderContext, config.Client) ([]ConfigLine, error)
}

func ByID(id string) (ProtocolProfile, bool) {
	switch id {
	case "awg_legacy_1_0":
		return Legacy10{}, true
	case "awg_1_5":
		return AWG15{}, true
	case "awg_2_0":
		return AWG20{}, true
	default:
		return nil, false
	}
}

func All() []ProtocolProfile {
	return []ProtocolProfile{Legacy10{}, AWG15{}, AWG20{}}
}
