package controlapi

import (
	"encoding/json"
	"errors"
	"math"
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	MaxSnapshotBytes          = 512 << 10
	MaxSnapshotTunnels        = 64
	MaxSnapshotClients        = 2048
	MaxProjectedNodes         = 1024
	MaxProjectionStorageBytes = 16 << 20
)

// Snapshot is an allowlist, never a config.State or an ordinary local API
// presenter. No keys, protocol parameters, notes, endpoints or raw diagnostics
// cross this boundary. SessionID is a fence, not an authentication credential.
type Snapshot struct {
	ContractVersion   int               `json:"contract_version"`
	SessionID         string            `json:"session_id"`
	StateEpoch        string            `json:"state_epoch"`
	BindingEpoch      uint64            `json:"binding_epoch"`
	BootID            string            `json:"boot_id"`
	BootSequence      uint64            `json:"boot_sequence"`
	DesiredGeneration uint64            `json:"desired_generation"`
	Sequence          uint64            `json:"snapshot_sequence"`
	ObservedAt        time.Time         `json:"observed_at"`
	Desired           DesiredProjection `json:"desired"`
	Observations      Observations      `json:"observations"`
}

type DesiredProjection struct {
	Tunnels []SnapshotTunnel `json:"tunnels"`
}
type SnapshotTunnel struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Interface  string           `json:"interface"`
	Profile    string           `json:"profile"`
	Enabled    bool             `json:"enabled"`
	ListenPort int              `json:"listen_port"`
	Revision   int              `json:"revision"`
	Clients    []SnapshotClient `json:"clients"`
}
type SnapshotClient struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Address string `json:"address"`
}
type Observations struct {
	ApplyEnabled     bool                `json:"apply_enabled"`
	HistoryAvailable bool                `json:"history_available"`
	Tunnels          []TunnelObservation `json:"tunnels"`
	Doctor           DoctorSummary       `json:"doctor"`
}
type TunnelObservation struct {
	ID          string              `json:"id"`
	Known       bool                `json:"known"`
	Up          bool                `json:"up"`
	ApplyFailed bool                `json:"apply_failed"`
	Clients     []ClientObservation `json:"clients"`
}
type ClientObservation struct {
	ID            string    `json:"id"`
	Present       bool      `json:"present"`
	LastHandshake time.Time `json:"last_handshake"`
	RxBytes       uint64    `json:"rx_bytes"`
	TxBytes       uint64    `json:"tx_bytes"`
}

// These bounded checks are a runtime Doctor summary, not the full local Doctor.
type DoctorSummary struct {
	Scope          string `json:"scope"`
	TUNAvailable   bool   `json:"tun_available"`
	Forwarding     bool   `json:"forwarding"`
	RuntimeUnknown int    `json:"runtime_unknown"`
	TunnelsDown    int    `json:"tunnels_down"`
	ApplyFailures  int    `json:"apply_failures"`
}
type SnapshotAccepted struct {
	Sequence   uint64    `json:"snapshot_sequence"`
	ReceivedAt time.Time `json:"received_at"`
}

// NodeView deliberately omits session and certificate material. Reachability
// and projection freshness are independently computed using controller time.
type NodeView struct {
	NodeID             string     `json:"node_id"`
	Name               string     `json:"name"`
	BindingEpoch       uint64     `json:"binding_epoch"`
	Status             string     `json:"status"`
	StateEpoch         string     `json:"state_epoch"`
	BootID             string     `json:"boot_id"`
	LastConfirmedAt    *time.Time `json:"last_confirmed_at"`
	ApplicationVersion string     `json:"application_version"`
	ContractVersion    int        `json:"contract_version"`
	Capabilities       []string   `json:"capabilities"`
}
type NodeInventory struct {
	ControllerID string     `json:"controller_id"`
	Nodes        []NodeView `json:"nodes"`
}
type NodeProjection struct {
	ControllerID      string             `json:"controller_id"`
	Node              NodeView           `json:"node"`
	StateEpoch        string             `json:"state_epoch"`
	BootID            string             `json:"boot_id"`
	DesiredGeneration uint64             `json:"desired_generation"`
	Sequence          uint64             `json:"snapshot_sequence"`
	ObservedAt        *time.Time         `json:"observed_at"`
	ReceivedAt        *time.Time         `json:"received_at"`
	Stale             bool               `json:"stale"`
	Available         bool               `json:"available"`
	Desired           *DesiredProjection `json:"desired"`
	Observations      *Observations      `json:"observations"`
}

