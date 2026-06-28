package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/keys"
	"github.com/astronaut808/awg-forge/internal/protocol"
)

type tunnelSpec struct {
	ProfileID     string
	Name          string
	InterfaceName string
	ListenPort    int
	IPv4Subnet    string
}

type TunnelSuggestion struct {
	Name       string
	ListenPort int
	IPv4Subnet string
}

type TunnelCreateOptions struct {
	ProfileID  string
	Name       string
	Subnet     string
	Port       int
	EgressMode string
}

func defaultTunnelSpec(profileID, name string, port int, subnet string) tunnelSpec {
	baseName, basePort, baseSubnet := SuggestedTunnelSpec(profileID)
	if name == "" {
		name = baseName
	}
	if port == 0 {
		port = basePort
	}
	if subnet == "" {
		subnet = baseSubnet
	}
	return tunnelSpec{
		ProfileID:     profileID,
		Name:          name,
		InterfaceName: name,
		ListenPort:    port,
		IPv4Subnet:    subnet,
	}
}

func SuggestedNextTunnelSpec(profileID string, state config.State) TunnelSuggestion {
	baseName, basePort, baseSubnet := SuggestedTunnelSpec(profileID)
	return TunnelSuggestion{
		Name:       nextFreeTunnelName(baseName, state.Tunnels),
		ListenPort: nextFreeTunnelPort(basePort, state.Tunnels),
		IPv4Subnet: nextFreeTunnelSubnet(baseSubnet, state.Tunnels),
	}
}

func nextFreeTunnelName(base string, tunnels []config.Tunnel) string {
	if base == "" {
		base = config.DefaultTunnel
	}
	used := make(map[string]bool, len(tunnels)*2)
	for _, tunnel := range tunnels {
		used[tunnel.Name] = true
		used[tunnel.InterfaceName] = true
	}
	if !used[base] {
		return base
	}
	for n := 2; n < 1000; n++ {
		candidate := fmt.Sprintf("%s-%d", base, n)
		if !used[candidate] {
			return candidate
		}
	}
	return fmt.Sprintf("%s-%d", base, time.Now().UTC().Unix())
}

func nextFreeTunnelPort(base int, tunnels []config.Tunnel) int {
	used := make(map[int]bool, len(tunnels))
	for _, tunnel := range tunnels {
		used[tunnel.ListenPort] = true
	}
	if base < 1 || base > 65535 {
		base = 51820
	}
	for port := base; port <= 65535; port++ {
		if !used[port] {
			return port
		}
	}
	for port := 1; port < base; port++ {
		if !used[port] {
			return port
		}
	}
	return base
}

func nextFreeTunnelSubnet(base string, tunnels []config.Tunnel) string {
	for n := uint32(0); n < 4096; n++ {
		candidate, ok := offsetSubnet24(base, n)
		if !ok {
			break
		}
		if !tunnelSubnetOverlaps(candidate, tunnels) {
			return candidate
		}
	}
	if !tunnelSubnetOverlaps(base, tunnels) {
		return base
	}
	return base
}

func offsetSubnet24(base string, offset uint32) (string, bool) {
	_, ipnet, err := normalizeIPv4CIDR(base)
	if err != nil {
		return "", false
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 32 || ones != 24 {
		return "", false
	}
	network := ipv4ToUint(ipnet.IP)
	candidate := network + (offset << 8)
	if candidate < network {
		return "", false
	}
	return fmt.Sprintf("%s/24", uintToIPv4(candidate).String()), true
}

func tunnelSubnetOverlaps(subnet string, tunnels []config.Tunnel) bool {
	for _, tunnel := range tunnels {
		if subnetsOverlap(tunnel.IPv4Subnet, subnet) {
			return true
		}
	}
	return false
}

func SuggestedTunnelSpec(profileID string) (name string, port int, subnet string) {
	switch profileID {
	case "awg_1_5":
		return "awg15", 51825, "10.15.0.0/24"
	case "awg_2_0":
		return "awg20", 51830, "10.20.0.0/24"
	default:
		return "awg0", 51820, "10.8.0.0/24"
	}
}

func (s *Service) CreateTunnel(profileID, name, subnet string, port int) (config.Tunnel, error) {
	return s.CreateTunnelWithOptions(context.Background(), TunnelCreateOptions{
		ProfileID: profileID,
		Name:      name,
		Subnet:    subnet,
		Port:      port,
	})
}

func (s *Service) CreateTunnelWithOptions(ctx context.Context, options TunnelCreateOptions) (config.Tunnel, error) {
	egressMode := valueOr(strings.TrimSpace(options.EgressMode), config.EgressWAN)
	if err := validateEgressMode(egressMode); err != nil {
		return config.Tunnel{}, err
	}
	if egressMode == config.EgressWarp {
		needsRegistration, err := s.warpRegistrationNeeded()
		if err != nil {
			return config.Tunnel{}, err
		}
		if needsRegistration {
			if ctx == nil {
				ctx = context.Background()
			}
			if _, err := s.RegisterWarp(ctx); err != nil {
				return config.Tunnel{}, fmt.Errorf("automatic WARP registration failed: %w", err)
			}
		}
	}
	return s.createTunnel(options.ProfileID, options.Name, options.Subnet, options.Port, egressMode)
}

func (s *Service) warpRegistrationNeeded() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.initLocked()
	if err != nil {
		return false, err
	}
	return !state.Warp.Configured(), nil
}

