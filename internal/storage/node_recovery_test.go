package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func recoveryJournalFixture() NodeRecoveryJournal {
	return NodeRecoveryJournal{Version: 1, OldStateHash: strings.Repeat("a", 64), NewStateHash: strings.Repeat("b", 64), OldGeneration: strings.Repeat("a", 32), NewGeneration: strings.Repeat("b", 32)}
}

func TestNodeRecoveryPublicationAbruptExit(t *testing.T) {
	if point := os.Getenv("AWG_TEST_RECOVERY_DEATH_POINT"); point != "" {
		s := New(os.Getenv("AWG_TEST_RECOVERY_DEATH_DIR"))
		_ = s.SaveNodeRecoveryJournal(recoveryJournalFixture(), func(p string) error {
			if p == point {
				os.Exit(79) // Deliberately skips all deferred cleanup.
			}
			return nil
		})
		os.Exit(80)
	}
	for _, point := range []string{"journal-published", "journal-durable"} {
		t.Run(point, func(t *testing.T) {
			s := privateRenewalStore(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			source, err := os.Open(executable)
			if err != nil {
				t.Fatal(err)
			}
			helperDir := t.TempDir()
			target, err := os.OpenFile(filepath.Join(helperDir, "recovery-publication-helper"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
			if err != nil {
				_ = source.Close()
				t.Fatal(err)
			}
			_, copyErr := io.Copy(target, source)
			if err := errors.Join(copyErr, target.Close(), source.Close()); err != nil {
				t.Fatal(err)
			}
			child := exec.CommandContext(ctx, "./recovery-publication-helper", "-test.run=^TestNodeRecoveryPublicationAbruptExit$")
			child.Dir = helperDir
			child.Env = append(os.Environ(), "AWG_TEST_RECOVERY_DEATH_POINT="+point, "AWG_TEST_RECOVERY_DEATH_DIR="+s.dir)
			var exit *exec.ExitError
			if err := child.Run(); !errors.As(err, &exit) || exit.ExitCode() != 79 {
				t.Fatal("child did not exit at publication boundary")
			}
			got, err := s.LoadNodeRecoveryJournal()
			if err != nil || got != recoveryJournalFixture() {
				t.Fatal("abrupt death made complete journal unreadable")
			}
			entries, err := os.ReadDir(s.dir)
			if err != nil || len(entries) != 1 || entries[0].Name() != NodeRecoveryJournalFileName {
				t.Fatal("publication retained temporary alias")
			}
		})
	}
}

func TestNodeRecoveryAtomicJournalAndNoReplacement(t *testing.T) {
	for _, point := range []string{"journal-created", "journal-written", "journal-synced", "journal-published", "journal-durable"} {
		t.Run(point, func(t *testing.T) {
			s := privateRenewalStore(t)
			dir := s.dir
			j := recoveryJournalFixture()
			injected := errors.New("publication fault")
			if err := s.SaveNodeRecoveryJournal(j, func(p string) error {
				if p == point {
					return injected
				}
				return nil
			}); !errors.Is(err, injected) {
				t.Fatal("fault boundary missed")
			}
			got, err := s.LoadNodeRecoveryJournal()
			if point == "journal-published" || point == "journal-durable" {
				if err != nil || got != j {
					t.Fatal("published journal incomplete")
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal("partial journal became visible")
			}
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".tmp") {
					t.Fatal("exact temp cleanup missed")
				}
			}
		})
	}
	for _, kind := range []string{"malformed", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			s := privateRenewalStore(t)
			path := s.NodeRecoveryJournalPath()
			switch kind {
			case "malformed":
				_ = os.WriteFile(path, []byte("retain-evidence"), 0600)
			case "symlink":
				_ = os.Symlink("missing-target", path)
			case "directory":
				_ = os.Mkdir(path, 0700)
			}
			if err := s.SaveNodeRecoveryJournal(recoveryJournalFixture(), nil); err == nil {
				t.Fatal("existing evidence replaced")
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal("existing evidence removed")
			}
			if kind == "malformed" {
				body, _ := os.ReadFile(path)
				if !bytes.Equal(body, []byte("retain-evidence")) {
					t.Fatal("malformed evidence overwritten")
				}
			}
			if kind == "symlink" && info.Mode()&os.ModeSymlink == 0 {
				t.Fatal("symlink replaced")
			}
		})
	}
}

func TestNodeRecoveryRetirementIsExactAndRejectsRedirection(t *testing.T) {
	for _, bad := range []string{"extra", "symlink", "hardlink", "mode", "directory-symlink"} {
		t.Run(bad, func(t *testing.T) {
			s := privateRenewalStore(t)
			dir := s.dir
			gen := strings.Repeat("a", 32)
			if err := s.SaveNodeIdentity(gen, NodeIdentityMaterial{CACert: []byte("ca"), Certificate: []byte("cert"), PrivateKey: []byte("key")}); err != nil {
				t.Fatal(err)
			}
			base := filepath.Join(dir, "node", gen)
			key := filepath.Join(base, "key.pem")
			outside := filepath.Join(dir, "outside")
			_ = os.WriteFile(outside, []byte("preserve"), 0600)
			switch bad {
			case "extra":
				_ = os.WriteFile(filepath.Join(base, "foreign"), []byte("retain"), 0600)
			case "symlink":
				_ = os.Remove(key)
				_ = os.Symlink(outside, key)
			case "hardlink":
				_ = os.Remove(key)
				_ = os.Link(outside, key)
			case "mode":
				_ = os.Chmod(key, 0644)
			case "directory-symlink":
				_ = os.Rename(base, base+"-evidence")
				_ = os.Symlink(base+"-evidence", base)
			}
			if err := s.RetireNodeGeneration(gen, false); err == nil {
				t.Fatal("unsafe generation retired")
			}
			body, _ := os.ReadFile(outside)
			if string(body) != "preserve" {
				t.Fatal("unrelated target changed")
			}
			if bad != "directory-symlink" {
				if _, err := os.Lstat(filepath.Join(base, "ca.pem")); err != nil {
					t.Fatal("deleted before completing preflight")
				}
			}
		})
	}
}

func TestNodeRecoveryJournalRejectsAmbiguousAuthority(t *testing.T) {
	s := privateRenewalStore(t)
	if err := s.SaveNodeRecoveryJournal(recoveryJournalFixture(), nil); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(s.NodeRecoveryJournalPath())
	for _, bad := range [][]byte{append(append([]byte{}, body...), []byte("{}")...), bytes.Replace(body, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1), bytes.Replace(body, []byte(`"version":1`), []byte(`"version":1,"unknown":1`), 1)} {
		if err := os.WriteFile(s.NodeRecoveryJournalPath(), bad, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.LoadNodeRecoveryJournal(); err == nil {
			t.Fatal("ambiguous journal accepted")
		}
	}
}

func TestNodeRecoveryPublicationCollision(t *testing.T) {
	s := privateRenewalStore(t)
	evidence := []byte("competing evidence")
	err := s.SaveNodeRecoveryJournal(recoveryJournalFixture(), func(point string) error {
		if point == "journal-synced" {
			return os.WriteFile(s.NodeRecoveryJournalPath(), evidence, 0600)
		}
		return nil
	})
	if err == nil {
		t.Fatal("publication replaced a competing journal")
	}
	got, err := os.ReadFile(s.NodeRecoveryJournalPath())
	if err != nil || !bytes.Equal(got, evidence) {
		t.Fatal("competing evidence changed")
	}
}
