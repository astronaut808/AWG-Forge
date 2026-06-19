package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/astronaut808/awg-forge/internal/audit"
	"github.com/astronaut808/awg-forge/internal/config"
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
	mu    sync.Mutex
	cfg   config.Config
	store storage.Store
	audit audit.Logger
}

type TunnelStatus struct {
	TunnelID     string
	ApplyEnabled bool
	Up           bool
	LastRenderAt time.Time
	LastApplyAt  time.Time
	LastError    string
}

type ClientRuntimeStatus struct {
	Present         bool
	LatestHandshake string
	LastSeenAt      time.Time
	RxBytes         uint64
	TxBytes         uint64
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

type TunnelSettingsUpdate struct {
	Name       string
	ServerHost string
	EgressMode string
	Subnet     string
	DNS        string
	AllowedIPs string
	Keepalive  int
	MTU        int
	Port       int
	Enabled    bool
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
	return &Service{cfg: cfg, store: storage.New(cfg.ConfigDir), audit: audit.New(cfg)}
}

func (s *Service) Audit() audit.Logger {
	return s.audit
}

func (s *Service) log(level, event, message string, fields map[string]any, err error) {
	if s.audit == nil {
		return
	}
	entry := audit.Event{
		Level:   level,
		Event:   event,
		Message: message,
		Fields:  fields,
		Error:   audit.Error(err),
	}
	s.audit.Log(context.Background(), entry)
}

func (s *Service) State() (config.State, error) {
	return s.Init()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renderAllLocked()
}

func (s *Service) renderAllLocked() error {
	state, err := s.initLocked()
	if err != nil {
		return err
	}
	for _, tunnel := range state.Tunnels {
		if err := s.renderTunnelLocked(tunnel.ID, false); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) RenderTunnel(tunnelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renderTunnelLocked(tunnelID, true)
}

func (s *Service) renderTunnelLocked(tunnelID string, failOnApply bool) error {
	state, err := s.initLocked()
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
		s.log("error", "tunnel.render.failed", "rendered config write failed", tunnelAuditFields(state.Tunnels[idx]), err)
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
				s.log("error", "tunnel.apply.failed", "runtime apply failed", tunnelAuditFields(state.Tunnels[idx]), err)
				return &ApplyError{Err: err}
			}
			s.log("warn", "tunnel.apply.failed", "runtime apply failed but state was saved", tunnelAuditFields(state.Tunnels[idx]), err)
			return nil
		}
		if err := s.reconcileWarpRuntime(state); err != nil {
			state.Tunnels[idx].LastApplyError = err.Error()
			state.Tunnels[idx].UpdatedAt = now
			state.UpdatedAt = now
			if saveErr := s.store.Save(state); saveErr != nil {
				return errors.Join(fmt.Errorf("WARP apply failed: %w", err), fmt.Errorf("save state failed: %w", saveErr))
			}
			if failOnApply {
				s.log("error", "warp.apply.failed", "WARP runtime apply failed", tunnelAuditFields(state.Tunnels[idx]), err)
				return &ApplyError{Err: err}
			}
			s.log("warn", "warp.apply.failed", "WARP runtime apply failed but state was saved", tunnelAuditFields(state.Tunnels[idx]), err)
			return nil
		}
		state.Tunnels[idx].LastApplyAt = now
		s.log("info", "tunnel.apply.succeeded", "runtime tunnel applied", tunnelAuditFields(state.Tunnels[idx]), nil)
	}
	state.Tunnels[idx].UpdatedAt = now
	state.UpdatedAt = now
	if err := s.store.Save(state); err != nil {
		s.log("error", "state.save.failed", "state save failed after render", tunnelAuditFields(state.Tunnels[idx]), err)
		return err
	}
	return nil
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
	s.mu.Lock()
	defer s.mu.Unlock()
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
	state, err := s.initLocked()
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
	if err := s.renderTunnelLocked(tunnelID, true); err != nil {
		if rollbackErr := s.rollbackRenderedState(previousState, tunnelID); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr))
		}
		s.log("error", "tunnel.protocol.failed", "protocol update failed", map[string]any{"tunnel_id": tunnelID, "profile": profileID}, err)
		return err
	}
	s.log("info", "tunnel.protocol.updated", "protocol settings updated", tunnelAuditFields(state.Tunnels[idx]), nil)
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

func tunnelAuditFields(tunnel config.Tunnel) map[string]any {
	return map[string]any{
		"tunnel_id": tunnel.ID,
		"name":      tunnel.Name,
		"interface": tunnel.InterfaceName,
		"profile":   tunnel.ProtocolProfileID,
		"egress":    tunnel.EgressMode,
		"port":      tunnel.ListenPort,
		"subnet":    tunnel.IPv4Subnet,
		"enabled":   tunnel.Enabled,
		"revision":  tunnel.ConfigRevision,
	}
}

func clientAuditFields(tunnel config.Tunnel, client config.Client) map[string]any {
	fields := tunnelAuditFields(tunnel)
	fields["client_id"] = client.ID
	fields["client_name"] = client.Name
	fields["client_ip"] = client.IPv4Address
	fields["client_enabled"] = client.Enabled
	fields["client_revision"] = client.ConfigRevision
	return fields
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
