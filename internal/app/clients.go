package app

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/keys"
	"github.com/astronaut808/awg-forge/internal/render"
	"github.com/astronaut808/awg-forge/internal/sqldb"
)

type ClientCreateOptions struct {
	ExpiresAt       time.Time
	Persist         func(config.Client) error
	RollbackPersist func(config.Client) error
}

type ClientSettingsUpdate struct {
	Name      string
	Notes     string
	ExpiresAt time.Time
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
	return s.AddClientToTunnelWithOptions(tunnelID, name, ClientCreateOptions{})
}

func (s *Service) AddClientToTunnelWithOptions(tunnelID, name string, opts ClientCreateOptions) (config.Client, error) {
	if err := s.lockStateMutation(); err != nil {
		return config.Client{}, err
	}
	defer s.unlockStateMutation()
	if opts.Persist != nil && opts.RollbackPersist == nil {
		return config.Client{}, errors.New("client persistence requires a rollback")
	}
	if !clientNameRE.MatchString(name) {
		return config.Client{}, errors.New("client name must be 1-64 chars and contain only letters, numbers, spaces, dots, underscores, or dashes")
	}
	state, err := s.initLocked()
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
		ExpiresAt:      opts.ExpiresAt.UTC(),
		CreatedAt:      now, UpdatedAt: now,
	}
	if opts.Persist != nil {
		if err := opts.Persist(client); err != nil {
			return config.Client{}, err
		}
	}
	rollbackPersist := func(err error) error {
		if opts.RollbackPersist == nil {
			return err
		}
		if rollbackErr := opts.RollbackPersist(client); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("client persistence rollback failed: %w", rollbackErr))
		}
		return err
	}
	state.Tunnels[idx].Clients = append(state.Tunnels[idx].Clients, client)
	state.Tunnels[idx].UpdatedAt = now
	state.UpdatedAt = now
	if err := s.saveLocalDesiredState(&state); err != nil {
		return config.Client{}, rollbackPersist(err)
	}
	if err := s.renderTunnelLocked(state.Tunnels[idx].ID, true); err != nil {
		if rollbackErr := s.rollbackRuntimeState(previousState, state.Tunnels[idx].ID); rollbackErr != nil {
			return config.Client{}, rollbackPersist(errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr)))
		}
		s.log("error", "client.create.failed", "client creation failed", clientAuditFields(state.Tunnels[idx], client), err)
		return config.Client{}, rollbackPersist(err)
	}
	s.log("info", "client.created", "client created", clientAuditFields(state.Tunnels[idx], client), nil)
	return client, nil
}

func (s *Service) RemoveClient(id string) error {
	if err := s.lockStateMutation(); err != nil {
		return err
	}
	defer s.unlockStateMutation()
	state, err := s.initLocked()
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
			if err := s.saveLocalDesiredState(&state); err != nil {
				return err
			}
			if err := s.renderTunnelLocked(state.Tunnels[ti].ID, true); err != nil {
				if rollbackErr := s.rollbackRuntimeState(previousState, state.Tunnels[ti].ID); rollbackErr != nil {
					return errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr))
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
	if err := s.lockStateMutation(); err != nil {
		return err
	}
	defer s.unlockStateMutation()
	return s.setClientEnabledLocked(id, enabled, nil)
}

var ErrTrafficLimitMarkerUnavailable = errors.New("traffic limit marker unavailable")
var ErrTrafficLimitCheckUnavailable = errors.New("traffic limit check unavailable")
var ErrTrafficLimitReleaseCheckUnavailable = errors.New("traffic limit release check unavailable")

type ClientEnableResult struct {
	Exceeded         *sqldb.ExceededTrafficLimit
	MarkerClearError error
}

