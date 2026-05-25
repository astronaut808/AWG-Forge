package app

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/firewall"
	"github.com/astronaut808/awg-forge/internal/keys"
	"github.com/astronaut808/awg-forge/internal/protocol"
	"github.com/astronaut808/awg-forge/internal/render"
	"github.com/astronaut808/awg-forge/internal/storage"
)

const (
	healthTrafficWarningThresholdBytes = uint64(1024)
	maxClientNotesLength               = 1000
)

var clientNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_. -]{0,62}[A-Za-z0-9]$|^[A-Za-z0-9]$`)
var tunnelNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,31}$`)
var serverHostRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
var transferRE = regexp.MustCompile(`^transfer:\s+(.+?) received,\s+(.+?) sent$`)

type Service struct {
	cfg   config.Config
	store storage.Store
}

type TunnelStatus struct {
	TunnelID     string
	ApplyEnabled bool
	Up           bool
	LastRenderAt time.Time
	LastApplyAt  time.Time
	LastError    string
}

type ClientHealth struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	Address         string `json:"address"`
	Present         bool   `json:"present"`
	Status          string `json:"status"`
	LatestHandshake string `json:"latest_handshake"`
	RxBytes         uint64 `json:"rx_bytes"`
	TxBytes         uint64 `json:"tx_bytes"`
	RxDeltaBytes    uint64 `json:"rx_delta_bytes"`
	TxDeltaBytes    uint64 `json:"tx_delta_bytes"`
	Warning         string `json:"warning,omitempty"`
}

type TunnelHealth struct {
	TunnelID      string         `json:"tunnel_id"`
	Name          string         `json:"name"`
	InterfaceName string         `json:"interface"`
	SampleSeconds int            `json:"sample_seconds"`
	Warnings      []string       `json:"warnings"`
	Clients       []ClientHealth `json:"clients"`
}

type ApplyError struct {
	Err error
}

func (e *ApplyError) Error() string {
	if e == nil || e.Err == nil {
		return "apply failed"
	}
	return "apply failed: " + e.Err.Error()
}

