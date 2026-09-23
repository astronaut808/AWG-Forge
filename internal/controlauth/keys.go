package controlauth

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	KeyFileName       = "controller-auth.keys"
	keySize           = 32
	sessionTokenBytes = 32
	recoveryCodeBytes = 12
)

var (
	ErrInvalidKeyFile      = errors.New("invalid controller auth key file")
	ErrInvalidSessionToken = errors.New("invalid controller session token")
	ErrInvalidRecoveryCode = errors.New("invalid controller recovery code")
)

type Digest [sha256.Size]byte

type Keys struct {
	encryption [keySize]byte
	digest     [keySize]byte
}

type diskKeys struct {
	Version       int    `json:"version"`
	EncryptionKey string `json:"encryption_key"`
	DigestKey     string `json:"digest_key"`
}

func LoadOrCreateKeys(path string, random io.Reader) (*Keys, error) {
	if path == "" {
		return nil, ErrInvalidKeyFile
	}
	if _, err := os.Lstat(path); err == nil {
		return LoadKeys(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if random == nil {
		random = rand.Reader
	}
	keys := &Keys{}
	if _, err := io.ReadFull(random, keys.encryption[:]); err != nil {
		return nil, fmt.Errorf("generate controller encryption key: %w", err)
	}
	if _, err := io.ReadFull(random, keys.digest[:]); err != nil {
		return nil, fmt.Errorf("generate controller digest key: %w", err)
	}
	body, err := json.Marshal(diskKeys{
		Version:       1,
		EncryptionKey: base64.RawStdEncoding.EncodeToString(keys.encryption[:]),
		DigestKey:     base64.RawStdEncoding.EncodeToString(keys.digest[:]),
	})
	if err != nil {
		return nil, err
	}
	body = append(body, '\n')
	if err := createPrivateFile(path, body); err != nil {
		if errors.Is(err, os.ErrExist) {
			return LoadKeys(path)
		}
		return nil, err
	}
	return keys, nil
}

// LoadKeys loads existing controller authentication keys without ever creating
// replacements. Controller startup uses this fail-closed path.
func LoadKeys(path string) (*Keys, error) {
	if err := validatePrivateDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, ErrInvalidKeyFile
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var stored diskKeys
	if err := decoder.Decode(&stored); err != nil {
		return nil, ErrInvalidKeyFile
	}
	if err := ensureJSONEnd(decoder); err != nil || stored.Version != 1 {
		return nil, ErrInvalidKeyFile
	}
	decode := base64.RawStdEncoding.Strict()
	encryption, err := decode.DecodeString(stored.EncryptionKey)
	if err != nil || len(encryption) != keySize {
		return nil, ErrInvalidKeyFile
	}
	digest, err := decode.DecodeString(stored.DigestKey)
	if err != nil || len(digest) != keySize {
		return nil, ErrInvalidKeyFile
	}
	keys := &Keys{}
	copy(keys.encryption[:], encryption)
	copy(keys.digest[:], digest)
	return keys, nil
}

// RemoveKeyFile removes only a validated regular key file from a validated
// private directory. It never follows a symlink.
func RemoveKeyFile(path string) error {
	if path == "" {
		return ErrInvalidKeyFile
	}
	if err := validatePrivateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return ErrInvalidKeyFile
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	if parent, err := os.Open(filepath.Dir(path)); err == nil {
		_ = parent.Sync()
		_ = parent.Close()
	}
	return nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalidKeyFile
	}
	return nil
}

func createPrivateFile(path string, body []byte) error {
	dir := filepath.Dir(path)
	if err := ensurePrivateDirectory(dir); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		_ = os.Remove(tempPath)
	}()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if _, err := file.Write(body); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	closed = true
	if err := os.Link(tempPath, path); err != nil {
		return err
	}
	if err := os.Remove(tempPath); err != nil {
		return err
	}
	if parent, err := os.Open(dir); err == nil {
		_ = parent.Sync()
		_ = parent.Close()
	}
	return nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidKeyFile
	}
	if info.Mode().Perm() != 0700 {
		if err := os.Chmod(path, 0700); err != nil {
			return err
		}
	}
	return validatePrivateDirectory(path)
}

func validatePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return ErrInvalidKeyFile
	}
	return nil
}

func (k *Keys) SealTOTP(userID, secret string, random io.Reader) (string, error) {
	if k == nil || userID == "" || secret == "" {
		return "", ErrInvalidKeyFile
	}
	block, err := aes.NewCipher(k.encryption[:])
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if random == nil {
		random = rand.Reader
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(random, nonce); err != nil {
		return "", fmt.Errorf("generate TOTP nonce: %w", err)
	}
	sealed := aead.Seal(nonce, nonce, []byte(secret), totpAssociatedData(userID))
	return "v1." + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (k *Keys) OpenTOTP(userID, sealed string) (string, error) {
	if k == nil || userID == "" || !strings.HasPrefix(sealed, "v1.") {
		return "", ErrInvalidKeyFile
	}
	encoded := strings.TrimPrefix(sealed, "v1.")
	payload, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return "", ErrInvalidKeyFile
	}
	block, err := aes.NewCipher(k.encryption[:])
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(payload) < aead.NonceSize()+aead.Overhead() {
		return "", ErrInvalidKeyFile
	}
	nonce, ciphertext := payload[:aead.NonceSize()], payload[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, ciphertext, totpAssociatedData(userID))
	if err != nil {
		return "", ErrInvalidKeyFile
	}
	return string(plain), nil
}

func (k *Keys) SessionDigest(token string) (Digest, error) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(decoded) != sessionTokenBytes {
		return Digest{}, ErrInvalidSessionToken
	}
	return k.digestValue("session", decoded), nil
}

func (k *Keys) RecoveryDigest(userID, code string) (Digest, error) {
	normalized, err := NormalizeRecoveryCode(code)
	if err != nil || userID == "" {
		return Digest{}, ErrInvalidRecoveryCode
	}
	return k.digestValue("recovery", []byte(userID+"\x00"+normalized)), nil
}

func (k *Keys) AccountDigest(username string) Digest {
	return k.digestValue("account", []byte(username))
}

func (k *Keys) SourceDigest(source string) Digest {
	return k.digestValue("source", []byte(source))
}

func (k *Keys) digestValue(purpose string, value []byte) Digest {
	mac := hmac.New(sha256.New, k.digest[:])
	_, _ = mac.Write([]byte("awg-forge/controller-auth/" + purpose + "/v1\x00"))
	_, _ = mac.Write(value)
	var digest Digest
	copy(digest[:], mac.Sum(nil))
	return digest
}

func NewSessionToken(random io.Reader) (string, error) {
	if random == nil {
		random = rand.Reader
	}
	value := make([]byte, sessionTokenBytes)
	if _, err := io.ReadFull(random, value); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func NewRecoveryCodes(count int, random io.Reader) ([]string, error) {
	if count < 1 || count > 32 {
		return nil, ErrInvalidRecoveryCode
	}
	if random == nil {
		random = rand.Reader
	}
	encoder := base32.StdEncoding.WithPadding(base32.NoPadding)
	codes := make([]string, 0, count)
	seen := make(map[string]struct{}, count)
	for len(codes) < count {
		value := make([]byte, recoveryCodeBytes)
		if _, err := io.ReadFull(random, value); err != nil {
			return nil, fmt.Errorf("generate recovery code: %w", err)
		}
		raw := encoder.EncodeToString(value)
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		codes = append(codes, groupRecoveryCode(raw))
	}
	return codes, nil
}

func NormalizeRecoveryCode(code string) (string, error) {
	code = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	if len(code) != 20 {
		return "", ErrInvalidRecoveryCode
	}
	for _, char := range code {
		if (char < 'A' || char > 'Z') && (char < '2' || char > '7') {
			return "", ErrInvalidRecoveryCode
		}
	}
	return code, nil
}

func groupRecoveryCode(raw string) string {
	groups := make([]string, 0, (len(raw)+3)/4)
	for len(raw) > 4 {
		groups = append(groups, raw[:4])
		raw = raw[4:]
	}
	groups = append(groups, raw)
	return strings.Join(groups, "-")
}

func totpAssociatedData(userID string) []byte {
	return []byte("awg-forge/controller-auth/totp/v1\x00" + userID)
}
