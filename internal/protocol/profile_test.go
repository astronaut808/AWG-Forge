package protocol

import (
	"maps"
	"reflect"
	"testing"
)

func TestProfileRegistry(t *testing.T) {
	want := []struct {
		id      string
		name    string
		version string
	}{
		{id: "awg_legacy_1_0", name: "AmneziaWG Legacy / 1.0", version: "1.0"},
		{id: "awg_1_5", name: "AmneziaWG 1.5", version: "1.5"},
		{id: "awg_2_0", name: "AmneziaWG 2.0", version: "2"},
		{id: "awg_3", name: "AmneziaWG 3.x", version: "3.x"},
	}

	profiles := All()
	if len(profiles) != len(want) {
		t.Fatalf("profiles = %d, want %d", len(profiles), len(want))
	}
	seen := make(map[string]struct{}, len(profiles))
	for idx, profile := range profiles {
		if profile.ID() != want[idx].id || profile.DisplayName() != want[idx].name || profile.Version() != want[idx].version {
			t.Errorf("profile %d = %q/%q/%q, want %q/%q/%q", idx, profile.ID(), profile.DisplayName(), profile.Version(), want[idx].id, want[idx].name, want[idx].version)
		}
		if _, duplicate := seen[profile.ID()]; duplicate {
			t.Errorf("duplicate profile ID %q", profile.ID())
		}
		seen[profile.ID()] = struct{}{}
		registered, ok := ByID(profile.ID())
		if !ok || reflect.TypeOf(registered) != reflect.TypeOf(profile) {
			t.Errorf("ByID(%q) = %#v, %t", profile.ID(), registered, ok)
		}
		assertProfileParameterKeys(t, profile)
	}
	if _, ok := ByID("unknown"); ok {
		t.Fatal("unknown profile is registered")
	}
}

func TestProfileRegistryReturnsDefensiveCopies(t *testing.T) {
	profiles := All()
	profiles[0] = AWG3{}
	if got := All()[0].ID(); got != "awg_legacy_1_0" {
		t.Fatalf("registry changed through All result: first profile = %q", got)
	}

	profile := All()[0]
	keys := profile.ParameterKeys()
	keys[0] = "changed"
	if got := profile.ParameterKeys()[0]; got == "changed" {
		t.Fatal("profile parameter keys were mutated through returned slice")
	}
}

func assertProfileParameterKeys(t *testing.T, profile ProtocolProfile) {
	t.Helper()
	params, err := profile.GenerateDefaults()
	if err != nil {
		t.Fatalf("generate defaults for %q: %v", profile.ID(), err)
	}
	keys := profile.ParameterKeys()
	wantKeys := make(map[string]bool, len(params))
	for key := range params {
		wantKeys[key] = true
	}
	gotKeys := make(map[string]bool, len(keys))
	for _, key := range keys {
		if key == "HeaderProtectionKey" {
			t.Errorf("profile %q exposes HeaderProtectionKey as a public parameter", profile.ID())
		}
		if gotKeys[key] {
			t.Errorf("profile %q contains duplicate parameter key %q", profile.ID(), key)
		}
		gotKeys[key] = true
	}
	if !maps.Equal(gotKeys, wantKeys) {
		t.Errorf("profile %q parameter keys = %v, default keys = %v", profile.ID(), gotKeys, wantKeys)
	}
}