func (e *ApplyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func New(cfg config.Config) *Service {
	return &Service{cfg: cfg, store: storage.New(cfg.ConfigDir)}
}

func (s *Service) Init() (config.State, error) {
	if state, err := s.store.Load(); err == nil {
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

func (s *Service) State() (config.State, error) {
	return s.Init()
}

type tunnelSpec struct {
	ProfileID     string
	Name          string
	InterfaceName string
	ListenPort    int
	IPv4Subnet    string
}

func defaultTunnelSpec(profileID, name string, port int, subnet string) tunnelSpec {
	if name == "" {
		name = config.DefaultTunnel
	}
	return tunnelSpec{
		ProfileID:     profileID,
		Name:          name,
		InterfaceName: name,
		ListenPort:    port,
		IPv4Subnet:    subnet,
	}
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
	if profileID == "" {
		profileID = s.cfg.ProtocolProfile
	}
	suggestedName, suggestedPort, suggestedSubnet := SuggestedTunnelSpec(profileID)
	if name == "" {
		name = suggestedName
	}
	if port == 0 {
		port = suggestedPort
	}
	if subnet == "" {
		subnet = suggestedSubnet
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
	state, err := s.Init()
	if err != nil {
		return config.Tunnel{}, err
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
	state.Tunnels = append(state.Tunnels, tunnel)
	state.UpdatedAt = time.Now().UTC()
	if err := s.store.Save(state); err != nil {
		return config.Tunnel{}, err
	}
	if err := s.RenderTunnel(tunnel.ID); err != nil {
		var applyErr *ApplyError
		if errors.As(err, &applyErr) {
			if rollbackErr := s.rollbackRenderedState(previousState, "", tunnel.InterfaceName); rollbackErr != nil {
				return config.Tunnel{}, errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr))
			}
		}
		return config.Tunnel{}, err
	}
	return tunnel, nil
}

func (s *Service) UpdateTunnelSettings(tunnelID, name, serverHost, subnet, dns, allowedIPs string, keepalive, mtu, port int, enabled bool) (config.Tunnel, error) {
	state, err := s.Init()
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
	name = strings.TrimSpace(name)
	serverHost = strings.TrimSpace(serverHost)
	subnet = strings.TrimSpace(subnet)
	dns = strings.TrimSpace(dns)
	allowedIPs = strings.TrimSpace(allowedIPs)
	if name == "" {
		name = state.Tunnels[idx].Name
	}
	if subnet == "" {
		subnet = state.Tunnels[idx].IPv4Subnet
	}
	if dns == "" {
		dns = state.Tunnels[idx].DNS
	}
	if allowedIPs == "" {
		allowedIPs = state.Tunnels[idx].AllowedIPs
	}
	if !tunnelNameRE.MatchString(name) {
		return config.Tunnel{}, errors.New("tunnel name must start with a letter and contain only letters, numbers, dots, underscores, or dashes")
	}
	if err := validateServerHost(serverHost); err != nil {
		return config.Tunnel{}, err
	}
	if port < 1 || port > 65535 {
		return config.Tunnel{}, errors.New("listen port must be between 1 and 65535")
	}
	if keepalive < 0 || keepalive > 65535 {
		return config.Tunnel{}, errors.New("persistent keepalive must be between 0 and 65535")
	}
	if mtu != 0 && (mtu < 576 || mtu > 1500) {
		return config.Tunnel{}, errors.New("MTU must be auto or between 576 and 1500")
	}
	normalizedSubnet, _, err := normalizeIPv4CIDR(subnet)
	if err != nil {
		return config.Tunnel{}, err
	}
	subnet = normalizedSubnet
	serverIP, err := serverAddress(subnet)
	if err != nil {
		return config.Tunnel{}, err
	}
	for i, tunnel := range state.Tunnels {
		if i == idx {
			continue
		}
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
	if subnet != state.Tunnels[idx].IPv4Subnet && len(state.Tunnels[idx].Clients) > 0 {
		return config.Tunnel{}, errors.New("cannot change subnet while tunnel has clients")
	}
	old := state.Tunnels[idx]
	oldInterface := old.InterfaceName
	now := time.Now().UTC()
	state.Tunnels[idx].Name = name
	state.Tunnels[idx].InterfaceName = name
	state.Tunnels[idx].ServerHost = serverHost
	state.Tunnels[idx].ListenPort = port
	state.Tunnels[idx].IPv4Subnet = subnet
	state.Tunnels[idx].ServerAddress = serverIP
	state.Tunnels[idx].DNS = dns
	state.Tunnels[idx].AllowedIPs = allowedIPs
	state.Tunnels[idx].Keepalive = keepalive
	state.Tunnels[idx].MTU = mtu
	state.Tunnels[idx].Enabled = enabled
	if tunnelConfigChanged(old, state.Tunnels[idx]) {
		state.Tunnels[idx].ConfigRevision++
	}
	state.Tunnels[idx].UpdatedAt = now
	state.UpdatedAt = now
	if err := s.store.Save(state); err != nil {
		return config.Tunnel{}, err
	}
	if s.cfg.ApplyConfig && firewallRelevantChanged(old, state.Tunnels[idx]) {
		if oldInterface != name {
			_ = exec.Command("awg-quick", "down", oldInterface).Run()
		}
		_ = s.cleanupFirewallRules(old)
	}
	if oldInterface != name {
		_ = s.store.DeleteRenderedTunnel(oldInterface)
	}
	if err := s.RenderTunnel(state.Tunnels[idx].ID); err != nil {
		var applyErr *ApplyError
		if errors.As(err, &applyErr) {
			deleteRendered := []string{}
			if oldInterface != name {
				deleteRendered = append(deleteRendered, name)
			}
			if rollbackErr := s.rollbackRuntimeState(previousState, old.ID, deleteRendered...); rollbackErr != nil {
				return config.Tunnel{}, errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr))
			}
		}
		return config.Tunnel{}, err
	}
	return state.Tunnels[idx], nil
}

func (s *Service) DeleteTunnel(tunnelID string) error {
	state, err := s.Init()
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
			return &ApplyError{Err: err}
		}
		if err := s.cleanupFirewallRules(tunnel); err != nil {
			if rollbackErr := s.rollbackRuntimeState(previousState, tunnel.ID); rollbackErr != nil {
				return errors.Join(&ApplyError{Err: err}, fmt.Errorf("rollback failed: %w", rollbackErr))
			}
			return &ApplyError{Err: err}
		}
	}
	return s.store.DeleteRenderedTunnel(tunnel.InterfaceName)
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

func (s *Service) AddClient(name string) (config.Client, error) {
	state, err := s.Init()
	if err != nil {
		return config.Client{}, err
	}
	if len(state.Tunnels) == 0 {
		return config.Client{}, errors.New("no tunnels configured")
	}
	return s.AddClientToTunnel(state.Tunnels[0].ID, name)
}

func (s *Service) AddClientToTunnel(tunnelID, name string) (config.Client, error) {
	if !clientNameRE.MatchString(name) {
		return config.Client{}, errors.New("client name must be 1-64 chars and contain only letters, numbers, spaces, dots, underscores, or dashes")
	}
	state, err := s.Init()
	if err != nil {
		return config.Client{}, err
	}
	idx, ok := tunnelIndexByID(state, tunnelID)
	if !ok {
		return config.Client{}, errors.New("tunnel not found")
	}
	previousState, err := cloneState(state)
	if err != nil {
		return config.Client{}, err
	}
	ip, err := nextClientIP(state.Tunnels[idx])
	if err != nil {
		return config.Client{}, err
	}
	priv, pub, err := keys.PrivateKey()
	if err != nil {
		return config.Client{}, err
	}
	psk, err := keys.PresharedKey()
	if err != nil {
		return config.Client{}, err
	}
	now := time.Now().UTC()
	client := config.Client{
		ID: randomID(), TunnelID: state.Tunnels[idx].ID, Name: name, Enabled: true, IPv4Address: ip,
		PrivateKey: priv, PublicKey: pub, PresharedKey: psk,
		ConfigRevision: state.Tunnels[idx].ConfigRevision,
		CreatedAt:      now, UpdatedAt: now,
	}
	state.Tunnels[idx].Clients = append(state.Tunnels[idx].Clients, client)
	state.Tunnels[idx].UpdatedAt = now
	state.UpdatedAt = now
	if err := s.store.Save(state); err != nil {
		return config.Client{}, err
	}
	if err := s.RenderTunnel(state.Tunnels[idx].ID); err != nil {
		var applyErr *ApplyError
		if errors.As(err, &applyErr) {
			if rollbackErr := s.rollbackRenderedState(previousState, state.Tunnels[idx].ID); rollbackErr != nil {
				return config.Client{}, errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr))
			}
		}
		return config.Client{}, err
	}
	return client, nil
}

