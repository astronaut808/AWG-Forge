package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/buildinfo"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/render"
)

const (
	formatVersion = "awg-forge-backup-v1"
	cipherName    = "AES-256-GCM"
	kdfName       = "argon2id"
	kdfTime       = uint32(3)
	kdfMemoryKiB  = uint32(64 * 1024)
	kdfThreads    = uint8(4)
	keySize       = uint32(32)
)

type Archive struct {
	Name string
	Data []byte
}

type Options struct {
	Now time.Time
}

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

type Metadata struct {
	Format        string         `json:"format"`
	CreatedAt     string         `json:"created_at"`
	Build         buildinfo.Info `json:"build"`
	SchemaVersion int            `json:"schema_version"`
	ServerHost    string         `json:"server_host"`
	Tunnels       []string       `json:"tunnels"`
	Files         []FileMeta     `json:"files"`
	Warning       string         `json:"warning"`
}

type FileMeta struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func Create(ctx context.Context, cfg config.Config, service *app.Service, password string, opts Options) (Archive, error) {
	_ = ctx
	if err := validatePassword(password); err != nil {
		return Archive{}, err
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	state, err := service.Init()
	if err != nil {
		return Archive{}, err
	}
	plain, err := createPlainZip(cfg, state, now)
	if err != nil {
		return Archive{}, err
	}
	data, err := encrypt(plain, password, now)
	if err != nil {
		return Archive{}, err
	}
	return Archive{
		Name: fmt.Sprintf("awg-forge-backup-%s.afbackup", now.Format("20060102-150405")),
		Data: data,
	}, nil
}

func WriteFile(ctx context.Context, cfg config.Config, service *app.Service, password, path string) (string, error) {
	archive, err := Create(ctx, cfg, service, password, Options{})
	if err != nil {
		return "", err
	}
	if path == "" {
		path = archive.Name
	}
	if err := os.WriteFile(path, archive.Data, 0600); err != nil {
		return "", err
	}
	return path, nil
}

func Restore(ctx context.Context, cfg config.Config, password, path string) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("backup file is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	plain, err := decrypt(data, password)
	if err != nil {
		return err
	}
	files, metadata, state, err := readPlainZip(plain)
	if err != nil {
		return err
	}
	if metadata.SchemaVersion > config.CurrentStateSchemaVersion || state.SchemaVersion > config.CurrentStateSchemaVersion {
		return fmt.Errorf("backup schema %d is newer than supported schema %d", max(metadata.SchemaVersion, state.SchemaVersion), config.CurrentStateSchemaVersion)
	}
	for _, tunnel := range state.Tunnels {
		if _, err := render.ServerConfig(state, tunnel); err != nil {
			return fmt.Errorf("backup validation failed for %s: %w", tunnel.Name, err)
		}
	}
	if preRestore, ok, err := preRestoreBackupFile(ctx, cfg, password); err != nil {
		return err
	} else if ok {
		files = append(files, preRestore)
	}
	return restoreFiles(cfg.ConfigDir, files)
}

func validatePassword(password string) error {
	if len(password) < 8 {
		return errors.New("backup password must be at least 8 characters")
	}
	return nil
}

func createPlainZip(cfg config.Config, state config.State, now time.Time) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	var metas []FileMeta
	if err := addExistingFile(zw, cfg.ConfigDir, "state.json", &metas); err != nil {
		return nil, err
	}
	tunnelRoot := filepath.Join(cfg.ConfigDir, "tunnels")
	if err := filepath.WalkDir(tunnelRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".conf" {
			return nil
		}
		rel, err := filepath.Rel(cfg.ConfigDir, path)
		if err != nil {
			return err
		}
		return addExistingFile(zw, cfg.ConfigDir, rel, &metas)
	}); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	metadata := Metadata{
		Format:        formatVersion,
		CreatedAt:     now.Format(time.RFC3339),
		Build:         buildinfo.Current(),
		SchemaVersion: state.SchemaVersion,
		ServerHost:    state.ServerHost,
		Files:         metas,
		Warning:       "This encrypted backup contains private keys and preshared keys.",
	}
	for _, tunnel := range state.Tunnels {
		metadata.Tunnels = append(metadata.Tunnels, tunnel.Name)
	}
	if err := addJSON(zw, "metadata.json", metadata); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func addExistingFile(zw *zip.Writer, root, rel string, metas *[]FileMeta) error {
	clean, err := cleanArchivePath(rel)
	if err != nil {
		return err
	}
	path := filepath.Join(root, filepath.FromSlash(clean))
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := addBytes(zw, clean, b); err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	*metas = append(*metas, FileMeta{Path: clean, Size: int64(len(b)), SHA256: hex.EncodeToString(sum[:])})
	return nil
}

