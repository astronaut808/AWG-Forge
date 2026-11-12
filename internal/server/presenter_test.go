package server

import (
	"reflect"
	"sort"
	"testing"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/protocol"
)

func TestOrderedParamsUsesProfileParameterKeys(t *testing.T) {
	for _, profile := range protocol.All() {
		t.Run(profile.ID(), func(t *testing.T) {
			params := make(config.ProtocolParams, len(profile.ParameterKeys())+1)
			for _, key := range profile.ParameterKeys() {
				params[key] = key + "-value"
			}
			params["Unknown"] = "must not be exposed"

			got := orderedParams(profile.ID(), params)
			gotKeys := make([]string, 0, len(got))
			for _, param := range got {
				gotKeys = append(gotKeys, param["key"])
				if param["value"] != param["key"]+"-value" {
					t.Errorf("parameter %q value = %q", param["key"], param["value"])
				}
			}
			wantKeys := profile.ParameterKeys()
			sort.Strings(wantKeys)
			if !reflect.DeepEqual(gotKeys, wantKeys) {
				t.Errorf("ordered keys = %v, want %v", gotKeys, wantKeys)
			}
		})
	}

	if got := orderedParams("unknown", config.ProtocolParams{"Jc": "4"}); got != nil {
		t.Fatalf("unknown profile params = %v, want nil", got)
	}
}