// EnableClientWithTrafficLimit checks the current quota and updates desired
// state under one lock, so a simultaneous limit change cannot pass between
// the check and enable operation.
func (s *Service) EnableClientWithTrafficLimit(ctx context.Context, id string) (ClientEnableResult, error) {
	var result ClientEnableResult
	if err := s.lockStateMutation(); err != nil {
		return result, err
	}
	defer s.unlockStateMutation()
	checkCtx, cancel := context.WithTimeout(ctx, s.cfg.DatabaseQueryTimeout)
	exceeded, err := sqldb.ListExceededTrafficLimits(checkCtx, s.cfg, time.Now().UTC())
	cancel()
	if err != nil && !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, sqldb.ErrDisabled) {
		return result, fmt.Errorf("%w: %w", ErrTrafficLimitCheckUnavailable, err)
	}
	for i := range exceeded {
		if exceeded[i].ClientID == id {
			result.Exceeded = &exceeded[i]
			return result, nil
		}
	}
	if err := s.setClientEnabledLocked(id, true, nil); err != nil {
		return result, err
	}
	clearCtx, cancel := context.WithTimeout(ctx, s.cfg.DatabaseQueryTimeout)
	err = sqldb.ClearClientTrafficLimitBlock(clearCtx, s.cfg, id)
	cancel()
	if err != nil && !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, sqldb.ErrDisabled) {
		result.MarkerClearError = err
	}
	return result, nil
}

// UpdateClientTrafficLimit serializes operator limit changes with automatic
// quota disable and release decisions for the same desired state.
func (s *Service) UpdateClientTrafficLimit(ctx context.Context, tunnelID, clientID string, limitBytes *uint64, period sqldb.TrafficLimitPeriod) error {
	if err := s.lockStateMutation(); err != nil {
		return err
	}
	defer s.unlockStateMutation()
	state, err := s.initLocked()
	if err != nil {
		return err
	}
	for _, tunnel := range state.Tunnels {
		if tunnel.ID != tunnelID {
			continue
		}
		for _, client := range tunnel.Clients {
			if client.ID == clientID {
				queryCtx, cancel := context.WithTimeout(ctx, s.cfg.DatabaseQueryTimeout)
				defer cancel()
				return sqldb.SetClientTrafficLimitWithPeriod(queryCtx, s.cfg, tunnelID, clientID, limitBytes, period)
			}
		}
	}
	return errors.New("client not found")
}

// DisableClientManually clears any automatic quota block while holding the
// same state lock used by quota enforcement.
func (s *Service) DisableClientManually(id string, clearQuotaBlock func() error) error {
	if clearQuotaBlock == nil {
		return errors.New("clear quota block callback is required")
	}
	if err := s.lockStateMutation(); err != nil {
		return err
	}
	defer s.unlockStateMutation()
	if err := clearQuotaBlock(); err != nil {
		return fmt.Errorf("%w: %w", ErrTrafficLimitMarkerUnavailable, err)
	}
	return s.setClientEnabledLocked(id, false, nil)
}

type trafficLimitDisable struct {
	TotalBytes uint64
	LimitBytes uint64
	Period     string
}

type TrafficLimitMarker struct {
	Mark  func() (bool, error)
	Clear func() error
}

func (s *Service) DisableClientForTrafficLimit(id string, totalBytes, limitBytes uint64, period string, marker TrafficLimitMarker) (bool, error) {
	if marker.Mark == nil || marker.Clear == nil {
		return false, errors.New("traffic limit marker callbacks are required")
	}
	if err := s.lockStateMutation(); err != nil {
		return false, err
	}
	defer s.unlockStateMutation()
	enabled, err := s.clientEnabledLocked(id)
	if err != nil || !enabled {
		return false, err
	}
	marked, err := marker.Mark()
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrTrafficLimitMarkerUnavailable, err)
	}
	if !marked {
		return false, nil
	}
	if err := s.setClientEnabledLocked(id, false, &trafficLimitDisable{TotalBytes: totalBytes, LimitBytes: limitBytes, Period: period}); err != nil {
		// A failed runtime rollback can leave the desired state disabled. Keep
		// the marker in that case so a later quota release can recover it.
		enabledAfter, inspectErr := s.clientEnabledLocked(id)
		if inspectErr != nil || !enabledAfter {
			return false, errors.Join(err, inspectErr)
		}
		if clearErr := marker.Clear(); clearErr != nil {
			return false, errors.Join(err, fmt.Errorf("clear traffic limit marker after failed disable: %w", clearErr))
		}
		return false, err
	}
	return true, nil
}

