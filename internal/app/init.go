package app

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/protocol"
)

type InitOptions struct {
	ServerHost          string
	ExternalInterface   string
	ProfileID           string
	Name                string
	ListenPort          int
	IPv4Subnet          string
	DNS                 string
	AllowedIPs          string
	PersistentKeepalive int
	MTU                 int
}

func (s *Service) Init() (config.State, error) {
	return s.InitWithOptions(InitOptionsFromConfig(s.cfg))
}

func (s *Service) InitWithOptions(options InitOptions) (config.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initWithOptionsLocked(options)
}

func (s *Service) initLocked() (config.State, error) {
	return s.initWithOptionsLocked(InitOptionsFromConfig(s.cfg))
}

func (s *Service) initWithOptionsLocked(options InitOptions) (config.State, error) {
	if state, err := s.store.Load(); err == nil {
		return s.repairLoadedState(state)
	} else if !errors.Is(err, os.ErrNotExist) {
		return config.State{}, err
	}

	return s.createInitialState(options)
}

func (s *Service) createInitialState(options InitOptions) (config.State, error) {
	options, spec, err := resolveInitOptions(options)
	if err != nil {
		return config.State{}, err
	}
	now := time.Now().UTC()
	secret, err := s.sessionSecretValue()
	if err != nil {
		return config.State{}, err
	}
	tunnel, err := s.newTunnel(spec)
	if err != nil {
		return config.State{}, err
	}
	tunnel.ServerHost = options.ServerHost
	tunnel.DNS = options.DNS
	tunnel.AllowedIPs = options.AllowedIPs
	tunnel.Keepalive = options.PersistentKeepalive
	tunnel.MTU = options.MTU
	state := config.State{
		SchemaVersion:     config.CurrentStateSchemaVersion,
		SessionSecret:     secret,
		ServerHost:        options.ServerHost,
		ExternalInterface: options.ExternalInterface,
		Warp:              config.Warp{InterfaceName: "warp0", MTU: 1280, PersistentKeepalive: 25},
		Tunnels:           []config.Tunnel{tunnel},
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := s.store.Save(state); err != nil {
		return config.State{}, err
	}
	return state, s.renderTunnelFromState(state, tunnel.ID, false)
}

func InitOptionsFromConfig(cfg config.Config) InitOptions {
	profileID := strings.TrimSpace(cfg.ProtocolProfile)
	if profileID == "" {
		profileID = "awg_2_0"
	}
	dns := strings.TrimSpace(cfg.DNS)
	if dns == "" {
		dns = "1.1.1.1"
	}
	allowedIPs := strings.TrimSpace(cfg.AllowedIPs)
	if allowedIPs == "" {
		allowedIPs = "0.0.0.0/0"
	}
	externalInterface := strings.TrimSpace(cfg.ExternalInterface)
	if externalInterface == "" {
		externalInterface = "eth0"
	}
	return InitOptions{
		ServerHost:          cfg.ServerHost,
		ExternalInterface:   externalInterface,
		ProfileID:           profileID,
		Name:                cfg.TunnelName,
		ListenPort:          cfg.ListenPort,
		IPv4Subnet:          cfg.IPv4Subnet,
		DNS:                 dns,
		AllowedIPs:          allowedIPs,
		PersistentKeepalive: cfg.PersistentKeepalive,
		MTU:                 cfg.MTU,
	}
}

func resolveInitOptions(options InitOptions) (InitOptions, tunnelSpec, error) {
	options.ServerHost = strings.TrimSpace(options.ServerHost)
	options.ExternalInterface = strings.TrimSpace(options.ExternalInterface)
	options.ProfileID = strings.TrimSpace(options.ProfileID)
	options.Name = strings.TrimSpace(options.Name)
	options.IPv4Subnet = strings.TrimSpace(options.IPv4Subnet)
	options.DNS = strings.TrimSpace(options.DNS)
	options.AllowedIPs = strings.TrimSpace(options.AllowedIPs)
	if options.ProfileID == "" {
		options.ProfileID = "awg_2_0"
	}
	if options.ExternalInterface == "" {
		options.ExternalInterface = "eth0"
	}
	if options.DNS == "" {
		options.DNS = "1.1.1.1"
	}
	if options.AllowedIPs == "" {
		options.AllowedIPs = "0.0.0.0/0"
	}
	spec := defaultTunnelSpec(options.ProfileID, options.Name, options.ListenPort, options.IPv4Subnet)
	if err := validateInitialTunnelOptions(options, spec); err != nil {
		return InitOptions{}, tunnelSpec{}, err
	}
	normalizedSubnet, _, err := normalizeIPv4CIDR(spec.IPv4Subnet)
	if err != nil {
		return InitOptions{}, tunnelSpec{}, err
	}
	spec.IPv4Subnet = normalizedSubnet
	return options, spec, nil
}

func validateInitialTunnelOptions(options InitOptions, spec tunnelSpec) error {
	if err := validateServerHost(options.ServerHost); err != nil {
		return err
	}
	if options.ExternalInterface == "" {
		return errors.New("external interface is required")
	}
	if options.DNS == "" {
		return errors.New("DNS is required")
	}
	if options.AllowedIPs == "" {
		return errors.New("allowed IPs are required")
	}
	if !tunnelNameRE.MatchString(spec.Name) {
		return errors.New("tunnel name must start with a letter and contain only letters, numbers, dots, underscores, or dashes")
	}
	if spec.ListenPort < 1 || spec.ListenPort > 65535 {
		return errors.New("listen port must be 1..65535")
	}
	if options.PersistentKeepalive < 0 {
		return errors.New("persistent keepalive must be non-negative")
	}
	if options.MTU != 0 && (options.MTU < 576 || options.MTU > 1500) {
		return errors.New("MTU must be auto or between 576 and 1500")
	}
	return nil
}

func (s *Service) repairLoadedState(state config.State) (config.State, error) {
	originalState := state
	changed := false
	protocolRepaired := false
	if state.SchemaVersion < config.CurrentStateSchemaVersion {
		state.SchemaVersion = config.CurrentStateSchemaVersion
		changed = true
	}
	if state.Warp.InterfaceName == "" {
		state.Warp.InterfaceName = "warp0"
		state.Warp.MTU = 1280
		state.Warp.PersistentKeepalive = 25
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
	if state.ServerHost == "" {
		state.ServerHost = s.cfg.ServerHost
		changed = true
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
		if state.Tunnels[ti].EgressMode == "" {
			state.Tunnels[ti].EgressMode = config.EgressWAN
			changed = true
		}
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
