package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/protocol"
)

const bootstrapFileName = "bootstrap.env"

type BootstrapTunnel struct {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initLocked()
}

func (s *Service) initLocked() (config.State, error) {
	if state, err := s.store.Load(); err == nil {
		return s.repairLoadedState(state)
	} else if !errors.Is(err, os.ErrNotExist) {
		return config.State{}, err
	}

	bootstrap, removeBootstrap, err := s.bootstrapTunnel()
	if err != nil {
		return config.State{}, err
	}
	state, err := s.createInitialState(bootstrap)
	if err != nil {
		return config.State{}, err
	}
	if removeBootstrap {
		_ = os.Remove(filepath.Join(s.cfg.ConfigDir, bootstrapFileName))
	}
	return state, nil
}

func (s *Service) createInitialState(bootstrap BootstrapTunnel) (config.State, error) {
	if err := validateBootstrap(bootstrap); err != nil {
		return config.State{}, err
	}
	now := time.Now().UTC()
	secret, err := s.sessionSecretValue()
	if err != nil {
		return config.State{}, err
	}
	tunnel, err := s.newTunnel(defaultTunnelSpec(bootstrap.ProfileID, bootstrap.Name, bootstrap.ListenPort, bootstrap.IPv4Subnet))
	if err != nil {
		return config.State{}, err
	}
	tunnel.ServerHost = strings.TrimSpace(bootstrap.ServerHost)
	tunnel.DNS = strings.TrimSpace(bootstrap.DNS)
	tunnel.AllowedIPs = strings.TrimSpace(bootstrap.AllowedIPs)
	tunnel.Keepalive = bootstrap.PersistentKeepalive
	tunnel.MTU = bootstrap.MTU
	state := config.State{
		SchemaVersion:     config.CurrentStateSchemaVersion,
		SessionSecret:     secret,
		ServerHost:        strings.TrimSpace(bootstrap.ServerHost),
		ExternalInterface: strings.TrimSpace(bootstrap.ExternalInterface),
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

func (s *Service) bootstrapTunnel() (BootstrapTunnel, bool, error) {
	path := filepath.Join(s.cfg.ConfigDir, bootstrapFileName)
	if b, err := os.ReadFile(path); err == nil {
		bootstrap, err := parseBootstrapEnv(string(b), s.cfg)
		return bootstrap, true, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return BootstrapTunnel{}, false, err
	}
	return bootstrapFromConfig(s.cfg), false, nil
}

func bootstrapFromConfig(cfg config.Config) BootstrapTunnel {
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
	return BootstrapTunnel{
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

func parseBootstrapEnv(text string, cfg config.Config) (BootstrapTunnel, error) {
	values := make(map[string]string)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return BootstrapTunnel{}, fmt.Errorf("invalid bootstrap line %q", line)
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	bootstrap := bootstrapFromConfig(cfg)
	setString := func(key string, dst *string) {
		if value, ok := values[key]; ok {
			*dst = value
		}
	}
	setInt := func(key string, dst *int) error {
		value, ok := values[key]
		if !ok || value == "" {
			return nil
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s must be an integer", key)
		}
		*dst = n
		return nil
	}
	setString("SERVER_HOST", &bootstrap.ServerHost)
	setString("EXTERNAL_INTERFACE", &bootstrap.ExternalInterface)
	setString("PROTOCOL_PROFILE", &bootstrap.ProfileID)
	setString("TUNNEL_NAME", &bootstrap.Name)
	setString("IPV4_SUBNET", &bootstrap.IPv4Subnet)
	setString("DNS", &bootstrap.DNS)
	setString("ALLOWED_IPS", &bootstrap.AllowedIPs)
	if err := setInt("LISTEN_PORT", &bootstrap.ListenPort); err != nil {
		return BootstrapTunnel{}, err
	}
	if err := setInt("PERSISTENT_KEEPALIVE", &bootstrap.PersistentKeepalive); err != nil {
		return BootstrapTunnel{}, err
	}
	if err := setInt("MTU", &bootstrap.MTU); err != nil {
		return BootstrapTunnel{}, err
	}
	return bootstrap, validateBootstrap(bootstrap)
}

func validateBootstrap(bootstrap BootstrapTunnel) error {
	if err := validateServerHost(strings.TrimSpace(bootstrap.ServerHost)); err != nil {
		return err
	}
	if strings.TrimSpace(bootstrap.ExternalInterface) == "" {
		return errors.New("external interface is required")
	}
	if strings.TrimSpace(bootstrap.DNS) == "" {
		return errors.New("DNS is required")
	}
	if strings.TrimSpace(bootstrap.AllowedIPs) == "" {
		return errors.New("allowed IPs are required")
	}
	if bootstrap.ListenPort != 0 && (bootstrap.ListenPort < 1 || bootstrap.ListenPort > 65535) {
		return errors.New("listen port must be 1..65535")
	}
	if bootstrap.PersistentKeepalive < 0 {
		return errors.New("persistent keepalive must be non-negative")
	}
	if bootstrap.MTU != 0 && (bootstrap.MTU < 576 || bootstrap.MTU > 1500) {
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