func (s *Service) RemoveClient(id string) error {
	state, err := s.Init()
	if err != nil {
		return err
	}
	previousState, err := cloneState(state)
	if err != nil {
		return err
	}
	for ti := range state.Tunnels {
		clients := state.Tunnels[ti].Clients[:0]
		found := false
		for _, c := range state.Tunnels[ti].Clients {
			if c.ID == id {
				found = true
				continue
			}
			clients = append(clients, c)
		}
		if found {
			state.Tunnels[ti].Clients = clients
			state.Tunnels[ti].UpdatedAt = time.Now().UTC()
			state.UpdatedAt = state.Tunnels[ti].UpdatedAt
			if err := s.store.Save(state); err != nil {
				return err
			}
			if err := s.RenderTunnel(state.Tunnels[ti].ID); err != nil {
				var applyErr *ApplyError
				if errors.As(err, &applyErr) {
					if rollbackErr := s.rollbackRenderedState(previousState, state.Tunnels[ti].ID); rollbackErr != nil {
						return errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr))
					}
				}
				return err
			}
			return nil
		}
	}
	return errors.New("client not found")
}

func (s *Service) SetClientEnabled(id string, enabled bool) error {
	state, err := s.Init()
	if err != nil {
		return err
	}
	previousState, err := cloneState(state)
	if err != nil {
		return err
	}
	for ti := range state.Tunnels {
		for ci := range state.Tunnels[ti].Clients {
			if state.Tunnels[ti].Clients[ci].ID == id {
				now := time.Now().UTC()
				state.Tunnels[ti].Clients[ci].Enabled = enabled
				state.Tunnels[ti].Clients[ci].UpdatedAt = now
				state.Tunnels[ti].UpdatedAt = now
				state.UpdatedAt = now
				if err := s.store.Save(state); err != nil {
					return err
				}
				if err := s.RenderTunnel(state.Tunnels[ti].ID); err != nil {
					var applyErr *ApplyError
					if errors.As(err, &applyErr) {
						if rollbackErr := s.rollbackRenderedState(previousState, state.Tunnels[ti].ID); rollbackErr != nil {
							return errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr))
						}
					}
					return err
				}
				return nil
			}
		}
	}
	return errors.New("client not found")
}

func (s *Service) UpdateClientSettings(id, name, notes string) (config.Client, error) {
	name = strings.TrimSpace(name)
	if !clientNameRE.MatchString(name) {
		return config.Client{}, errors.New("client name must be 1-64 chars and contain only letters, numbers, spaces, dots, underscores, or dashes")
	}
	notes = strings.TrimSpace(notes)
	if len(notes) > maxClientNotesLength {
		return config.Client{}, fmt.Errorf("client notes must be at most %d bytes", maxClientNotesLength)
	}
	state, err := s.Init()
	if err != nil {
		return config.Client{}, err
	}
	for ti := range state.Tunnels {
		for ci := range state.Tunnels[ti].Clients {
			if state.Tunnels[ti].Clients[ci].ID == id {
				now := time.Now().UTC()
				state.Tunnels[ti].Clients[ci].Name = name
				state.Tunnels[ti].Clients[ci].Notes = notes
				state.Tunnels[ti].Clients[ci].UpdatedAt = now
				state.Tunnels[ti].UpdatedAt = now
				state.UpdatedAt = now
				if err := s.store.Save(state); err != nil {
					return config.Client{}, err
				}
				return state.Tunnels[ti].Clients[ci], nil
			}
		}
	}
	return config.Client{}, errors.New("client not found")
}

func (s *Service) ClientConfig(id string) (string, error) {
	state, err := s.Init()
	if err != nil {
		return "", err
	}
	tunnel, client, ok := findClient(state, id)
	if !ok {
		return "", errors.New("client not found")
	}
	conf, err := render.ClientConfig(state, tunnel, client)
	if err != nil {
		return "", err
	}
	_ = s.markClientConfigDelivered(id)
	return conf, nil
}

