package controlapi

import (
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
)

func validSnapshot() Snapshot {
	now := time.Now().UTC()
	return Snapshot{
		ContractVersion: 1, SessionID: uuid.NewString(), StateEpoch: uuid.NewString(), BindingEpoch: 1,
		BootID: uuid.NewString(), BootSequence: 1, DesiredGeneration: 1, Sequence: 1, ObservedAt: now,
		Desired:      DesiredProjection{Tunnels: []SnapshotTunnel{{ID: "0123456789abcdef", Name: "узел.example", Interface: "awg0.1", Profile: "awg_2_0", Enabled: true, ListenPort: 51820, Clients: []SnapshotClient{{ID: "fedcba9876543210", Name: "телефон", Enabled: true, Address: "10.20.0.2"}}}}},
		Observations: Observations{Tunnels: []TunnelObservation{{ID: "0123456789abcdef", Clients: []ClientObservation{{ID: "fedcba9876543210"}}}}, Doctor: DoctorSummary{Scope: "runtime"}},
	}
}

func TestSnapshotValidateAllowlistLabelsAndBounds(t *testing.T) {
	if err := validSnapshot().Validate(); err != nil {
		t.Fatalf("valid dotted interface and UTF-8 labels rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Snapshot){
		"path-like interface":               func(s *Snapshot) { s.Desired.Tunnels[0].Interface = "awg..0" },
		"invalid utf8 label":                func(s *Snapshot) { s.Desired.Tunnels[0].Name = string([]byte{0xff}) },
		"unallowlisted desired field value": func(s *Snapshot) { s.Desired.Tunnels[0].Profile = "AWG" },
		"sequence exceeds sqlite range":     func(s *Snapshot) { s.Sequence = math.MaxInt64 + 1 },
		"too many doctor failures":          func(s *Snapshot) { s.Observations.Doctor.ApplyFailures = MaxSnapshotTunnels + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			s := validSnapshot()
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
}

func TestValidObservationMetadataBounds(t *testing.T) {
	if !ValidObservationMetadata("v1.2.3", 1, []string{"presence", "snapshot.v1"}) {
		t.Fatal("valid metadata rejected")
	}
	for _, caps := range [][]string{{"snapshot.v1", "snapshot.v1"}, {"Snapshot.v1"}, nil} {
		if ValidObservationMetadata("v1", 1, caps) {
			t.Fatalf("invalid capabilities accepted: %#v", caps)
		}
	}
}
