package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/buildinfo"
	"github.com/astronaut808/awg-forge/internal/config"
)

var releaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

var ErrRelayChanged = errors.New("relay changed since it was opened")

// RelayTargetSuggestion resolves the configured tunnel endpoint to one IPv4 address.
// An empty suggestion leaves the destination for the operator to enter.
func (s *Service) RelayTargetSuggestion(ctx context.Context, tunnelID string) (string, error) {
	state, err := s.Init()
	if err != nil {
		return "", err
	}
	idx, ok := tunnelIndexByID(state, tunnelID)
	if !ok {
		return "", os.ErrNotExist
	}
	endpoint, err := state.Tunnels[idx].ClientEndpoint(state.ServerHost, config.Client{})
	if err != nil {
		return "", err
	}
	host := strings.TrimSpace(endpoint.Host)
	if ip, err := netip.ParseAddr(host); err == nil {
		if config.IsRelayIPv4(ip) {
			return ip.String(), nil
		}
		return "", nil
	}
	host = strings.TrimSuffix(host, ".")
	if !strings.Contains(host, ".") || !serverHostRE.MatchString(host) {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host+".")
	if err != nil {
		return "", nil
	}
	var unique netip.Addr
	for _, ip := range addrs {
		ip = ip.Unmap()
		if !config.IsRelayIPv4(ip) {
			continue
		}
		if unique.IsValid() && ip != unique {
			return "", nil
		}
		unique = ip
	}
	if !unique.IsValid() {
		return "", nil
	}
	return unique.String(), nil
}

func (s *Service) SaveRelay(tunnelID, relayID string, relay config.Relay) (config.Relay, error) {
	return s.SaveRelayIfCurrent(tunnelID, relayID, relay, nil)
}

func (s *Service) SaveRelayIfCurrent(tunnelID, relayID string, relay config.Relay, expected *config.Relay) (config.Relay, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.initLocked()
	if err != nil {
		return config.Relay{}, err
	}
	idx, ok := tunnelIndexByID(state, tunnelID)
	if !ok {
		return config.Relay{}, errors.New("tunnel not found")
	}
	previous, err := cloneState(state)
	if err != nil {
		return config.Relay{}, err
	}
	tunnel := &state.Tunnels[idx]
	relay.Name = strings.TrimSpace(relay.Name)
	relay.Host = strings.TrimSpace(relay.Host)
	relay.TargetHost = strings.TrimSpace(relay.TargetHost)
	if relayID == "" {
		relay.ID = randomID()
		tunnel.Relays = append(tunnel.Relays, relay)
	} else {
		relay.ID = relayID
		found := false
		for i, old := range tunnel.Relays {
			if old.ID == relayID {
				if expected != nil && old != *expected {
					return config.Relay{}, ErrRelayChanged
				}
				tunnel.Relays[i] = relay
				found = true
				if old.Host != relay.Host || old.Port != relay.Port {
					for j := range tunnel.Clients {
						if tunnel.Clients[j].RelayID == relayID {
							tunnel.Clients[j].EndpointRevision++
						}
					}
				}
				break
			}
		}
		if !found {
			if expected != nil {
				return config.Relay{}, ErrRelayChanged
			}
			return config.Relay{}, errors.New("relay not found")
		}
	}
	if err := config.ValidateRelays(state); err != nil {
		return config.Relay{}, err
	}
	if err := s.saveRelayState(previous, state, idx); err != nil {
		return config.Relay{}, err
	}
	s.log("info", "relay.saved", "relay saved", map[string]any{"tunnel_id": tunnelID, "relay_id": relay.ID}, nil)
	return relay, nil
}

func (s *Service) RemoveRelay(tunnelID, relayID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.initLocked()
	if err != nil {
		return err
	}
	idx, ok := tunnelIndexByID(state, tunnelID)
	if !ok {
		return errors.New("tunnel not found")
	}
	tunnel := &state.Tunnels[idx]
	for _, c := range tunnel.Clients {
		if c.RelayID == relayID {
			return errors.New("relay is assigned to a client")
		}
	}
	for i, relay := range tunnel.Relays {
		if relay.ID == relayID {
			tunnel.Relays = append(tunnel.Relays[:i], tunnel.Relays[i+1:]...)
			state.UpdatedAt = time.Now().UTC()
			tunnel.UpdatedAt = state.UpdatedAt
			if err := s.store.Save(state); err != nil {
				return err
			}
			s.log("info", "relay.deleted", "relay deleted", map[string]any{"tunnel_id": tunnelID, "relay_id": relayID}, nil)
			return nil
		}
	}
	return errors.New("relay not found")
}

func (s *Service) saveRelayState(previous, state config.State, idx int) error {
	state.UpdatedAt = time.Now().UTC()
	state.Tunnels[idx].UpdatedAt = state.UpdatedAt
	if err := s.store.Save(state); err != nil {
		return err
	}
	id := state.Tunnels[idx].ID
	if err := s.writeRenderedTunnelFiles(state, id); err != nil {
		return &ApplyError{Err: errors.Join(err, s.rollbackRenderedState(previous, id))}
	}
	return nil
}

func (s *Service) RelayInstallCommand(tunnelID, relayID string) (string, error) {
	state, err := s.Init()
	if err != nil {
		return "", err
	}
	idx, ok := tunnelIndexByID(state, tunnelID)
	if !ok {
		return "", errors.New("tunnel not found")
	}
	tunnel := state.Tunnels[idx]
	relay, ok := tunnel.RelayByID(relayID)
	if !ok {
		return "", errors.New("relay not found")
	}
	version := buildinfo.Current().Version
	if !releaseTagPattern.MatchString(version) {
		return "", errors.New("relay installer command requires a versioned release")
	}
	return fmt.Sprintf("curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/%s/install.sh -o awg-forge-install.sh && sudo bash ./awg-forge-install.sh relay --version %s --id %s --listen-port %d --target %s:%d",
		version, version, relay.ID, relay.Port, relay.Target(), tunnel.ListenPort), nil
}