func (s *Service) ClientConfigForDownload(id string) (string, config.Client, error) {
	state, err := s.Init()
	if err != nil {
		return "", config.Client{}, err
	}
	tunnel, client, ok := findClient(state, id)
	if !ok {
		return "", config.Client{}, errors.New("client not found")
	}
	conf, err := render.ClientConfig(state, tunnel, client)
	if err != nil {
		return "", config.Client{}, err
	}
	_ = s.markClientConfigDelivered(id)
	return conf, client, nil
}

func (s *Service) ClientImportKey(id string) (string, config.Client, error) {
	conf, client, err := s.ClientConfigForDownload(id)
	if err != nil {
		return "", config.Client{}, err
	}
	key := "vpn://" + base64.RawURLEncoding.EncodeToString([]byte(conf))
	return key, client, nil
}

func (s *Service) markClientConfigDelivered(id string) error {
	state, err := s.Init()
	if err != nil {
		return err
	}
	for ti := range state.Tunnels {
		for ci := range state.Tunnels[ti].Clients {
			if state.Tunnels[ti].Clients[ci].ID == id {
				if state.Tunnels[ti].Clients[ci].ConfigRevision == state.Tunnels[ti].ConfigRevision {
					return nil
				}
				now := time.Now().UTC()
				state.Tunnels[ti].Clients[ci].ConfigRevision = state.Tunnels[ti].ConfigRevision
				state.Tunnels[ti].Clients[ci].UpdatedAt = now
				state.Tunnels[ti].UpdatedAt = now
				state.UpdatedAt = now
				return s.store.Save(state)
			}
		}
	}
	return errors.New("client not found")
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
		old.InterfaceName != next.InterfaceName
}

func (s *Service) SessionSecret() (string, error) {
	if s.cfg.SessionSecret != "" {
		return s.cfg.SessionSecret, nil
	}
	state, err := s.Init()
	if err != nil {
		return "", err
	}
	return state.SessionSecret, nil
}

func (s *Service) RenderAll() error {
	state, err := s.Init()
	if err != nil {
		return err
	}
	for _, tunnel := range state.Tunnels {
		if err := s.renderTunnel(tunnel.ID, false); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) RenderTunnel(tunnelID string) error {
	return s.renderTunnel(tunnelID, true)
}

func (s *Service) renderTunnel(tunnelID string, failOnApply bool) error {
	state, err := s.Init()
	if err != nil {
		return err
	}
	return s.renderTunnelFromState(state, tunnelID, failOnApply)
}

func (s *Service) renderTunnelFromState(state config.State, tunnelID string, failOnApply bool) error {
	idx, ok := tunnelIndexByID(state, tunnelID)
	if !ok {
		return errors.New("tunnel not found")
	}
	if err := s.writeRenderedTunnelFiles(state, tunnelID); err != nil {
		return err
	}
	now := time.Now().UTC()
	state.Tunnels[idx].LastRenderAt = now
	state.Tunnels[idx].LastApplyError = ""
	if s.cfg.ApplyConfig && state.Tunnels[idx].Enabled {
		if err := s.apply(state.Tunnels[idx]); err != nil {
			state.Tunnels[idx].LastApplyError = err.Error()
			state.Tunnels[idx].UpdatedAt = now
			state.UpdatedAt = now
			if saveErr := s.store.Save(state); saveErr != nil {
				return errors.Join(fmt.Errorf("apply failed: %w", err), fmt.Errorf("save state failed: %w", saveErr))
			}
			if failOnApply {
				return &ApplyError{Err: err}
			}
			return nil
		}
		state.Tunnels[idx].LastApplyAt = now
	}
	state.Tunnels[idx].UpdatedAt = now
	state.UpdatedAt = now
	return s.store.Save(state)
}

func (s *Service) writeRenderedTunnelFiles(state config.State, tunnelID string) error {
	idx, ok := tunnelIndexByID(state, tunnelID)
	if !ok {
		return errors.New("tunnel not found")
	}
	tunnel := state.Tunnels[idx]
	serverConf, err := render.ServerConfig(state, tunnel)
	if err != nil {
		return err
	}
	clients := make(map[string]string)
	for _, c := range tunnel.Clients {
		conf, err := render.ClientConfig(state, tunnel, c)
		if err != nil {
			return err
		}
		clients[c.ID] = conf
	}
	return s.store.WriteRenderedTunnel(tunnel, serverConf, clients)
}

func (s *Service) rollbackRenderedState(previous config.State, tunnelID string, deleteRendered ...string) error {
	if err := s.store.Save(previous); err != nil {
		return err
	}
	if tunnelID != "" {
		if err := s.writeRenderedTunnelFiles(previous, tunnelID); err != nil {
			return err
		}
	}
	for _, interfaceName := range deleteRendered {
		if err := s.store.DeleteRenderedTunnel(interfaceName); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) rollbackRuntimeState(previous config.State, tunnelID string, deleteRendered ...string) error {
	if err := s.rollbackRenderedState(previous, tunnelID, deleteRendered...); err != nil {
		return err
	}
	if !s.cfg.ApplyConfig || tunnelID == "" {
		return nil
	}
	idx, ok := tunnelIndexByID(previous, tunnelID)
	if !ok || !previous.Tunnels[idx].Enabled {
		return nil
	}
	if err := s.apply(previous.Tunnels[idx]); err != nil {
		return fmt.Errorf("runtime rollback apply failed: %w", err)
	}
	return nil
}

func (s *Service) UpdateProtocol(profileID string, params config.ProtocolParams) error {
	state, err := s.Init()
	if err != nil {
		return err
	}
	if len(state.Tunnels) == 0 {
		return errors.New("no tunnels configured")
	}
	return s.UpdateTunnelProtocol(state.Tunnels[0].ID, profileID, params)
}

func (s *Service) UpdateTunnelProtocol(tunnelID, profileID string, params config.ProtocolParams) error {
	p, ok := protocol.ByID(profileID)
	if !ok {
		return fmt.Errorf("unsupported protocol profile %q", profileID)
	}
	defaults, err := p.GenerateDefaults()
	if err != nil {
		return err
	}
	for key, value := range defaults {
		if _, ok := params[key]; !ok {
			params[key] = value
		}
		if strings.HasPrefix(key, "I") && params[key] == "" {
			params[key] = value
		}
	}
	if err := p.Validate(params); err != nil {
		return err
	}
	state, err := s.Init()
	if err != nil {
		return err
	}
	idx, ok := tunnelIndexByID(state, tunnelID)
	if !ok {
		return errors.New("tunnel not found")
	}
	previousState, err := cloneState(state)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	state.Tunnels[idx].ProtocolProfileID = profileID
	state.Tunnels[idx].ProtocolParams = params
	state.Tunnels[idx].ConfigRevision++
	state.Tunnels[idx].UpdatedAt = now
	state.UpdatedAt = now
	if err := s.store.Save(state); err != nil {
		return err
	}
	if err := s.RenderTunnel(tunnelID); err != nil {
		var applyErr *ApplyError
		if errors.As(err, &applyErr) {
			if rollbackErr := s.rollbackRenderedState(previousState, tunnelID); rollbackErr != nil {
				return errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr))
			}
		}
		return err
	}
	return nil
}

