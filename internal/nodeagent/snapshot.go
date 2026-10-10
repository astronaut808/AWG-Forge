package nodeagent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
)

const snapshotCollectionTimeout = 5 * time.Second
const maxRuntimeObservationBytes = 256 << 10

type cappedOutput struct{ bytes.Buffer }

func (w *cappedOutput) Write(p []byte) (int, error) {
	if w.Len()+len(p) > maxRuntimeObservationBytes {
		return 0, errors.New("runtime observation limit")
	}
	return w.Buffer.Write(p)
}

// Only public peer IDs, handshake timestamps and counters are read. Dump/config
// commands are deliberately excluded; stdout/stderr are never logged.
func observationCommand(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "awg", args...)
	cmd.WaitDelay = time.Second
	var output cappedOutput
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if cmd.Run() != nil {
		return nil, errors.New("runtime observation unavailable")
	}
	return output.Bytes(), nil
}

func collectSnapshot(ctx context.Context, service *app.Service, cfg config.Config, session string, sequence uint64) (controlapi.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, snapshotCollectionTimeout)
	defer cancel()
	state, err := service.NodeObservationState(ctx)
	if err != nil {
		return controlapi.Snapshot{}, err
	}
	boot, err := service.BootID()
	if err != nil {
		return controlapi.Snapshot{}, err
	}
	if len(state.Tunnels) > controlapi.MaxSnapshotTunnels {
		return controlapi.Snapshot{}, errors.New("snapshot resource limit")
	}
	totalClients := 0
	for _, t := range state.Tunnels {
		totalClients += len(t.Clients)
		if totalClients > controlapi.MaxSnapshotClients {
			return controlapi.Snapshot{}, errors.New("snapshot resource limit")
		}
	}
	m := state.ManagedNode
	s := controlapi.Snapshot{ContractVersion: 1, SessionID: session, StateEpoch: m.StateEpoch, BindingEpoch: m.BindingEpoch, BootID: boot, BootSequence: m.BootSequence, DesiredGeneration: m.DesiredGeneration, Sequence: sequence, ObservedAt: time.Now().UTC(), Desired: controlapi.DesiredProjection{Tunnels: []controlapi.SnapshotTunnel{}}, Observations: controlapi.Observations{ApplyEnabled: cfg.ApplyConfig, HistoryAvailable: cfg.DatabaseMode == "sqlite", Tunnels: []controlapi.TunnelObservation{}, Doctor: controlapi.DoctorSummary{Scope: "runtime"}}}
	// Validate the desired allowlist and total resource bounds before commands.
	for _, t := range state.Tunnels {
		dt := controlapi.SnapshotTunnel{ID: t.ID, Name: t.Name, Interface: t.InterfaceName, Profile: t.ProtocolProfileID, Enabled: t.Enabled, ListenPort: t.ListenPort, Revision: t.ConfigRevision, Clients: []controlapi.SnapshotClient{}}
		o := controlapi.TunnelObservation{ID: t.ID, Clients: []controlapi.ClientObservation{}}
		for _, c := range t.Clients {
			dt.Clients = append(dt.Clients, controlapi.SnapshotClient{ID: c.ID, Name: c.Name, Enabled: c.Enabled, Address: c.IPv4Address})
			o.Clients = append(o.Clients, controlapi.ClientObservation{ID: c.ID})
		}
		s.Desired.Tunnels = append(s.Desired.Tunnels, dt)
		s.Observations.Tunnels = append(s.Observations.Tunnels, o)
	}
	if err := s.Validate(); err != nil {
		return controlapi.Snapshot{}, err
	}
	if _, err := os.Stat("/dev/net/tun"); err == nil {
		s.Observations.Doctor.TUNAvailable = true
	}
	if b, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward"); err == nil {
		s.Observations.Doctor.Forwarding = strings.TrimSpace(string(b)) == "1"
	}
	for i, t := range state.Tunnels {
		if err := ctx.Err(); err != nil {
			return controlapi.Snapshot{}, errors.New("snapshot collection timeout")
		}
		o := &s.Observations.Tunnels[i]
		o.ApplyFailed = t.LastApplyError != ""
		if o.ApplyFailed {
			s.Observations.Doctor.ApplyFailures++
		}
		handshakes, hErr := observationCommand(ctx, "show", t.InterfaceName, "latest-handshakes")
		transfers, tErr := observationCommand(ctx, "show", t.InterfaceName, "transfer")
		o.Known = hErr == nil && tErr == nil
		// A missing interface is distinguished from an installed runtime with
		// unavailable observations; neither is reported as healthy.
		link, linkErr := net.InterfaceByName(t.InterfaceName)
		o.Up = linkErr == nil && link.Flags&net.FlagUp != 0
		if !o.Known {
			s.Observations.Doctor.RuntimeUnknown++
		}
		if t.Enabled && !o.Up {
			s.Observations.Doctor.TunnelsDown++
		}
		if !o.Known {
			continue
		}
		type counters struct {
			handshake int64
			rx, tx    uint64
			present   bool
		}
		peers := map[string]counters{}
		for line := range strings.SplitSeq(string(handshakes), "\n") {
			f := strings.Fields(line)
			if len(f) != 2 {
				continue
			}
			stamp, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil || stamp < 0 {
				continue
			}
			peers[f[0]] = counters{handshake: stamp, present: true}
		}
		for line := range strings.SplitSeq(string(transfers), "\n") {
			f := strings.Fields(line)
			if len(f) != 3 {
				continue
			}
			rx, e1 := strconv.ParseUint(f[1], 10, 64)
			tx, e2 := strconv.ParseUint(f[2], 10, 64)
			if e1 != nil || e2 != nil {
				continue
			}
			p := peers[f[0]]
			p.rx, p.tx = rx, tx
			peers[f[0]] = p
		}
		for j, c := range t.Clients {
			p := peers[c.PublicKey]
			co := &o.Clients[j]
			co.Present, co.RxBytes, co.TxBytes = p.present, p.rx, p.tx
			if p.handshake > 0 {
				co.LastHandshake = time.Unix(p.handshake, 0).UTC()
			}
		}
	}
	if ctx.Err() != nil {
		return controlapi.Snapshot{}, errors.New("snapshot collection timeout")
	}
	s.ObservedAt = time.Now().UTC()
	if err := s.Validate(); err != nil {
		return controlapi.Snapshot{}, err
	}
	return s, nil
}
