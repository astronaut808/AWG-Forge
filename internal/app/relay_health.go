package app

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
)

const (
	relayCheckInterval    = time.Minute
	relayCheckWorkers     = 4
	relayProbeTimeout     = 10 * time.Second
	relayProbeCleanup     = 4 * time.Second
	relayProbeInterrupted = ""
)

type RelayHealth struct {
	Status      string     `json:"status"`
	CheckedAt   *time.Time `json:"checked_at,omitempty"`
	SucceededAt *time.Time `json:"succeeded_at,omitempty"`
}

type relayCheckTarget struct {
	relay    config.Relay
	tunnelID string
	port     int
	revision int
}

type relayChecks struct {
	mu      sync.Mutex
	results map[relayCheckTarget]RelayHealth
}

func relayTarget(tunnel config.Tunnel, relay config.Relay) relayCheckTarget {
	// A rename does not change the forwarding path.
	relay.Name = ""
	return relayCheckTarget{relay: relay, tunnelID: tunnel.ID, port: tunnel.ListenPort, revision: tunnel.ConfigRevision}
}

func (s *Service) RelayHealthByTunnel(tunnelID string) (map[string]RelayHealth, error) {
	state, err := s.State()
	if err != nil {
		return nil, err
	}
	index, ok := tunnelIndexByID(state, tunnelID)
	if !ok {
		return nil, os.ErrNotExist
	}
	tunnel := state.Tunnels[index]
	var active int
	for _, candidate := range state.Tunnels {
		if candidate.Enabled {
			active += len(candidate.Relays)
		}
	}
	health := make(map[string]RelayHealth, len(tunnel.Relays))
	s.relayChecks.mu.Lock()
	defer s.relayChecks.mu.Unlock()
	for _, relay := range tunnel.Relays {
		item := s.relayChecks.results[relayTarget(tunnel, relay)]
		switch {
		case !tunnel.Enabled || !s.cfg.ApplyConfig:
			item.Status = "unavailable"
		case item.CheckedAt == nil:
			item.Status = "pending"
		case time.Since(*item.CheckedAt) > relayStaleAfter(active):
			item.Status = "stale"
		}
		health[relay.ID] = item
	}
	return health, nil
}

func relayStaleAfter(active int) time.Duration {
	batches := (active + relayCheckWorkers - 1) / relayCheckWorkers
	sweep := time.Duration(batches) * (relayProbeTimeout + relayProbeCleanup)
	return 3 * max(relayCheckInterval, sweep)
}

// RunRelayChecks checks configured relays even when the web UI is closed.
func (s *Service) RunRelayChecks(ctx context.Context) {
	if !s.cfg.ApplyConfig {
		return
	}
	ticker := time.NewTicker(relayCheckInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		s.checkRelays(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) checkRelays(ctx context.Context) {
	state, err := s.State()
	if err != nil {
		return
	}
	active := make(map[relayCheckTarget]bool)
	var probes []func()
	for _, tunnel := range state.Tunnels {
		for _, relay := range tunnel.Relays {
			if !tunnel.Enabled {
				continue
			}
			target := relayTarget(tunnel, relay)
			active[target] = true
			probes = append(probes, func() {
				status := checkRelayHandshake(ctx, tunnel, relay)
				if ctx.Err() == nil {
					s.relayChecks.record(target, status)
				}
			})
		}
	}
	runRelayProbes(ctx, probes)
	s.relayChecks.mu.Lock()
	defer s.relayChecks.mu.Unlock()
	for target := range s.relayChecks.results {
		if !active[target] {
			delete(s.relayChecks.results, target)
		}
	}
}

func runRelayProbes(ctx context.Context, probes []func()) {
	var workers sync.WaitGroup
	for offset := range min(relayCheckWorkers, len(probes)) {
		workers.Go(func() {
			for i := offset; i < len(probes) && ctx.Err() == nil; i += relayCheckWorkers {
				probes[i]()
			}
		})
	}
	workers.Wait()
}

func (checks *relayChecks) record(target relayCheckTarget, status string) {
	if status == relayProbeInterrupted {
		return
	}
	now := time.Now().UTC()
	checks.mu.Lock()
	defer checks.mu.Unlock()
	if checks.results == nil {
		checks.results = make(map[relayCheckTarget]RelayHealth)
	}
	item := checks.results[target]
	item.Status, item.CheckedAt = status, &now
	if status == "reachable" {
		item.SucceededAt = &now
	}
	checks.results[target] = item
}