func (s *Service) RegenerateProtocol(profileID string) error {
	state, err := s.Init()
	if err != nil {
		return err
	}
	if len(state.Tunnels) == 0 {
		return errors.New("no tunnels configured")
	}
	return s.RegenerateTunnelProtocol(state.Tunnels[0].ID, profileID)
}

func (s *Service) RegenerateTunnelProtocol(tunnelID, profileID string) error {
	p, ok := protocol.ByID(profileID)
	if !ok {
		return fmt.Errorf("unsupported protocol profile %q", profileID)
	}
	params, err := p.GenerateDefaults()
	if err != nil {
		return err
	}
	return s.UpdateTunnelProtocol(tunnelID, profileID, params)
}

func (s *Service) RestartTunnel() error {
	state, err := s.Init()
	if err != nil {
		return err
	}
	if len(state.Tunnels) == 0 {
		return errors.New("no tunnels configured")
	}
	return s.RestartTunnelByID(state.Tunnels[0].ID)
}

func (s *Service) RestartTunnelByID(tunnelID string) error {
	state, err := s.Init()
	if err != nil {
		return err
	}
	idx, ok := tunnelIndexByID(state, tunnelID)
	if !ok {
		return errors.New("tunnel not found")
	}
	if !s.cfg.ApplyConfig {
		state.Tunnels[idx].LastApplyError = "APPLY_CONFIG=false; tunnel restart skipped"
		state.Tunnels[idx].UpdatedAt = time.Now().UTC()
		state.UpdatedAt = state.Tunnels[idx].UpdatedAt
		return s.store.Save(state)
	}
	_ = exec.Command("awg-quick", "down", state.Tunnels[idx].InterfaceName).Run()
	if err := s.RenderTunnel(tunnelID); err != nil {
		return err
	}
	state, err = s.store.Load()
	if err != nil {
		return err
	}
	idx, ok = tunnelIndexByID(state, tunnelID)
	if !ok {
		return errors.New("tunnel not found")
	}
	if state.Tunnels[idx].LastApplyError != "" {
		return errors.New(state.Tunnels[idx].LastApplyError)
	}
	return nil
}

