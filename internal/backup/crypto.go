package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/argon2"
)

type encryptedArchive struct {
	Format     string    `json:"format"`
	Cipher     string    `json:"cipher"`
	KDF        kdfParams `json:"kdf"`
	Salt       string    `json:"salt"`
	Nonce      string    `json:"nonce"`
	Ciphertext string    `json:"ciphertext"`
	CreatedAt  string    `json:"created_at"`
}

type kdfParams struct {
	Name      string `json:"name"`
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Threads   uint8  `json:"threads"`
	KeySize   uint32 `json:"key_size"`
}

func encrypt(plain []byte, password string, now time.Time) ([]byte, error) {
	salt, err := randomBytes(16)
	if err != nil {
		return nil, err
	}
	nonce, err := randomBytes(12)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(password, salt)
	if err != nil {
		return nil, err
	}
	env := encryptedArchive{
		Format: formatVersion,
		Cipher: cipherName,
		KDF: kdfParams{
			Name:      kdfName,
			Time:      kdfTime,
			MemoryKiB: kdfMemoryKiB,
			Threads:   kdfThreads,
			KeySize:   keySize,
		},
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(aead.Seal(nil, nonce, plain, []byte(formatVersion))),
		CreatedAt:  now.Format(time.RFC3339),
	}
	return json.MarshalIndent(env, "", "  ")
}

func decrypt(data []byte, password string) ([]byte, error) {
	var env encryptedArchive
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if env.Format != formatVersion {
		return nil, fmt.Errorf("unsupported backup format %q", env.Format)
	}
	if env.Cipher != cipherName {
		return nil, fmt.Errorf("unsupported backup cipher %q", env.Cipher)
	}
	if env.KDF.Name != kdfName || env.KDF.KeySize != keySize ||
		env.KDF.Time != kdfTime || env.KDF.MemoryKiB != kdfMemoryKiB || env.KDF.Threads != kdfThreads {
		return nil, errors.New("unsupported backup kdf")
	}
	salt, err := base64.StdEncoding.DecodeString(env.Salt)
	if err != nil {
		return nil, err
	}
	nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return nil, err
	}
	ciphertext, err := base64.StdEncoding.DecodeString(env.Ciphertext)
	if err != nil {
		return nil, err
	}
	key := argon2.IDKey([]byte(password), salt, env.KDF.Time, env.KDF.MemoryKiB, env.KDF.Threads, env.KDF.KeySize)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, nonce, ciphertext, []byte(formatVersion))
	if err != nil {
		return nil, errors.New("backup decrypt failed: wrong password or corrupted archive")
	}
	return plain, nil
}

func newAEAD(password string, salt []byte) (cipher.AEAD, error) {
	key := argon2.IDKey([]byte(password), salt, kdfTime, kdfMemoryKiB, kdfThreads, keySize)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}
