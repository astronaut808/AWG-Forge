package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/protocol"
)

func (s *Service) Init() (config.State, error) {
	if state, err := s.store.Load(); err == nil {
		return s.repairLoadedState(state)
	}

	now := time.Now().UTC()
	secret, err := s.sessionSecretValue()
	if err != nil {
		return config.State{}, err
	}
	tunnel, err := s.newTunnel(defaultTunnelSpec(s.cfg.ProtocolProfile, s.cfg.TunnelName, s.cfg.ListenPort, s.cfg.IPv4Subnet))
	if err != nil {
		return config.State{}, err
	}
	state := config.State{
		SchemaVersion:     config.CurrentStateSchemaVersion,
		SessionSecret:     secret,
		ServerHost:        s.cfg.ServerHost,
		ExternalInterface: s.cfg.ExternalInterface,
		Tunnels:           []config.Tunnel{tunnel},
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := s.store.Save(state); err != nil {
		return config.State{}, err
	}
	return state, s.RenderAll()
}

func (s *Service) repairLoadedState(state config.State) (config.State, error) {
	originalState := state
	changed := false
	protocolRepaired := false
	if state.SchemaVersion == 0 {
		state.SchemaVersion = config.CurrentStateSchemaVersion
		changed = true
	}
	if state.SessionSecret == "" {
		secret, err := s.sessionSecretValue()
		if err != nil {
			return config.State{}, err
		}
		state.SessionSecret = secret
		changed = true
	}
	if state.ServerHost != s.cfg.ServerHost {
		state.ServerHost = s.cfg.ServerHost
		changed = true
		for ti := range state.Tunnels {
			if strings.TrimSpace(state.Tunnels[ti].ServerHost) == "" {
				state.Tunnels[ti].ConfigRevision++
				state.Tunnels[ti].UpdatedAt = time.Now().UTC()
			}
		}
	}
	if state.ExternalInterface != s.cfg.ExternalInterface {
		state.ExternalInterface = s.cfg.ExternalInterface
		changed = true
	}
	if len(state.Tunnels) == 0 {
		tunnel, err := s.newTunnel(defaultTunnelSpec(s.cfg.ProtocolProfile, s.cfg.TunnelName, s.cfg.ListenPort, s.cfg.IPv4Subnet))
		if err != nil {
			return config.State{}, err
		}
		state.Tunnels = []config.Tunnel{tunnel}
		changed = true
	}
	for ti := range state.Tunnels {
		networkRepaired, err := repairTunnelNetwork(&state.Tunnels[ti])
		if err != nil {
			return config.State{}, err
		}
		if networkRepaired {
			changed = true
		}
		if state.Tunnels[ti].ConfigRevision == 0 {
			state.Tunnels[ti].ConfigRevision = 1
			changed = true
		}
		for ci := range state.Tunnels[ti].Clients {
			if state.Tunnels[ti].Clients[ci].ConfigRevision == 0 {
				state.Tunnels[ti].Clients[ci].ConfigRevision = state.Tunnels[ti].ConfigRevision
				changed = true
			}
		}
		repaired, err := s.repairProtocolParams(&state.Tunnels[ti])
		if err != nil {
			return config.State{}, err
		}
		if repaired {
			changed = true
			protocolRepaired = true
		}
	}
	if changed {
		state.UpdatedAt = time.Now().UTC()
		if protocolRepaired {
			if _, err := s.store.BackupState(originalState, "repair-protocol-params"); err != nil {
				return config.State{}, err
			}
		}
		if err := s.store.Save(state); err != nil {
			return config.State{}, err
		}
	}
	return state, nil
}

func (s *Service) repairProtocolParams(tunnel *config.Tunnel) (bool, error) {
	p, ok := protocol.ByID(tunnel.ProtocolProfileID)
	if !ok {
		return false, fmt.Errorf("unsupported protocol profile %q", tunnel.ProtocolProfileID)
	}
	if err := p.Validate(tunnel.ProtocolParams); err == nil {
		return false, nil
	}
	params, err := p.GenerateDefaults()
	if err != nil {
		return false, err
	}
	if err := p.Validate(params); err != nil {
		return false, err
	}
	tunnel.ProtocolParams = params
	tunnel.ConfigRevision++
	tunnel.UpdatedAt = time.Now().UTC()
	return true, nil
}

func repairTunnelNetwork(tunnel *config.Tunnel) (bool, error) {
	normalized, _, err := normalizeIPv4CIDR(tunnel.IPv4Subnet)
	if err != nil {
		return false, err
	}
	serverIP, err := serverAddress(normalized)
	if err != nil {
		return false, err
	}
	changed := false
	if tunnel.IPv4Subnet != normalized {
		tunnel.IPv4Subnet = normalized
		changed = true
	}
	if tunnel.ServerAddress != serverIP {
		tunnel.ServerAddress = serverIP
		changed = true
	}
	if changed {
		tunnel.ConfigRevision++
		tunnel.UpdatedAt = time.Now().UTC()
	}
	return changed, nil
}