func (s *Service) TunnelStatus() (TunnelStatus, error) {
	state, err := s.Init()
	if err != nil {
		return TunnelStatus{}, err
	}
	if len(state.Tunnels) == 0 {
		return TunnelStatus{}, errors.New("no tunnels configured")
	}
	return s.TunnelStatusByID(state.Tunnels[0].ID)
}

func (s *Service) TunnelStatusByID(tunnelID string) (TunnelStatus, error) {
	state, err := s.Init()
	if err != nil {
		return TunnelStatus{}, err
	}
	idx, ok := tunnelIndexByID(state, tunnelID)
	if !ok {
		return TunnelStatus{}, errors.New("tunnel not found")
	}
	tunnel := state.Tunnels[idx]
	return TunnelStatus{
		TunnelID:     tunnel.ID,
		ApplyEnabled: s.cfg.ApplyConfig,
		Up:           exec.Command("ip", "link", "show", tunnel.InterfaceName).Run() == nil,
		LastRenderAt: tunnel.LastRenderAt,
		LastApplyAt:  tunnel.LastApplyAt,
		LastError:    tunnel.LastApplyError,
	}, nil
}

func (s *Service) TunnelHealthByID(tunnelID string, sampleSeconds int) (TunnelHealth, error) {
	if sampleSeconds <= 0 {
		sampleSeconds = 2
	}
	if sampleSeconds > 10 {
		sampleSeconds = 10
	}
	state, err := s.Init()
	if err != nil {
		return TunnelHealth{}, err
	}
	idx, ok := tunnelIndexByID(state, tunnelID)
	if !ok {
		return TunnelHealth{}, errors.New("tunnel not found")
	}
	tunnel := state.Tunnels[idx]
	first, err := runtimeAWGShow(tunnel.InterfaceName)
	if err != nil {
		return TunnelHealth{}, err
	}
	time.Sleep(time.Duration(sampleSeconds) * time.Second)
	second, err := runtimeAWGShow(tunnel.InterfaceName)
	if err != nil {
		return TunnelHealth{}, err
	}
	health := TunnelHealth{
		TunnelID:      tunnel.ID,
		Name:          tunnel.Name,
		InterfaceName: tunnel.InterfaceName,
		SampleSeconds: sampleSeconds,
	}
	if !hasNATRule(tunnel.IPv4Subnet, s.cfg.ExternalInterface) {
		health.Warnings = append(health.Warnings, "possible NAT issue: missing MASQUERADE for "+tunnel.IPv4Subnet+" on "+s.cfg.ExternalInterface)
	}
	if !hasFilterRule("FORWARD", "-i", tunnel.InterfaceName, "-j", "ACCEPT") || !hasFilterRule("FORWARD", "-o", tunnel.InterfaceName, "-j", "ACCEPT") {
		health.Warnings = append(health.Warnings, "possible forwarding issue: missing FORWARD accept rules for "+tunnel.InterfaceName)
	}
	for _, client := range tunnel.Clients {
		item := ClientHealth{
			ID:      client.ID,
			Name:    client.Name,
			Enabled: client.Enabled,
			Address: client.IPv4Address,
			Status:  "disabled",
		}
		if !client.Enabled {
			health.Clients = append(health.Clients, item)
			continue
		}
		nextPeer, ok := second.Peers[client.PublicKey]
		if !ok {
			item.Status = "missing runtime peer"
			item.Warning = "enabled client is not present in awg runtime"
			health.Clients = append(health.Clients, item)
			continue
		}
		item.Present = true
		item.LatestHandshake = nextPeer.LatestHandshake
		item.RxBytes = nextPeer.RxBytes
		item.TxBytes = nextPeer.TxBytes
		if prevPeer, ok := first.Peers[client.PublicKey]; ok {
			item.RxDeltaBytes = byteDelta(prevPeer.RxBytes, nextPeer.RxBytes)
			item.TxDeltaBytes = byteDelta(prevPeer.TxBytes, nextPeer.TxBytes)
		}
		switch {
		case item.LatestHandshake == "":
			item.Status = "never connected"
			item.Warning = "no handshake yet"
		case item.RxDeltaBytes >= healthTrafficWarningThresholdBytes && item.TxDeltaBytes == 0:
			item.Status = "client sends traffic, server sends 0 bytes back"
			item.Warning = "possible NAT, forwarding, route, DNS, or upstream firewall issue"
		case item.RxDeltaBytes < healthTrafficWarningThresholdBytes && item.TxDeltaBytes == 0:
			item.Status = "idle, handshake ok"
		case item.RxDeltaBytes == 0 && item.TxDeltaBytes > 0:
			item.Status = "outbound only"
			item.Warning = "server sent traffic, but client traffic did not increase during sample window"
		default:
			item.Status = "traffic flowing"
		}
		health.Clients = append(health.Clients, item)
	}
	return health, nil
}

func (s *Service) FirewallCheck() (firewall.Report, error) {
	state, err := s.Init()
	if err != nil {
		return firewall.Report{}, err
	}
	return firewall.Check(s.cfg, state, firewall.IPTablesRunner{}), nil
}