type TrafficLimitReleaseMarker struct {
	CanRelease func() (bool, error)
	Clear      func() error
}

func (s *Service) EnableClientForTrafficLimitRelease(id, period string, marker TrafficLimitReleaseMarker) (bool, error) {
	if marker.CanRelease == nil || marker.Clear == nil {
		return false, errors.New("traffic limit release marker callbacks are required")
	}
	if err := s.lockStateMutation(); err != nil {
		return false, err
	}
	defer s.unlockStateMutation()
	canRelease, err := marker.CanRelease()
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrTrafficLimitReleaseCheckUnavailable, err)
	}
	if !canRelease {
		return false, nil
	}
	state, err := s.initLocked()
	if err != nil {
		return false, err
	}
	for _, tunnel := range state.Tunnels {
		for _, client := range tunnel.Clients {
			if client.ID != id {
				continue
			}
			released := false
			if !client.Enabled && !config.ClientExpired(client, time.Now().UTC()) {
				if err := s.setClientEnabledLocked(id, true, &trafficLimitDisable{Period: period}); err != nil {
					return false, err
				}
				released = true
			}
			if err := marker.Clear(); err != nil {
				return released, fmt.Errorf("%w: %w", ErrTrafficLimitMarkerUnavailable, err)
			}
			return released, nil
		}
	}
	return false, errors.New("client not found")
}

func (s *Service) clientEnabledLocked(id string) (bool, error) {
	state, err := s.initLocked()
	if err != nil {
		return false, err
	}
	for _, tunnel := range state.Tunnels {
		for _, client := range tunnel.Clients {
			if client.ID == id {
				return client.Enabled, nil
			}
		}
	}
	return false, errors.New("client not found")
}

func (s *Service) setClientEnabledLocked(id string, enabled bool, trafficLimit *trafficLimitDisable) error {
	state, err := s.initLocked()
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
				if state.Tunnels[ti].Clients[ci].Enabled == enabled {
					return nil
				}
				now := time.Now().UTC()
				state.Tunnels[ti].Clients[ci].Enabled = enabled
				state.Tunnels[ti].Clients[ci].UpdatedAt = now
				state.Tunnels[ti].UpdatedAt = now
				state.UpdatedAt = now
				if err := s.saveLocalDesiredState(&state); err != nil {
					return err
				}
				if err := s.renderTunnelLocked(state.Tunnels[ti].ID, true); err != nil {
					if rollbackErr := s.rollbackRuntimeState(previousState, state.Tunnels[ti].ID); rollbackErr != nil {
						return errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr))
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
				fields := clientAuditFields(state.Tunnels[ti], state.Tunnels[ti].Clients[ci])
				if trafficLimit != nil {
					if enabled {
						event = "client.traffic_limit.released"
						message = "client enabled after traffic limit release"
					} else {
						event = "client.traffic_limit.exceeded"
						message = "client disabled after traffic limit exceeded"
						fields["traffic_total_bytes"] = trafficLimit.TotalBytes
						fields["traffic_limit_bytes"] = trafficLimit.LimitBytes
					}
					fields["traffic_limit_period"] = trafficLimit.Period
				}
				s.log("info", event, message, fields, nil)
				return nil
			}
		}
	}
	return errors.New("client not found")
}

func (s *Service) UpdateClientSettings(id, name, notes string) (config.Client, error) {
	return s.UpdateClientSettingsWithOptions(id, ClientSettingsUpdate{Name: name, Notes: notes})
}

