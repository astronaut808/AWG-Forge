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

func TestRunAWGQuickReturnsSanitizedFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "awg-quick")
	key := strings.Repeat("A", 43) + "="
	script := "#!/bin/sh\necho 'PrivateKey = do-not-log' >&2\necho 'HeaderProtectionKey = " + key + "' >&2\necho 'RTNETLINK answers: Operation not permitted' >&2\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	err := runAWGQuick("up", "awg20")
	if err == nil {
		t.Fatal("expected command failure")
	}
	if strings.Contains(err.Error(), "do-not-log") || strings.Contains(err.Error(), "PrivateKey") || strings.Contains(err.Error(), key) {
		t.Fatalf("command output leaked through error: %v", err)
	}
	if !strings.Contains(err.Error(), "RTNETLINK answers: Operation not permitted") {
		t.Fatalf("safe command failure detail was omitted: %v", err)
	}
}

func TestRunAWGQuickUserspaceSelection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "awg-quick")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s' \"${AWG_QUICK_FORCE_USERSPACE:-}\" >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	err := runAWGQuickForProfile("awg_3_0", "up", "awg30")
	if err == nil || !strings.Contains(err.Error(), "1") {
		t.Fatalf("AWG3 did not force userspace mode: %v", err)
	}
	err = runAWGQuickForProfile("awg_2_0", "up", "awg20")
	if err == nil || strings.Contains(err.Error(), ": 1") {
		t.Fatalf("AWG2 unexpectedly forced userspace mode: %v", err)
	}
	err = runAWGQuickWithUserspace(true, "up", "warp0")
	if err == nil || !strings.Contains(err.Error(), "1") {
		t.Fatalf("WARP in the AWG3 image did not force userspace mode: %v", err)
	}
}
