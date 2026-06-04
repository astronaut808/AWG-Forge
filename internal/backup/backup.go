package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

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

	maxEncryptedBackupBytes = int64(64 << 20)
)

type Archive struct {
	Name string
	Data []byte
}

type Options struct {
	Now time.Time
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

type VerifyReport struct {
	Format        string         `json:"format"`
	CreatedAt     string         `json:"created_at"`
	Build         buildinfo.Info `json:"build"`
	SchemaVersion int            `json:"schema_version"`
	ServerHost    string         `json:"server_host"`
	FileCount     int            `json:"file_count"`
	TotalSize     int64          `json:"total_size"`
	Tunnels       []VerifyTunnel `json:"tunnels"`
	ClientCount   int            `json:"client_count"`
}

type VerifyTunnel struct {
	Name       string `json:"name"`
	Interface  string `json:"interface"`
	Profile    string `json:"profile"`
	ListenPort int    `json:"listen_port"`
	Subnet     string `json:"subnet"`
	Clients    int    `json:"clients"`
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
	validated, err := loadAndValidate(password, path)
	if err != nil {
		return err
	}
	if preRestore, ok, err := preRestoreBackupFile(ctx, cfg, password); err != nil {
		return err
	} else if ok {
		validated.Files = append(validated.Files, preRestore)
	}
	return restoreFiles(cfg.ConfigDir, validated.Files)
}

func Verify(ctx context.Context, cfg config.Config, password, path string) (VerifyReport, error) {
	_ = ctx
	_ = cfg
	validated, err := loadAndValidate(password, path)
	if err != nil {
		return VerifyReport{}, err
	}
	return verifyReport(validated.Metadata, validated.State), nil
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

type restoreFile struct {
	Path string
	Data []byte
}

type validatedBackup struct {
	Files    []restoreFile
	Metadata Metadata
	State    config.State
}

func loadAndValidate(password, archivePath string) (validatedBackup, error) {
	if err := validatePassword(password); err != nil {
		return validatedBackup{}, err
	}
	if strings.TrimSpace(archivePath) == "" {
		return validatedBackup{}, errors.New("backup file is required")
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		return validatedBackup{}, err
	}
	if info.Size() > maxEncryptedBackupBytes {
		return validatedBackup{}, errors.New("backup file is too large")
	}
	data, err := os.ReadFile(archivePath)
	if err != nil {
		return validatedBackup{}, err
	}
	plain, err := decrypt(data, password)
	if err != nil {
		return validatedBackup{}, err
	}
	files, metadata, state, err := readPlainZip(plain)
	if err != nil {
		return validatedBackup{}, err
	}
	if metadata.SchemaVersion > config.CurrentStateSchemaVersion || state.SchemaVersion > config.CurrentStateSchemaVersion {
		return validatedBackup{}, fmt.Errorf("backup schema %d is newer than supported schema %d", max(metadata.SchemaVersion, state.SchemaVersion), config.CurrentStateSchemaVersion)
	}
	if err := validateStateSanity(state); err != nil {
		return validatedBackup{}, err
	}
	for _, tunnel := range state.Tunnels {
		if _, err := render.ServerConfig(state, tunnel); err != nil {
			return validatedBackup{}, fmt.Errorf("backup validation failed for %s: %w", tunnel.Name, err)
		}
		for _, client := range tunnel.Clients {
			if _, err := render.ClientConfig(state, tunnel, client); err != nil {
				return validatedBackup{}, fmt.Errorf("backup client validation failed for %s/%s: %w", tunnel.Name, client.Name, err)
			}
		}
	}
	return validatedBackup{Files: files, Metadata: metadata, State: state}, nil
}

func validateStateSanity(state config.State) error {
	tunnelIDs := map[string]bool{}
	tunnelNames := map[string]bool{}
	interfaces := map[string]bool{}
	ports := map[int]bool{}
	subnets := map[string]bool{}
	clientIDs := map[string]bool{}

	for _, tunnel := range state.Tunnels {
		if strings.TrimSpace(tunnel.ID) == "" {
			return errors.New("backup validation failed: tunnel id is empty")
		}
		if tunnelIDs[tunnel.ID] {
			return fmt.Errorf("backup validation failed: tunnel id %q is duplicated", tunnel.ID)
		}
		tunnelIDs[tunnel.ID] = true

		if strings.TrimSpace(tunnel.Name) == "" {
			return errors.New("backup validation failed: tunnel name is empty")
		}
		if tunnelNames[tunnel.Name] {
			return fmt.Errorf("backup validation failed: tunnel name %q is duplicated", tunnel.Name)
		}
		tunnelNames[tunnel.Name] = true

		if strings.TrimSpace(tunnel.InterfaceName) == "" {
			return fmt.Errorf("backup validation failed: tunnel %s interface is empty", tunnel.Name)
		}
		if interfaces[tunnel.InterfaceName] {
			return fmt.Errorf("backup validation failed: interface %q is duplicated", tunnel.InterfaceName)
		}
		interfaces[tunnel.InterfaceName] = true

		if tunnel.ListenPort <= 0 || tunnel.ListenPort > 65535 {
			return fmt.Errorf("backup validation failed: tunnel %s listen port %d is invalid", tunnel.Name, tunnel.ListenPort)
		}
		if ports[tunnel.ListenPort] {
			return fmt.Errorf("backup validation failed: listen port %d is duplicated", tunnel.ListenPort)
		}
		ports[tunnel.ListenPort] = true

		if _, _, err := net.ParseCIDR(tunnel.IPv4Subnet); err != nil {
			return fmt.Errorf("backup validation failed: tunnel %s subnet is invalid: %w", tunnel.Name, err)
		}
		if subnets[tunnel.IPv4Subnet] {
			return fmt.Errorf("backup validation failed: subnet %q is duplicated", tunnel.IPv4Subnet)
		}
		subnets[tunnel.IPv4Subnet] = true

		for _, client := range tunnel.Clients {
			if strings.TrimSpace(client.ID) == "" {
				return fmt.Errorf("backup validation failed: tunnel %s has empty client id", tunnel.Name)
			}
			if clientIDs[client.ID] {
				return fmt.Errorf("backup validation failed: client id %q is duplicated", client.ID)
			}
			clientIDs[client.ID] = true
			if strings.TrimSpace(client.Name) == "" {
				return fmt.Errorf("backup validation failed: tunnel %s has empty client name", tunnel.Name)
			}
			if net.ParseIP(strings.TrimSuffix(client.IPv4Address, "/32")) == nil {
				return fmt.Errorf("backup validation failed: client %s/%s address %q is invalid", tunnel.Name, client.Name, client.IPv4Address)
			}
		}
	}
	return nil
}

func verifyReport(metadata Metadata, state config.State) VerifyReport {
	report := VerifyReport{
		Format:        metadata.Format,
		CreatedAt:     metadata.CreatedAt,
		Build:         metadata.Build,
		SchemaVersion: max(metadata.SchemaVersion, state.SchemaVersion),
		ServerHost:    state.ServerHost,
		FileCount:     len(metadata.Files),
	}
	for _, file := range metadata.Files {
		report.TotalSize += file.Size
	}
	for _, tunnel := range state.Tunnels {
		report.ClientCount += len(tunnel.Clients)
		report.Tunnels = append(report.Tunnels, VerifyTunnel{
			Name:       tunnel.Name,
			Interface:  tunnel.InterfaceName,
			Profile:    tunnel.ProtocolProfileID,
			ListenPort: tunnel.ListenPort,
			Subnet:     tunnel.IPv4Subnet,
			Clients:    len(tunnel.Clients),
		})
	}
	return report
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
		if file.FileInfo().IsDir() {
			continue
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
			if hasMeta {
				return nil, Metadata{}, config.State{}, errors.New("backup metadata.json is duplicated")
			}
			if err := json.Unmarshal(b, &metadata); err != nil {
				return nil, Metadata{}, config.State{}, err
			}
			hasMeta = true
		case "state.json":
			if hasState {
				return nil, Metadata{}, config.State{}, errors.New("backup state.json is duplicated")
			}
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
		clean, err := cleanArchivePath(file.Path)
		if err != nil {
			return err
		}
		if _, ok := data[clean]; ok {
			return fmt.Errorf("backup file %s is duplicated", clean)
		}
		data[clean] = file.Data
	}
	expected := map[string]FileMeta{}
	for _, meta := range metas {
		clean, err := cleanArchivePath(meta.Path)
		if err != nil {
			return err
		}
		if clean != meta.Path {
			return fmt.Errorf("backup metadata path %s is not normalized", meta.Path)
		}
		if _, ok := expected[clean]; ok {
			return fmt.Errorf("backup metadata file %s is duplicated", clean)
		}
		expected[clean] = meta
	}
	for path := range data {
		if _, ok := expected[path]; !ok {
			return fmt.Errorf("backup file %s is not listed in metadata", path)
		}
	}
	for path, meta := range expected {
		b, ok := data[path]
		if !ok {
			return fmt.Errorf("backup file %s is missing", path)
		}
		if int64(len(b)) != meta.Size {
			return fmt.Errorf("backup file %s size mismatch", path)
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != meta.SHA256 {
			return fmt.Errorf("backup file %s checksum mismatch", path)
		}
	}
	return nil
}