func (s *Service) UpdateClientSettingsWithOptions(id string, update ClientSettingsUpdate) (config.Client, error) {
	if err := s.lockStateMutation(); err != nil {
		return config.Client{}, err
	}
	defer s.unlockStateMutation()
	name := update.Name
	name = strings.TrimSpace(name)
	if !clientNameRE.MatchString(name) {
		return config.Client{}, errors.New("client name must be 1-64 chars and contain only letters, numbers, spaces, dots, underscores, or dashes")
	}
	notes := update.Notes
	notes = strings.TrimSpace(notes)
	if len(notes) > maxClientNotesLength {
		return config.Client{}, fmt.Errorf("client notes must be at most %d bytes", maxClientNotesLength)
	}
	state, err := s.initLocked()
	if err != nil {
		return config.Client{}, err
	}
	previousState, err := cloneState(state)
	if err != nil {
		return config.Client{}, err
	}
	for ti := range state.Tunnels {
		for ci := range state.Tunnels[ti].Clients {
			if state.Tunnels[ti].Clients[ci].ID == id {
				now := time.Now().UTC()
				expirationChanged := !state.Tunnels[ti].Clients[ci].ExpiresAt.Equal(update.ExpiresAt)
				state.Tunnels[ti].Clients[ci].Name = name
				state.Tunnels[ti].Clients[ci].Notes = notes
				state.Tunnels[ti].Clients[ci].ExpiresAt = update.ExpiresAt.UTC()
				state.Tunnels[ti].Clients[ci].UpdatedAt = now
				state.Tunnels[ti].UpdatedAt = now
				state.UpdatedAt = now
				if err := s.saveLocalDesiredState(&state); err != nil {
					return config.Client{}, err
				}
				if expirationChanged {
					if err := s.renderTunnelLocked(state.Tunnels[ti].ID, true); err != nil {
						if rollbackErr := s.rollbackRuntimeState(previousState, state.Tunnels[ti].ID); rollbackErr != nil {
							return config.Client{}, errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr))
						}
						s.log("error", "client.settings.failed", "client settings update failed", clientAuditFields(state.Tunnels[ti], state.Tunnels[ti].Clients[ci]), err)
						return config.Client{}, err
					}
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

type ClientExportContext struct {
	ServerHost   string
	Tunnel       config.Tunnel
	Client       config.Client
	RenderedConf string
}

func (s *Service) ClientExportContext(id string) (ClientExportContext, error) {
	state, err := s.Init()
	if err != nil {
		return ClientExportContext{}, err
	}
	tunnel, client, ok := findClient(state, id)
	if !ok {
		return ClientExportContext{}, errors.New("client not found")
	}
	conf, err := render.ClientConfig(state, tunnel, client)
	if err != nil {
		return ClientExportContext{}, err
	}
	_ = s.markClientConfigDelivered(id)
	s.log("info", "client.config.downloaded", "client config downloaded", clientAuditFields(tunnel, client), nil)
	return ClientExportContext{ServerHost: state.ServerHost, Tunnel: tunnel, Client: client, RenderedConf: conf}, nil
}

func (s *Service) ClientImportKey(id string) (string, config.Client, error) {
	ctx, err := s.ClientExportContext(id)
	if err != nil {
		return "", config.Client{}, err
	}
	key := "vpn://" + base64.RawURLEncoding.EncodeToString([]byte(ctx.RenderedConf))
	s.log("info", "client.import_key.generated", "client import key generated", map[string]any{"client_id": ctx.Client.ID, "client_name": ctx.Client.Name}, nil)
	return key, ctx.Client, nil
}

func (s *Service) EnforceExpiredClients() error {
	if err := s.lockStateMutation(); err != nil {
		return err
	}
	defer s.unlockStateMutation()
	state, err := s.initLocked()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, tunnel := range state.Tunnels {
		hasExpiredActiveClient := false
		for _, client := range tunnel.Clients {
			needsRender := tunnel.LastRenderAt.IsZero() || client.ExpiresAt.After(tunnel.LastRenderAt)
			if client.Enabled && config.ClientExpired(client, now) && needsRender {
				hasExpiredActiveClient = true
				break
			}
		}
		if !hasExpiredActiveClient {
			continue
		}
		if err := s.renderTunnelLocked(tunnel.ID, true); err != nil {
			s.log("error", "client.expiration.enforce_failed", "expired client enforcement failed", tunnelAuditFields(tunnel), err)
			return err
		}
		s.log("info", "client.expiration.enforced", "expired clients removed from rendered tunnel config", tunnelAuditFields(tunnel), nil)
	}
	return nil
}

func (s *Service) markClientConfigDelivered(id string) error {
	if err := s.lockStateMutation(); err != nil {
		return err
	}
	defer s.unlockStateMutation()
	state, err := s.initLocked()
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