func (s *Service) FirewallRepair() (firewall.Report, error) {
	state, err := s.Init()
	if err != nil {
		return firewall.Report{}, err
	}
	return firewall.Repair(s.cfg, state, firewall.IPTablesRunner{})
}

func (s *Service) apply(tunnel config.Tunnel) error {
	serverPath := filepath.Join(s.cfg.ConfigDir, "tunnels", tunnel.InterfaceName, "server.conf")
	runtimePath := filepath.Join("/etc/amnezia/amneziawg", tunnel.InterfaceName+".conf")
	if err := copyRuntimeConfig(serverPath, runtimePath); err != nil {
		return err
	}
	if err := exec.Command("ip", "link", "show", tunnel.InterfaceName).Run(); err != nil {
		if err := runCommand("awg-quick", "up", tunnel.InterfaceName); err != nil {
			return err
		}
		return s.ensureFirewallRules(tunnel)
	}
	stripped, err := exec.Command("awg-quick", "strip", runtimePath).Output()
	if err != nil {
		return err
	}
	cmd := exec.Command("awg", "syncconf", tunnel.InterfaceName, "/dev/stdin")
	cmd.Stdin = strings.NewReader(string(stripped))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("awg syncconf failed: %s", strings.TrimSpace(string(out)))
	}
	return s.ensureFirewallRules(tunnel)
}

func (s *Service) ensureFirewallRules(tunnel config.Tunnel) error {
	_, err := firewall.Repair(s.cfg, config.State{Tunnels: []config.Tunnel{tunnel}}, firewall.IPTablesRunner{})
	return err
}

func (s *Service) cleanupFirewallRules(tunnel config.Tunnel) error {
	rules := []iptablesRule{
		{table: "nat", args: []string{"POSTROUTING", "-s", tunnel.IPv4Subnet, "-o", s.cfg.ExternalInterface, "-j", "MASQUERADE"}},
		{args: []string{"INPUT", "-p", "udp", "-m", "udp", "--dport", strconv.Itoa(tunnel.ListenPort), "-j", "ACCEPT"}},
		{args: []string{"FORWARD", "-i", tunnel.InterfaceName, "-j", "ACCEPT"}},
		{args: []string{"FORWARD", "-o", tunnel.InterfaceName, "-j", "ACCEPT"}},
	}
	var errs []string
	for _, rule := range rules {
		if err := deleteAllIPTablesRules(rule); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

type iptablesRule struct {
	table string
	args  []string
}

func deleteAllIPTablesRules(rule iptablesRule) error {
	for i := 0; i < 64; i++ {
		if !iptablesRuleExists(rule) {
			return nil
		}
		args := append([]string{}, iptablesTableArgs(rule.table)...)
		args = append(args, "-D")
		args = append(args, rule.args...)
		out, err := exec.Command("iptables", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("iptables %s failed: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
		}
	}
	return fmt.Errorf("iptables duplicate cleanup limit reached for %s", strings.Join(rule.args, " "))
}

func iptablesRuleExists(rule iptablesRule) bool {
	if err := iptablesCheck(rule); err != nil {
		return false
	}
	return true
}

func iptablesCheck(rule iptablesRule) error {
	args := append([]string{}, iptablesTableArgs(rule.table)...)
	args = append(args, "-C")
	args = append(args, rule.args...)
	return exec.Command("iptables", args...).Run()
}

func iptablesTableArgs(table string) []string {
	if table == "" {
		return nil
	}
	return []string{"-t", table}
}

type runtimeInterface struct {
	Peers map[string]runtimePeer
}

type runtimePeer struct {
	LatestHandshake string
	RxBytes         uint64
	TxBytes         uint64
}

func runtimeAWGShow(interfaceName string) (runtimeInterface, error) {
	out, err := exec.Command("awg", "show", interfaceName).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return runtimeInterface{}, fmt.Errorf("awg show %s failed: %s", interfaceName, msg)
	}
	return parseRuntimeAWGShow(string(out)), nil
}

func parseRuntimeAWGShow(out string) runtimeInterface {
	result := runtimeInterface{Peers: map[string]runtimePeer{}}
	var currentKey string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "peer: ") {
			currentKey = strings.TrimSpace(strings.TrimPrefix(line, "peer: "))
			result.Peers[currentKey] = runtimePeer{}
			continue
		}
		if currentKey == "" {
			continue
		}
		peer := result.Peers[currentKey]
		switch {
		case strings.HasPrefix(line, "latest handshake: "):
			peer.LatestHandshake = strings.TrimSpace(strings.TrimPrefix(line, "latest handshake: "))
		case transferRE.MatchString(line):
			match := transferRE.FindStringSubmatch(line)
			peer.RxBytes = parseByteQuantity(match[1])
			peer.TxBytes = parseByteQuantity(match[2])
		}
		result.Peers[currentKey] = peer
	}
	return result
}