func (s *Service) createTunnel(profileID, name, subnet string, port int, egressMode string) (config.Tunnel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if profileID == "" {
		profileID = s.cfg.ProtocolProfile
	}
	state, err := s.initLocked()
	if err != nil {
		return config.Tunnel{}, err
	}
	suggestion := SuggestedNextTunnelSpec(profileID, state)
	if name == "" {
		name = suggestion.Name
	}
	if port == 0 {
		port = suggestion.ListenPort
	}
	if subnet == "" {
		subnet = suggestion.IPv4Subnet
	}
	normalizedSubnet, _, err := normalizeIPv4CIDR(subnet)
	if err != nil {
		return config.Tunnel{}, err
	}
	subnet = normalizedSubnet
	if !tunnelNameRE.MatchString(name) {
		return config.Tunnel{}, errors.New("tunnel name must start with a letter and contain only letters, numbers, dots, underscores, or dashes")
	}
	if port < 1 || port > 65535 {
		return config.Tunnel{}, errors.New("listen port must be between 1 and 65535")
	}
	previousState, err := cloneState(state)
	if err != nil {
		return config.Tunnel{}, err
	}
	for _, tunnel := range state.Tunnels {
		if tunnel.InterfaceName == name || tunnel.Name == name {
			return config.Tunnel{}, fmt.Errorf("tunnel %q already exists", name)
		}
		if tunnel.ListenPort == port {
			return config.Tunnel{}, fmt.Errorf("listen port %d is already used by %s", port, tunnel.Name)
		}
		if subnetsOverlap(tunnel.IPv4Subnet, subnet) {
			return config.Tunnel{}, fmt.Errorf("subnet %s overlaps with tunnel %s", subnet, tunnel.Name)
		}
	}
	tunnel, err := s.newTunnel(tunnelSpec{
		ProfileID:     profileID,
		Name:          name,
		InterfaceName: name,
		ListenPort:    port,
		IPv4Subnet:    subnet,
	})
	if err != nil {
		return config.Tunnel{}, err
	}
	tunnel.EgressMode = egressMode
	state.Tunnels = append(state.Tunnels, tunnel)
	state.UpdatedAt = time.Now().UTC()
	if err := s.store.Save(state); err != nil {
		return config.Tunnel{}, err
	}
	if err := s.renderTunnelLocked(tunnel.ID, true); err != nil {
		if rollbackErr := s.rollbackRenderedState(previousState, "", tunnel.InterfaceName); rollbackErr != nil {
			return config.Tunnel{}, errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr))
		}
		s.log("error", "tunnel.create.failed", "tunnel creation failed", tunnelAuditFields(tunnel), err)
		return config.Tunnel{}, err
	}
	s.log("info", "tunnel.created", "tunnel created", tunnelAuditFields(tunnel), nil)
	return tunnel, nil
}

func (s *Service) UpdateTunnelSettings(tunnelID string, update TunnelSettingsUpdate) (config.Tunnel, error) {
	return s.UpdateTunnelSettingsContext(context.Background(), tunnelID, update)
}

func (s *Service) UpdateTunnelSettingsContext(ctx context.Context, tunnelID string, update TunnelSettingsUpdate) (config.Tunnel, error) {
	if err := s.ensureWarpForTunnelSettings(ctx, tunnelID, update); err != nil {
		return config.Tunnel{}, err
	}
	return s.updateTunnelSettings(tunnelID, update)
}