var resourceIDRE = regexp.MustCompile(`^[a-f0-9]{16}$`)
var interfaceRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{0,31}$`)
var capabilityRE = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
var profileRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func validUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value
}
func safeLabel(value string) bool {
	if len(value) == 0 || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, c := range value {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

// Validate checks bounds and reference completeness before any storage. Unknown
// JSON fields are separately rejected by the transport decoder.
func (s Snapshot) Validate() error {
	bad := errors.New("invalid snapshot")
	if s.ContractVersion != 1 || !validUUID(s.SessionID) || !validUUID(s.StateEpoch) || !validUUID(s.BootID) || s.BindingEpoch == 0 || s.BindingEpoch > math.MaxInt64 || s.BootSequence == 0 || s.BootSequence > math.MaxInt64 || s.Sequence == 0 || s.Sequence > math.MaxInt64 || s.DesiredGeneration > math.MaxInt64 || s.ObservedAt.IsZero() {
		return bad
	}
	if s.Desired.Tunnels == nil || s.Observations.Tunnels == nil || len(s.Desired.Tunnels) > MaxSnapshotTunnels || len(s.Desired.Tunnels) != len(s.Observations.Tunnels) {
		return bad
	}
	d := s.Observations.Doctor
	if d.Scope != "runtime" || d.RuntimeUnknown < 0 || d.TunnelsDown < 0 || d.ApplyFailures < 0 || d.RuntimeUnknown > MaxSnapshotTunnels || d.TunnelsDown > MaxSnapshotTunnels || d.ApplyFailures > MaxSnapshotTunnels {
		return bad
	}
	ids := map[string]bool{}
	clients := 0
	for i, t := range s.Desired.Tunnels {
		o := s.Observations.Tunnels[i]
		if !resourceIDRE.MatchString(t.ID) || ids[t.ID] || !safeLabel(t.Name) || !interfaceRE.MatchString(t.Interface) || strings.Contains(t.Interface, "..") || !profileRE.MatchString(t.Profile) || t.ListenPort < 1 || t.ListenPort > 65535 || t.Revision < 0 || t.Revision > math.MaxInt32 || t.Clients == nil || o.Clients == nil || o.ID != t.ID || len(t.Clients) != len(o.Clients) {
			return bad
		}
		ids[t.ID] = true
		clients += len(t.Clients)
		if clients > MaxSnapshotClients {
			return bad
		}
		for j, c := range t.Clients {
			co := o.Clients[j]
			ip, err := netip.ParseAddr(c.Address)
			if !resourceIDRE.MatchString(c.ID) || ids[c.ID] || !safeLabel(c.Name) || err != nil || !ip.Is4() || co.ID != c.ID || !co.LastHandshake.IsZero() && co.LastHandshake.After(s.ObservedAt.Add(time.Minute)) {
				return bad
			}
			ids[c.ID] = true
		}
	}
	encoded, err := json.Marshal(s)
	if err != nil || len(encoded) > MaxSnapshotBytes {
		return bad
	}
	return nil
}

// ValidObservationMetadata bounds the public presence metadata at persistence
// and archive boundaries as well as transport admission.
func ValidObservationMetadata(version string, contract int, capabilities []string) bool {
	if !safeLabel(version) || len(version) > 64 || contract < 1 || contract > 255 || len(capabilities) < 1 || len(capabilities) > 128 {
		return false
	}
	seen := map[string]bool{}
	for _, c := range capabilities {
		if !capabilityRE.MatchString(c) || seen[c] {
			return false
		}
		seen[c] = true
	}
	return true
}