func parseByteQuantity(value string) uint64 {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	unit := "B"
	if len(fields) > 1 {
		unit = strings.ToLower(fields[1])
	}
	multiplier := float64(1)
	switch unit {
	case "kib":
		multiplier = 1024
	case "mib":
		multiplier = 1024 * 1024
	case "gib":
		multiplier = 1024 * 1024 * 1024
	case "tib":
		multiplier = 1024 * 1024 * 1024 * 1024
	case "kb":
		multiplier = 1000
	case "mb":
		multiplier = 1000 * 1000
	case "gb":
		multiplier = 1000 * 1000 * 1000
	case "tb":
		multiplier = 1000 * 1000 * 1000 * 1000
	}
	if n <= 0 {
		return 0
	}
	return uint64(n * multiplier)
}

func byteDelta(before, after uint64) uint64 {
	if after < before {
		return 0
	}
	return after - before
}

func hasNATRule(subnet, externalInterface string) bool {
	return exec.Command("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", subnet, "-o", externalInterface, "-j", "MASQUERADE").Run() == nil
}

func hasFilterRule(chain string, args ...string) bool {
	cmdArgs := append([]string{"-C", chain}, args...)
	return exec.Command("iptables", cmdArgs...).Run() == nil
}

func runCommand(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s failed: %s", name, strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func copyRuntimeConfig(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0600)
}

func findClient(state config.State, id string) (config.Tunnel, config.Client, bool) {
	for _, tunnel := range state.Tunnels {
		for _, c := range tunnel.Clients {
			if c.ID == id {
				return tunnel, c, true
			}
		}
	}
	return config.Tunnel{}, config.Client{}, false
}

func tunnelIndexByID(state config.State, id string) (int, bool) {
	for i, tunnel := range state.Tunnels {
		if tunnel.ID == id || tunnel.Name == id || tunnel.InterfaceName == id {
			return i, true
		}
	}
	return 0, false
}

func randomID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (s *Service) sessionSecretValue() (string, error) {
	if s.cfg.SessionSecret != "" {
		return s.cfg.SessionSecret, nil
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func normalizeIPv4CIDR(cidr string) (string, *net.IPNet, error) {
	ip, ipnet, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil {
		return "", nil, err
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return "", nil, errors.New("IPv4 subnet required")
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 32 {
		return "", nil, errors.New("IPv4 subnet required")
	}
	if ones < 16 {
		return "", nil, errors.New("subnet too large")
	}
	if ones > 30 {
		return "", nil, errors.New("subnet too small")
	}
	network := ip4.Mask(ipnet.Mask)
	normalized := &net.IPNet{IP: network, Mask: ipnet.Mask}
	return fmt.Sprintf("%s/%d", network.String(), ones), normalized, nil
}

func serverAddress(cidr string) (string, error) {
	_, ipnet, err := normalizeIPv4CIDR(cidr)
	if err != nil {
		return "", err
	}
	network := ipv4ToUint(ipnet.IP)
	broadcast := broadcastIPv4(ipnet)
	if network+1 >= broadcast {
		return "", errors.New("subnet too small")
	}
	return uintToIPv4(network + 1).String(), nil
}

func nextClientIP(tunnel config.Tunnel) (string, error) {
	_, ipnet, err := normalizeIPv4CIDR(tunnel.IPv4Subnet)
	if err != nil {
		return "", err
	}
	used := map[string]bool{tunnel.ServerAddress: true}
	for _, c := range tunnel.Clients {
		used[c.IPv4Address] = true
	}
	network := ipv4ToUint(ipnet.IP)
	broadcast := broadcastIPv4(ipnet)
	for n := network + 2; n < broadcast; n++ {
		candidate := uintToIPv4(n).String()
		if !used[candidate] {
			return candidate, nil
		}
	}
	return "", errors.New("no free client IPs")
}

func subnetsOverlap(a, b string) bool {
	_, aNet, errA := normalizeIPv4CIDR(a)
	_, bNet, errB := normalizeIPv4CIDR(b)
	if errA != nil || errB != nil {
		return false
	}
	return aNet.Contains(bNet.IP) || bNet.Contains(aNet.IP)
}

func cloneState(state config.State) (config.State, error) {
	b, err := json.Marshal(state)
	if err != nil {
		return config.State{}, err
	}
	var cloned config.State
	if err := json.Unmarshal(b, &cloned); err != nil {
		return config.State{}, err
	}
	return cloned, nil
}

func ipv4ToUint(ip net.IP) uint32 {
	return binary.BigEndian.Uint32(ip.To4())
}

func uintToIPv4(v uint32) net.IP {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return net.IPv4(b[0], b[1], b[2], b[3])
}

func broadcastIPv4(ipnet *net.IPNet) uint32 {
	network := ipv4ToUint(ipnet.IP)
	mask := binary.BigEndian.Uint32(ipnet.Mask)
	return network | ^mask
}