func (s *Service) ensureWarpForTunnelSettings(ctx context.Context, tunnelID string, update TunnelSettingsUpdate) error {
	s.mu.Lock()
	state, err := s.initLocked()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	idx, ok := tunnelIndexByID(state, tunnelID)
	if !ok {
		s.mu.Unlock()
		return errors.New("tunnel not found")
	}
	settings, err := resolveTunnelSettings(state, idx, update)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	needsRegistration := settings.EgressMode == config.EgressWarp && !state.Warp.Configured()
	s.mu.Unlock()
	if !needsRegistration {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := s.RegisterWarp(ctx); err != nil {
		return fmt.Errorf("automatic WARP registration failed: %w", err)
	}
	return nil
}

func (s *Service) updateTunnelSettings(tunnelID string, update TunnelSettingsUpdate) (config.Tunnel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.initLocked()
	if err != nil {
		return config.Tunnel{}, err
	}
	idx, ok := tunnelIndexByID(state, tunnelID)
	if !ok {
		return config.Tunnel{}, errors.New("tunnel not found")
	}
	previousState, err := cloneState(state)
	if err != nil {
		return config.Tunnel{}, err
	}
	old := state.Tunnels[idx]
	settings, err := resolveTunnelSettings(state, idx, update)
	if err != nil {
		return config.Tunnel{}, err
	}
	state.Tunnels[idx] = applyTunnelSettings(old, settings)
	state.UpdatedAt = state.Tunnels[idx].UpdatedAt
	if err := s.store.Save(state); err != nil {
		return config.Tunnel{}, err
	}
	if s.cfg.ApplyConfig && firewallRelevantChanged(old, state.Tunnels[idx]) {
		if old.InterfaceName != settings.Name {
			_ = exec.Command("awg-quick", "down", old.InterfaceName).Run()
		}
		_ = s.cleanupFirewallRules(old)
	}
	if old.InterfaceName != settings.Name {
		_ = s.store.DeleteRenderedTunnel(old.InterfaceName)
	}
	if err := s.renderTunnelLocked(state.Tunnels[idx].ID, true); err != nil {
		deleteRendered := []string{}
		if old.InterfaceName != settings.Name {
			deleteRendered = append(deleteRendered, settings.Name)
		}
		if rollbackErr := s.rollbackRuntimeState(previousState, old.ID, deleteRendered...); rollbackErr != nil {
			return config.Tunnel{}, errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr))
		}
		s.log("error", "tunnel.settings.failed", "tunnel settings update failed", tunnelAuditFields(state.Tunnels[idx]), err)
		return config.Tunnel{}, err
	}
	s.log("info", "tunnel.settings.updated", "tunnel settings updated", tunnelAuditFields(state.Tunnels[idx]), nil)
	return state.Tunnels[idx], nil
}

type resolvedTunnelSettings struct {
	Name       string
	ServerHost string
	EgressMode string
	Subnet     string
	ServerIP   string
	DNS        string
	AllowedIPs string
	Keepalive  int
	MTU        int
	Port       int
	Enabled    bool
}

func resolveTunnelSettings(state config.State, idx int, update TunnelSettingsUpdate) (resolvedTunnelSettings, error) {
	current := state.Tunnels[idx]
	settings := resolvedTunnelSettings{
		Name:       valueOr(strings.TrimSpace(update.Name), current.Name),
		ServerHost: strings.TrimSpace(update.ServerHost),
		EgressMode: valueOr(strings.TrimSpace(update.EgressMode), current.EgressMode),
		Subnet:     valueOr(strings.TrimSpace(update.Subnet), current.IPv4Subnet),
		DNS:        valueOr(strings.TrimSpace(update.DNS), current.DNS),
		AllowedIPs: valueOr(strings.TrimSpace(update.AllowedIPs), current.AllowedIPs),
		Keepalive:  update.Keepalive,
		MTU:        update.MTU,
		Port:       update.Port,
		Enabled:    update.Enabled,
	}
	if err := validateResolvedTunnelSettings(settings); err != nil {
		return resolvedTunnelSettings{}, err
	}
	normalizedSubnet, _, err := normalizeIPv4CIDR(settings.Subnet)
	if err != nil {
		return resolvedTunnelSettings{}, err
	}
	settings.Subnet = normalizedSubnet
	settings.ServerIP, err = serverAddress(settings.Subnet)
	if err != nil {
		return resolvedTunnelSettings{}, err
	}
	if settings.Subnet != current.IPv4Subnet && len(current.Clients) > 0 {
		return resolvedTunnelSettings{}, errors.New("cannot change subnet while tunnel has clients")
	}
	if err := validateTunnelUniqueness(state, idx, settings); err != nil {
		return resolvedTunnelSettings{}, err
	}
	return settings, nil
}