func addJSON(zw *zip.Writer, name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return addBytes(zw, name, append(b, '\n'))
}

func addBytes(zw *zip.Writer, name string, b []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
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
	if env.KDF.Name != kdfName || env.KDF.KeySize != keySize {
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

type restoreFile struct {
	Path string
	Data []byte
}

func readPlainZip(data []byte) ([]restoreFile, Metadata, config.State, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, Metadata{}, config.State{}, err
	}
	var (
		files    []restoreFile
		metadata Metadata
		state    config.State
		hasMeta  bool
		hasState bool
	)
	for _, file := range reader.File {
		name, err := cleanArchivePath(file.Name)
		if err != nil {
			return nil, Metadata{}, config.State{}, err
		}
		rc, err := file.Open()
		if err != nil {
			return nil, Metadata{}, config.State{}, err
		}
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, Metadata{}, config.State{}, err
		}
		switch name {
		case "metadata.json":
			if err := json.Unmarshal(b, &metadata); err != nil {
				return nil, Metadata{}, config.State{}, err
			}
			hasMeta = true
		case "state.json":
			if err := json.Unmarshal(b, &state); err != nil {
				return nil, Metadata{}, config.State{}, err
			}
			hasState = true
			files = append(files, restoreFile{Path: name, Data: b})
		default:
			if name == "" || strings.HasSuffix(name, "/") {
				continue
			}
			files = append(files, restoreFile{Path: name, Data: b})
		}
	}
	if !hasMeta {
		return nil, Metadata{}, config.State{}, errors.New("backup metadata.json is missing")
	}
	if metadata.Format != formatVersion {
		return nil, Metadata{}, config.State{}, fmt.Errorf("unsupported backup metadata format %q", metadata.Format)
	}
	if !hasState {
		return nil, Metadata{}, config.State{}, errors.New("backup state.json is missing")
	}
	if err := verifyChecksums(files, metadata.Files); err != nil {
		return nil, Metadata{}, config.State{}, err
	}
	return files, metadata, state, nil
}

func verifyChecksums(files []restoreFile, metas []FileMeta) error {
	data := map[string][]byte{}
	for _, file := range files {
		data[file.Path] = file.Data
	}
	for _, meta := range metas {
		b, ok := data[meta.Path]
		if !ok {
			return fmt.Errorf("backup file %s is missing", meta.Path)
		}
		if int64(len(b)) != meta.Size {
			return fmt.Errorf("backup file %s size mismatch", meta.Path)
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != meta.SHA256 {
			return fmt.Errorf("backup file %s checksum mismatch", meta.Path)
		}
	}
	return nil
}

func cleanArchivePath(path string) (string, error) {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" || strings.HasPrefix(path, "/") {
		return "", errors.New("invalid archive path")
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", errors.New("invalid archive path")
	}
	return clean, nil
}

func preRestoreBackupFile(ctx context.Context, cfg config.Config, password string) (restoreFile, bool, error) {
	if _, err := os.Stat(filepath.Join(cfg.ConfigDir, "state.json")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return restoreFile{}, false, nil
		}
		return restoreFile{}, false, err
	}
	service := app.New(cfg)
	archive, err := Create(ctx, cfg, service, password, Options{})
	if err != nil {
		return restoreFile{}, false, err
	}
	path := "backups/pre-restore-" + time.Now().UTC().Format("20060102-150405") + ".afbackup"
	return restoreFile{Path: path, Data: archive.Data}, true, nil
}

func restoreFiles(root string, files []restoreFile) error {
	tmp := root + ".restore-tmp-" + time.Now().UTC().Format("20060102-150405")
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := os.MkdirAll(tmp, 0700); err != nil {
		return err
	}
	for _, file := range files {
		clean, err := cleanArchivePath(file.Path)
		if err != nil {
			return err
		}
		dst := filepath.Join(tmp, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(dst, file.Data, 0600); err != nil {
			return err
		}
	}
	if err := os.Chmod(tmp, 0700); err != nil {
		return err
	}
	old := root + ".restore-old-" + time.Now().UTC().Format("20060102-150405")
	if _, err := os.Stat(root); err == nil {
		if err := os.Rename(root, old); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmp, root); err != nil {
		_ = os.Rename(old, root)
		return err
	}
	_ = os.RemoveAll(old)
	return nil
}
