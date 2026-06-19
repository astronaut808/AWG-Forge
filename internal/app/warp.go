package app

import (
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/warp"
)

type WarpSummary struct {
	Configured          bool      `json:"configured"`
	InterfaceName       string    `json:"interface_name"`
	Endpoint            string    `json:"endpoint,omitempty"`
	AddressV4           string    `json:"address_v4,omitempty"`
	MTU                 int       `json:"mtu,omitempty"`
	PersistentKeepalive int       `json:"persistent_keepalive,omitempty"`
	EnabledTunnelCount  int       `json:"enabled_tunnel_count"`
	LastApplyAt         time.Time `json:"last_apply_at,omitempty"`
	LastApplyError      string    `json:"last_apply_error,omitempty"`
}

type WarpRuntimeStatus struct {
	Up bool `json:"up"`
}

func (s *Service) ImportWarpConfig(text string) (config.Warp, error) {
	parsed, err := warp.ParseWireGuardConfig(text)
	if err != nil {
		return config.Warp{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.initLocked()
	if err != nil {
		return config.Warp{}, err
	}
	previous, err := cloneState(state)
	if err != nil {
		return config.Warp{}, err
	}
	now := time.Now().UTC()
	parsed.UpdatedAt = now
	state.Warp = parsed
	state.UpdatedAt = now
	if err := s.store.Save(state); err != nil {
		return config.Warp{}, err
	}
	if s.cfg.ApplyConfig {
		if err := s.reconcileWarpRuntime(state); err != nil {
			if rollbackErr := s.store.Save(previous); rollbackErr != nil {
				return config.Warp{}, errors.Join(err, rollbackErr)
			}
			if rollbackErr := s.reconcileWarpRuntime(previous); rollbackErr != nil {
				return config.Warp{}, errors.Join(err, rollbackErr)
			}
			s.log("error", "warp.import.failed", "WARP config import failed", warpAuditFields(parsed, state), err)
			return config.Warp{}, &ApplyError{Err: err}
		}
		state.Warp.LastApplyAt = now
		state.Warp.LastApplyError = ""
		state.UpdatedAt = now
		if err := s.store.Save(state); err != nil {
			return config.Warp{}, err
		}
	}
	s.log("info", "warp.imported", "WARP config imported", warpAuditFields(parsed, state), nil)
	return state.Warp, nil
}

func (s *Service) DeleteWarpConfig() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.initLocked()
	if err != nil {
		return err
	}
	for _, tunnel := range state.Tunnels {
		if tunnel.EgressMode == config.EgressWarp {
			return errors.New("cannot delete WARP config while tunnels use WARP egress")
		}
	}
	interfaceName := state.Warp.RuntimeInterface()
	state.Warp = config.Warp{InterfaceName: "warp0", MTU: 1280, PersistentKeepalive: 25}
	state.UpdatedAt = time.Now().UTC()
	if err := s.store.Save(state); err != nil {
		return err
	}
	if s.cfg.ApplyConfig {
		_ = exec.Command("awg-quick", "down", interfaceName).Run()
	}
	s.log("info", "warp.deleted", "WARP config deleted", map[string]any{"interface": interfaceName}, nil)
	return nil
}

func (s *Service) RestartWarp() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.initLocked()
	if err != nil {
		return err
	}
	if !s.cfg.ApplyConfig {
		return errors.New("APPLY_CONFIG=false; WARP restart skipped")
	}
	if err := s.reconcileWarpRuntime(state); err != nil {
		state.Warp.LastApplyError = err.Error()
		state.UpdatedAt = time.Now().UTC()
		_ = s.store.Save(state)
		s.log("error", "warp.restart.failed", "WARP restart failed", warpAuditFields(state.Warp, state), err)
		return &ApplyError{Err: err}
	}
	state.Warp.LastApplyAt = time.Now().UTC()
	state.Warp.LastApplyError = ""
	state.UpdatedAt = state.Warp.LastApplyAt
	if err := s.store.Save(state); err != nil {
		return err
	}
	s.log("info", "warp.restarted", "WARP restarted", warpAuditFields(state.Warp, state), nil)
	return nil
}

func (s *Service) WarpSummary(state config.State) WarpSummary {
	return WarpSummary{
		Configured:          state.Warp.Configured(),
		InterfaceName:       state.Warp.RuntimeInterface(),
		Endpoint:            state.Warp.Endpoint,
		AddressV4:           state.Warp.AddressV4,
		MTU:                 state.Warp.MTU,
		PersistentKeepalive: state.Warp.PersistentKeepalive,
		EnabledTunnelCount:  warp.EnabledTunnelCount(state),
		LastApplyAt:         state.Warp.LastApplyAt,
		LastApplyError:      state.Warp.LastApplyError,
	}
}

func (s *Service) WarpRuntimeStatus(state config.State) WarpRuntimeStatus {
	interfaceName := state.Warp.RuntimeInterface()
	return WarpRuntimeStatus{Up: exec.Command("ip", "link", "show", interfaceName).Run() == nil}
}

func warpAuditFields(w config.Warp, state config.State) map[string]any {
	return map[string]any{
		"configured":           w.Configured(),
		"interface":            w.RuntimeInterface(),
		"endpoint":             w.Endpoint,
		"address_v4":           w.AddressV4,
		"enabled_tunnel_count": warp.EnabledTunnelCount(state),
		"private_key_set":      strings.TrimSpace(w.PrivateKey) != "",
		"preshared_key_set":    strings.TrimSpace(w.PresharedKey) != "",
	}
}
