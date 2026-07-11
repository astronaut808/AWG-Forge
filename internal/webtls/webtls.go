package webtls

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
)

type Mode string

type Source string

const (
	ModeOff          Mode = "off"
	ModeReverseProxy Mode = "reverse-proxy"
	ModeManual       Mode = "manual"

	SourceEnvironment Source = "environment"
	SourceManaged     Source = "managed"
)

const SettingsRelativePath = "tls/config.json"

type Settings struct {
	Mode       Mode   `json:"mode"`
	CertFile   string `json:"cert_file,omitempty"`
	KeyFile    string `json:"key_file,omitempty"`
	ServerName string `json:"server_name,omitempty"`
}

type Status struct {
	Mode      Mode
	Source    Source
	Subject   string
	Issuer    string
	NotBefore time.Time
	NotAfter  time.Time
}

type Runtime struct {
	Settings  Settings
	Source    Source
	Status    Status
	TLSConfig *tls.Config
}

func SettingsFromConfig(cfg config.Config) Settings {
	mode := Mode(strings.TrimSpace(cfg.WebUITLSMode))
	if mode == "" {
		mode = ModeOff
	}
	return Settings{
		Mode:       mode,
		CertFile:   strings.TrimSpace(cfg.WebUITLSCertFile),
		KeyFile:    strings.TrimSpace(cfg.WebUITLSKeyFile),
		ServerName: strings.TrimSpace(cfg.WebUITLSServerName),
	}
}

func Load(cfg config.Config) (Runtime, error) {
	settings, source, err := resolveSettings(cfg)
	if err != nil {
		return Runtime{}, err
	}
	return buildRuntime(cfg, settings, source)
}

func LoadEnvironment(cfg config.Config) (Runtime, error) {
	return buildRuntime(cfg, SettingsFromConfig(cfg), SourceEnvironment)
}

func UseEnvironment(cfg config.Config) (Runtime, error) {
	runtime, err := LoadEnvironment(cfg)
	if err != nil {
		return Runtime{}, err
	}
	if err := os.Remove(settingsPath(cfg)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Runtime{}, errors.New("cannot remove managed TLS settings")
	}
	return runtime, nil
}

func resolveSettings(cfg config.Config) (Settings, Source, error) {
	path := settingsPath(cfg)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SettingsFromConfig(cfg), SourceEnvironment, nil
		}
		return Settings{}, "", errors.New("cannot inspect TLS settings file")
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Settings{}, "", errors.New("TLS settings file must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Settings{}, "", errors.New("TLS settings file permissions must not allow group or other access")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Settings{}, "", errors.New("cannot read TLS settings file")
	}
	var settings Settings
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return Settings{}, "", fmt.Errorf("invalid TLS settings: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Settings{}, "", errors.New("invalid TLS settings")
	}
	return settings, SourceManaged, nil
}

func Save(cfg config.Config, settings Settings) error {
	if _, err := buildRuntime(cfg, settings, SourceManaged); err != nil {
		return err
	}
	dir := filepath.Dir(settingsPath(cfg))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, settingsPath(cfg))
}

func buildRuntime(cfg config.Config, settings Settings, source Source) (Runtime, error) {
	if err := validateSettings(settings); err != nil {
		return Runtime{}, err
	}
	if err := validateDeployment(cfg, settings); err != nil {
		return Runtime{}, err
	}
	runtime := Runtime{
		Settings: settings,
		Source:   source,
		Status:   Status{Mode: settings.Mode, Source: source},
	}
	if settings.Mode != ModeManual {
		return runtime, nil
	}
	status, pair, err := loadManual(settings)
	if err != nil {
		return Runtime{}, err
	}
	status.Source = source
	runtime.Status = status
	runtime.TLSConfig = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{pair},
		NextProtos:   []string{"h2", "http/1.1"},
	}
	return runtime, nil
}

func validateSettings(settings Settings) error {
	switch settings.Mode {
	case ModeOff, ModeReverseProxy:
		return nil
	case ModeManual:
		if strings.TrimSpace(settings.CertFile) == "" || strings.TrimSpace(settings.KeyFile) == "" {
			return errors.New("manual TLS certificate and key paths are required")
		}
		if !filepath.IsAbs(settings.CertFile) || !filepath.IsAbs(settings.KeyFile) {
			return errors.New("manual TLS certificate and key paths must be absolute")
		}
		if settings.CertFile == settings.KeyFile {
			return errors.New("manual TLS certificate and key paths must differ")
		}
		return nil
	default:
		return errors.New("TLS mode must be off, reverse-proxy, or manual")
	}
}

func validateDeployment(cfg config.Config, settings Settings) error {
	if settings.Mode != ModeReverseProxy {
		return nil
	}
	if cfg.Password == "" {
		return errors.New("PASSWORD is required when reverse-proxy TLS is active")
	}
	if !cfg.WebUITrustProxyHeaders || len(cfg.WebUITrustedProxyCIDRs) == 0 {
		return errors.New("trusted proxy headers and CIDRs are required when reverse-proxy TLS is active")
	}
	return nil
}

func loadManual(settings Settings) (Status, tls.Certificate, error) {
	if err := validateSettings(settings); err != nil {
		return Status{}, tls.Certificate{}, err
	}
	if err := checkRegularFile(settings.CertFile, false); err != nil {
		return Status{}, tls.Certificate{}, fmt.Errorf("manual TLS certificate: %w", err)
	}
	if err := checkRegularFile(settings.KeyFile, true); err != nil {
		return Status{}, tls.Certificate{}, fmt.Errorf("manual TLS private key: %w", err)
	}
	pair, err := tls.LoadX509KeyPair(settings.CertFile, settings.KeyFile)
	if err != nil {
		return Status{}, tls.Certificate{}, errors.New("load manual TLS certificate")
	}
	if len(pair.Certificate) == 0 {
		return Status{}, tls.Certificate{}, errors.New("manual TLS certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return Status{}, tls.Certificate{}, fmt.Errorf("parse manual TLS certificate: %w", err)
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return Status{}, tls.Certificate{}, errors.New("manual TLS certificate is not currently valid")
	}
	if settings.ServerName != "" {
		if err := leaf.VerifyHostname(settings.ServerName); err != nil {
			return Status{}, tls.Certificate{}, fmt.Errorf("manual TLS certificate does not match WEBUI_TLS_SERVER_NAME: %w", err)
		}
	}
	return Status{
		Mode:      ModeManual,
		Subject:   leaf.Subject.String(),
		Issuer:    leaf.Issuer.String(),
		NotBefore: leaf.NotBefore.UTC(),
		NotAfter:  leaf.NotAfter.UTC(),
	}, pair, nil
}

func checkRegularFile(path string, private bool) error {
	if private {
		if err := checkPrivateKeyDirectory(filepath.Dir(path)); err != nil {
			return err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return errors.New("cannot inspect file")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("must be a regular file, not a symlink")
	}
	if private && info.Mode().Perm() != 0o600 {
		return errors.New("permissions must be 0600")
	}
	return nil
}

func checkPrivateKeyDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return errors.New("cannot inspect private key directory")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("parent directory must be a directory, not a symlink")
	}
	if info.Mode().Perm() != 0o700 {
		return errors.New("parent directory permissions must be 0700")
	}
	return nil
}

func settingsPath(cfg config.Config) string {
	return filepath.Join(cfg.ConfigDir, filepath.FromSlash(SettingsRelativePath))
}
