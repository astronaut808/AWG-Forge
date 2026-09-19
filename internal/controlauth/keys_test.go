package controlauth

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestKeyFileRoundTripAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(dir, KeyFileName)
	keys, err := LoadOrCreateKeys(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("key file mode = %o, want 600", got)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0700 {
		t.Fatalf("key directory mode = %o, want 700", got)
	}
	token, err := NewSessionToken(nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := keys.SessionDigest(token)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadOrCreateKeys(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := reloaded.SessionDigest(token)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("key file reload changed token digest")
	}
}

func TestTOTPSecretUsesUserBoundAuthenticatedEncryption(t *testing.T) {
	keys, err := LoadOrCreateKeys(filepath.Join(t.TempDir(), KeyFileName), nil)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := keys.SealTOTP("user-a", "JBSWY3DPEHPK3PXP", nil)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := keys.OpenTOTP("user-a", sealed)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("opened TOTP secret = %q", plain)
	}
	if _, err := keys.OpenTOTP("user-b", sealed); !errors.Is(err, ErrInvalidKeyFile) {
		t.Fatalf("wrong-user OpenTOTP error = %v", err)
	}
}

func TestKeyFileRejectsPermissiveModeAndSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, KeyFileName)
	keys, err := LoadOrCreateKeys(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if keys == nil {
		t.Fatal("nil keys")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKeys(path, nil); !errors.Is(err, ErrInvalidKeyFile) {
		t.Fatalf("permissive key file error = %v", err)
	}
	link := filepath.Join(dir, "link.keys")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKeys(link, nil); !errors.Is(err, ErrInvalidKeyFile) {
		t.Fatalf("symlink key file error = %v", err)
	}
}

func TestKeyFileRejectsNonPrivateOrSymlinkParent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(dir, KeyFileName)
	if _, err := LoadOrCreateKeys(path, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKeys(path, nil); !errors.Is(err, ErrInvalidKeyFile) {
		t.Fatalf("non-private key directory error = %v", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKeys(path, nil); !errors.Is(err, ErrInvalidKeyFile) {
		t.Fatalf("non-exact key file mode error = %v", err)
	}

	realDir := filepath.Join(t.TempDir(), "real")
	realPath := filepath.Join(realDir, KeyFileName)
	if _, err := LoadOrCreateKeys(realPath, nil); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKeys(filepath.Join(linkDir, KeyFileName), nil); !errors.Is(err, ErrInvalidKeyFile) {
		t.Fatalf("symlink key directory error = %v", err)
	}
}

func TestConcurrentKeyCreationPublishesOneCompleteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", KeyFileName)
	token, err := NewSessionToken(nil)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 16
	start := make(chan struct{})
	digests := make([]Digest, workers)
	errorsByWorker := make([]error, workers)
	var wait sync.WaitGroup
	for index := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			keys, err := LoadOrCreateKeys(path, nil)
			if err != nil {
				errorsByWorker[index] = err
				return
			}
			digests[index], errorsByWorker[index] = keys.SessionDigest(token)
		}()
	}
	close(start)
	wait.Wait()
	for index, err := range errorsByWorker {
		if err != nil {
			t.Fatalf("worker %d: %v", index, err)
		}
		if digests[index] != digests[0] {
			t.Fatalf("worker %d loaded different keys", index)
		}
	}
}

func TestRecoveryCodesNormalizeAndDigest(t *testing.T) {
	keys, err := LoadOrCreateKeys(filepath.Join(t.TempDir(), KeyFileName), nil)
	if err != nil {
		t.Fatal(err)
	}
	codes, err := NewRecoveryCodes(10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 10 {
		t.Fatalf("recovery codes = %d, want 10", len(codes))
	}
	for _, code := range codes {
		normalized, err := NormalizeRecoveryCode(code)
		if err != nil {
			t.Fatal(err)
		}
		first, err := keys.RecoveryDigest("user", code)
		if err != nil {
			t.Fatal(err)
		}
		second, err := keys.RecoveryDigest("user", normalized)
		if err != nil {
			t.Fatal(err)
		}
		if first != second {
			t.Fatal("formatted recovery code changed digest")
		}
	}
}
