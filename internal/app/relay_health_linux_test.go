package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRelayTimeoutAfterPeerRemoval(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	script := filepath.Join(bin, "awg")
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{"peer present", "printf '%s\\n' probe-peer", "unreachable"},
		{"peer removed", "printf '%s\\n' client-peer", relayProbeInterrupted},
		{"interface removed", "exit 1", relayProbeInterrupted},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(script, []byte("#!/bin/sh\n"+tt.body+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if got := relayTimeoutStatus(t.Context(), "awg0", "probe-peer"); got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
		})
	}
}
