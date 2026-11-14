//go:build !linux

package app

import (
	"context"

	"github.com/astronaut808/awg-forge/internal/config"
)

func checkRelayHandshake(context.Context, config.Tunnel, config.Relay) string {
	return "unavailable"
}
