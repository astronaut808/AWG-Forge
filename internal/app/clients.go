package app

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/keys"
	"github.com/astronaut808/awg-forge/internal/render"
)

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
		s.log("error", "client.create.failed", "client creation failed", clientAuditFields(state.Tunnels[idx], client), err)
		return config.Client{}, err
	}
	s.log("info", "client.created", "client created", clientAuditFields(state.Tunnels[idx], client), nil)
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
		var deleted config.Client
		for _, c := range state.Tunnels[ti].Clients {
			if c.ID == id {
				found = true
				deleted = c
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
				s.log("error", "client.delete.failed", "client deletion failed", map[string]any{"client_id": id, "tunnel_id": state.Tunnels[ti].ID}, err)
				return err
			}
			s.log("info", "client.deleted", "client deleted", clientAuditFields(state.Tunnels[ti], deleted), nil)
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
					s.log("error", "client.enabled.failed", "client enabled state update failed", clientAuditFields(state.Tunnels[ti], state.Tunnels[ti].Clients[ci]), err)
					return err
				}
				event := "client.disabled"
				message := "client disabled"
				if enabled {
					event = "client.enabled"
					message = "client enabled"
				}
				s.log("info", event, message, clientAuditFields(state.Tunnels[ti], state.Tunnels[ti].Clients[ci]), nil)
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
				s.log("info", "client.settings.updated", "client settings updated", clientAuditFields(state.Tunnels[ti], state.Tunnels[ti].Clients[ci]), nil)
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
	s.log("info", "client.config.rendered", "client config rendered", clientAuditFields(tunnel, client), nil)
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
	s.log("info", "client.config.downloaded", "client config downloaded", clientAuditFields(tunnel, client), nil)
	return conf, client, nil
}

func (s *Service) ClientImportKey(id string) (string, config.Client, error) {
	conf, client, err := s.ClientConfigForDownload(id)
	if err != nil {
		return "", config.Client{}, err
	}
	key := "vpn://" + base64.RawURLEncoding.EncodeToString([]byte(conf))
	s.log("info", "client.import_key.generated", "client import key generated", map[string]any{"client_id": client.ID, "client_name": client.Name}, nil)
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