func validateEgressMode(mode string) error {
	switch mode {
	case "", config.EgressWAN, config.EgressWarp:
		return nil
	default:
		return errors.New("egress mode must be wan or warp")
	}
}

func validateResolvedTunnelSettings(settings resolvedTunnelSettings) error {
	if !tunnelNameRE.MatchString(settings.Name) {
		return errors.New("tunnel name must start with a letter and contain only letters, numbers, dots, underscores, or dashes")
	}
	if err := validateServerHost(settings.ServerHost); err != nil {
		return err
	}
	if settings.Port < 1 || settings.Port > 65535 {
		return errors.New("listen port must be between 1 and 65535")
	}
	if settings.Keepalive < 0 || settings.Keepalive > 65535 {
		return errors.New("persistent keepalive must be between 0 and 65535")
	}
	if settings.MTU != 0 && (settings.MTU < 576 || settings.MTU > 1500) {
		return errors.New("MTU must be auto or between 576 and 1500")
	}
	return validateEgressMode(settings.EgressMode)
}

func validateTunnelUniqueness(state config.State, idx int, settings resolvedTunnelSettings) error {
	for i, tunnel := range state.Tunnels {
		if i == idx {
			continue
		}
		if tunnel.InterfaceName == settings.Name || tunnel.Name == settings.Name {
			return fmt.Errorf("tunnel %q already exists", settings.Name)
		}
		if tunnel.ListenPort == settings.Port {
			return fmt.Errorf("listen port %d is already used by %s", settings.Port, tunnel.Name)
		}
		if subnetsOverlap(tunnel.IPv4Subnet, settings.Subnet) {
			return fmt.Errorf("subnet %s overlaps with tunnel %s", settings.Subnet, tunnel.Name)
		}
	}
	return nil
}

func applyTunnelSettings(tunnel config.Tunnel, settings resolvedTunnelSettings) config.Tunnel {
	old := tunnel
	tunnel.Name = settings.Name
	tunnel.InterfaceName = settings.Name
	tunnel.ServerHost = settings.ServerHost
	tunnel.EgressMode = settings.EgressMode
	tunnel.ListenPort = settings.Port
	tunnel.IPv4Subnet = settings.Subnet
	tunnel.ServerAddress = settings.ServerIP
	tunnel.DNS = settings.DNS
	tunnel.AllowedIPs = settings.AllowedIPs
	tunnel.Keepalive = settings.Keepalive
	tunnel.MTU = settings.MTU
	tunnel.Enabled = settings.Enabled
	if tunnelConfigChanged(old, tunnel) {
		tunnel.ConfigRevision++
	}
	tunnel.UpdatedAt = time.Now().UTC()
	return tunnel
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func (s *Service) DeleteTunnel(tunnelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.initLocked()
	if err != nil {
		return err
	}
	if len(state.Tunnels) <= 1 {
		return errors.New("cannot delete the last tunnel")
	}
	idx, ok := tunnelIndexByID(state, tunnelID)
	if !ok {
		return errors.New("tunnel not found")
	}
	previousState, err := cloneState(state)
	if err != nil {
		return err
	}
	tunnel := state.Tunnels[idx]
	if _, err := s.store.BackupState(state, "delete-"+tunnel.InterfaceName); err != nil {
		return err
	}
	state.Tunnels = append(state.Tunnels[:idx], state.Tunnels[idx+1:]...)
	state.UpdatedAt = time.Now().UTC()
	if err := s.store.Save(state); err != nil {
		return err
	}
	if s.cfg.ApplyConfig {
		if err := exec.Command("awg-quick", "down", tunnel.InterfaceName).Run(); err != nil {
			if rollbackErr := s.rollbackRuntimeState(previousState, tunnel.ID); rollbackErr != nil {
				return errors.Join(&ApplyError{Err: err}, fmt.Errorf("rollback failed: %w", rollbackErr))
			}
			s.log("error", "tunnel.delete.failed", "tunnel delete runtime down failed", tunnelAuditFields(tunnel), err)
			return &ApplyError{Err: err}
		}
		if err := s.cleanupFirewallRules(tunnel); err != nil {
			if rollbackErr := s.rollbackRuntimeState(previousState, tunnel.ID); rollbackErr != nil {
				return errors.Join(&ApplyError{Err: err}, fmt.Errorf("rollback failed: %w", rollbackErr))
			}
			s.log("error", "tunnel.delete.failed", "tunnel delete firewall cleanup failed", tunnelAuditFields(tunnel), err)
			return &ApplyError{Err: err}
		}
		if err := s.reconcileWarpRuntime(state); err != nil {
			if rollbackErr := s.rollbackRuntimeState(previousState, tunnel.ID); rollbackErr != nil {
				return errors.Join(&ApplyError{Err: err}, fmt.Errorf("rollback failed: %w", rollbackErr))
			}
			s.log("error", "tunnel.delete.failed", "WARP runtime reconciliation failed after tunnel delete", tunnelAuditFields(tunnel), err)
			return &ApplyError{Err: err}
		}
	}
	if err := s.store.DeleteRenderedTunnel(tunnel.InterfaceName); err != nil {
		if rollbackErr := s.rollbackRuntimeState(previousState, tunnel.ID); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr))
		}
		s.log("error", "tunnel.delete.failed", "tunnel rendered files deletion failed", tunnelAuditFields(tunnel), err)
		return err
	}
	s.log("info", "tunnel.deleted", "tunnel deleted", tunnelAuditFields(tunnel), nil)
	return nil
}

