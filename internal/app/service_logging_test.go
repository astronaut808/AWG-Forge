package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeFieldsExcludeClientIdentity(t *testing.T) {
	fields := runtimeFields(map[string]any{
		"tunnel_id":   "tunnel-1",
		"client_id":   "client-1",
		"client_name": "Personal phone",
		"client_ip":   "10.20.0.2",
		"private_key": "do-not-log",
	})

	if fields["tunnel_id"] != "tunnel-1" || fields["client_id"] != "client-1" {
		t.Fatalf("missing operational fields: %#v", fields)
	}
	for _, key := range []string{"client_name", "client_ip", "private_key"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("runtime fields expose %s: %#v", key, fields)
		}
	}
}

func TestRunAWGQuickDoesNotReturnCommandOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "awg-quick")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho 'PrivateKey = do-not-log' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	err := runAWGQuick("up", "awg20")
	if err == nil {
		t.Fatal("expected command failure")
	}
	if strings.Contains(err.Error(), "do-not-log") || strings.Contains(err.Error(), "PrivateKey") {
		t.Fatalf("command output leaked through error: %v", err)
	}
}