func (s *Service) newTunnel(spec tunnelSpec) (config.Tunnel, error) {
	p, ok := protocol.ByID(spec.ProfileID)
	if !ok {
		return config.Tunnel{}, fmt.Errorf("unsupported protocol profile %q", spec.ProfileID)
	}
	priv, pub, err := keys.PrivateKey()
	if err != nil {
		return config.Tunnel{}, err
	}
	params, err := p.GenerateDefaults()
	if err != nil {
		return config.Tunnel{}, err
	}
	if err := p.Validate(params); err != nil {
		return config.Tunnel{}, err
	}
	normalizedSubnet, _, err := normalizeIPv4CIDR(spec.IPv4Subnet)
	if err != nil {
		return config.Tunnel{}, err
	}
	serverIP, err := serverAddress(normalizedSubnet)
	if err != nil {
		return config.Tunnel{}, err
	}
	now := time.Now().UTC()
	return config.Tunnel{
		ID:                randomID(),
		Name:              spec.Name,
		InterfaceName:     spec.InterfaceName,
		EgressMode:        config.EgressWAN,
		Enabled:           true,
		ListenPort:        spec.ListenPort,
		ServerAddress:     serverIP,
		IPv4Subnet:        normalizedSubnet,
		DNS:               s.cfg.DNS,
		AllowedIPs:        s.cfg.AllowedIPs,
		Keepalive:         s.cfg.PersistentKeepalive,
		MTU:               s.cfg.MTU,
		ServerPrivateKey:  priv,
		ServerPublicKey:   pub,
		ProtocolProfileID: spec.ProfileID,
		ProtocolParams:    params,
		ConfigRevision:    1,
		Clients:           []config.Client{},
		CreatedAt:         now,
		UpdatedAt:         now,
	}, nil
}

func tunnelConfigChanged(old, next config.Tunnel) bool {
	return old.ListenPort != next.ListenPort ||
		old.ServerHost != next.ServerHost ||
		old.ServerAddress != next.ServerAddress ||
		old.IPv4Subnet != next.IPv4Subnet ||
		old.DNS != next.DNS ||
		old.AllowedIPs != next.AllowedIPs ||
		old.Keepalive != next.Keepalive ||
		old.MTU != next.MTU ||
		old.ProtocolProfileID != next.ProtocolProfileID
}

func validateServerHost(host string) error {
	if host == "" {
		return nil
	}
	if strings.ContainsAny(host, " \t\r\n/\\") || strings.Contains(host, ":") {
		return errors.New("server host must be a hostname or IPv4 address without scheme, path, or port")
	}
	if len(host) > 253 {
		return errors.New("server host is too long")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() == nil {
			return errors.New("server host must be a hostname or IPv4 address")
		}
		return nil
	}
	if !serverHostRE.MatchString(host) {
		return errors.New("server host must be a valid hostname or IPv4 address")
	}
	return nil
}

func firewallRelevantChanged(old, next config.Tunnel) bool {
	return old.ListenPort != next.ListenPort ||
		old.IPv4Subnet != next.IPv4Subnet ||
		old.InterfaceName != next.InterfaceName ||
		old.EgressMode != next.EgressMode
}
