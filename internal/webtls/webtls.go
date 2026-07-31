package webtls

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"golang.org/x/crypto/acme/autocert"
)

type Mode string

const (
	ModeOff          Mode = "off"
	ModeReverseProxy Mode = "reverse-proxy"
	ModeManual       Mode = "manual"
	ModeACMEDomain   Mode = "acme-domain"
)

const (
	SettingsRelativePath  = "tls/config.json"
	ACMECacheRelativePath = "tls/acme"
	ACMEHTTPAddress       = ":80"
)

type Settings struct {
	Mode          Mode   `json:"mode"`
	CertFile      string `json:"cert_file,omitempty"`
	KeyFile       string `json:"key_file,omitempty"`
	ServerName    string `json:"server_name,omitempty"`
	ACMEDomain    string `json:"acme_domain,omitempty"`
	ACMEEmail     string `json:"acme_email,omitempty"`
	ACMEAcceptTOS bool   `json:"acme_accept_tos,omitempty"`
}

type Status struct {
	Mode      Mode
	Subject   string
	Issuer    string
	NotBefore time.Time
	NotAfter  time.Time
	Domain    string
	State     string
	Error     string
}

type Runtime struct {
	Settings        Settings
	Status          Status
	TLSConfig       *tls.Config
	ACMEHTTPHandler http.Handler
	statusReader    func() Status
}

func (r Runtime) ReadStatus() Status {
	if r.statusReader != nil {
		return r.statusReader()
	}
	return r.Status
}

func Load(cfg config.Config) (Runtime, error) {
	settings, err := loadSettings(cfg)
	if err != nil {
		return Runtime{}, err
	}
	return buildRuntime(cfg, settings)
}

func loadSettings(cfg config.Config) (Settings, error) {
	path := settingsPath(cfg)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Settings{Mode: ModeOff}, nil
		}
		return Settings{}, errors.New("cannot inspect TLS settings file")
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Settings{}, errors.New("TLS settings file must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Settings{}, errors.New("TLS settings file permissions must not allow group or other access")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Settings{}, errors.New("cannot read TLS settings file")
	}
	var settings Settings
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return Settings{}, fmt.Errorf("invalid TLS settings: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Settings{}, errors.New("invalid TLS settings")
	}
	return settings, nil
}

func Save(cfg config.Config, settings Settings) error {
	if settings.Mode == ModeACMEDomain {
		domain, err := normalizeDomain(settings.ACMEDomain)
		if err != nil {
			return err
		}
		settings.ACMEDomain = domain
	}
	if _, err := buildRuntime(cfg, settings); err != nil {
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

func buildRuntime(cfg config.Config, settings Settings) (Runtime, error) {
	if err := validateSettings(settings); err != nil {
		return Runtime{}, err
	}
	if err := validateDeployment(cfg, settings); err != nil {
		return Runtime{}, err
	}
	runtime := Runtime{
		Settings: settings,
		Status:   Status{Mode: settings.Mode},
	}
	if settings.Mode == ModeACMEDomain {
		return buildACMERuntime(cfg, runtime)
	}
	if settings.Mode != ModeManual {
		return runtime, nil
	}
	status, pair, err := loadManual(settings)
	if err != nil {
		return Runtime{}, err
	}
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
	case ModeACMEDomain:
		domain, err := normalizeDomain(settings.ACMEDomain)
		if err != nil {
			return err
		}
		if settings.ACMEEmail == "" {
			return errors.New("ACME contact email is required")
		}
		address, err := mail.ParseAddress(settings.ACMEEmail)
		if err != nil || address.Address != settings.ACMEEmail {
			return errors.New("ACME contact email is invalid")
		}
		if !settings.ACMEAcceptTOS {
			return errors.New("ACME terms of service must be accepted")
		}
		settings.ACMEDomain = domain
		return nil
	default:
		return errors.New("TLS mode must be off, reverse-proxy, manual, or acme-domain")
	}
}

func validateDeployment(cfg config.Config, settings Settings) error {
	switch settings.Mode {
	case ModeReverseProxy:
		if cfg.Password == "" {
			return errors.New("PASSWORD is required when reverse-proxy TLS is active")
		}
		if !cfg.WebUITrustProxyHeaders || len(cfg.WebUITrustedProxyCIDRs) == 0 {
			return errors.New("trusted proxy headers and CIDRs are required when reverse-proxy TLS is active")
		}
	case ModeACMEDomain:
		if cfg.Password == "" {
			return errors.New("PASSWORD is required when ACME TLS is active")
		}
		if cfg.SessionCookieSecure == "false" {
			return errors.New("SESSION_COOKIE_SECURE=false is not allowed when ACME TLS is active")
		}
		if cfg.WebUITrustProxyHeaders {
			return errors.New("trusted proxy headers must be disabled when ACME TLS is active")
		}
		if hostIsLoopback(cfg.WebUIHost) {
			return errors.New("WEBUI_HOST must be publicly reachable when ACME TLS is active")
		}
	}
	return nil
}

func buildACMERuntime(cfg config.Config, runtime Runtime) (Runtime, error) {
	domain, err := normalizeDomain(runtime.Settings.ACMEDomain)
	if err != nil {
		return Runtime{}, err
	}
	runtime.Settings.ACMEDomain = domain
	cacheDir := filepath.Join(cfg.ConfigDir, filepath.FromSlash(ACMECacheRelativePath))
	if info, err := os.Lstat(cacheDir); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return Runtime{}, errors.New("ACME cache path must be a directory, not a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Runtime{}, errors.New("cannot inspect ACME cache directory")
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return Runtime{}, errors.New("cannot create ACME cache directory")
	}
	if err := os.Chmod(cacheDir, 0o700); err != nil {
		return Runtime{}, errors.New("cannot secure ACME cache directory")
	}
	manager := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Email:      runtime.Settings.ACMEEmail,
		Cache:      autocert.DirCache(cacheDir),
		HostPolicy: autocert.HostWhitelist(domain),
	}
	tlsConfig := manager.TLSConfig()
	tlsConfig.MinVersion = tls.VersionTLS13
	runtime.Status.Domain = domain
	runtime.Status.State = "pending"
	if status, ok := loadCachedACMEStatus(cacheDir, domain); ok {
		status.Mode = ModeACMEDomain
		status.Domain = domain
		runtime.Status = status
	}
	var statusMu sync.RWMutex
	currentStatus := runtime.Status
	getCertificate := tlsConfig.GetCertificate
	tlsConfig.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		pair, err := getCertificate(hello)
		if err != nil {
			statusMu.Lock()
			currentStatus.State = "failed"
			currentStatus.Error = "certificate issuance failed"
			statusMu.Unlock()
			return pair, err
		}
		if len(pair.Certificate) == 0 {
			return pair, err
		}
		leaf, parseErr := x509.ParseCertificate(pair.Certificate[0])
		if parseErr == nil {
			statusMu.Lock()
			currentStatus.Subject = leaf.Subject.String()
			currentStatus.Issuer = leaf.Issuer.String()
			currentStatus.NotBefore = leaf.NotBefore.UTC()
			currentStatus.NotAfter = leaf.NotAfter.UTC()
			currentStatus.State = "active"
			currentStatus.Error = ""
			statusMu.Unlock()
		}
		return pair, nil
	}
	runtime.TLSConfig = tlsConfig
	runtime.ACMEHTTPHandler = manager.HTTPHandler(acmeFallback(domain, cfg.WebUIPort))
	runtime.statusReader = func() Status {
		statusMu.RLock()
		defer statusMu.RUnlock()
		return currentStatus
	}
	return runtime, nil
}

func loadCachedACMEStatus(cacheDir, domain string) (Status, bool) {
	data, err := autocert.DirCache(cacheDir).Get(context.Background(), domain)
	if err != nil || len(data) == 0 {
		return Status{}, false
	}
	pair, err := tls.X509KeyPair(data, data)
	if err != nil || len(pair.Certificate) == 0 {
		return Status{}, false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || time.Now().After(leaf.NotAfter) || leaf.VerifyHostname(domain) != nil {
		return Status{}, false
	}
	return Status{Subject: leaf.Subject.String(), Issuer: leaf.Issuer.String(), NotBefore: leaf.NotBefore.UTC(), NotAfter: leaf.NotAfter.UTC(), State: "active"}, true
}

func acmeFallback(domain string, port int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		host := requestHost(r.Host)
		if host != domain {
			http.NotFound(w, r)
			return
		}
		target := "https://" + domain
		if port != 443 {
			target += fmt.Sprintf(":%d", port)
		}
		http.Redirect(w, r, target+r.URL.RequestURI(), http.StatusMovedPermanently)
	})
}

func requestHost(value string) string {
	value = strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	domain, err := normalizeDomain(value)
	if err != nil {
		return ""
	}
	return domain
}

func normalizeDomain(value string) (string, error) {
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if domain == "" || len(domain) > 253 || net.ParseIP(domain) != nil || strings.Contains(domain, "*") || !strings.Contains(domain, ".") {
		return "", errors.New("ACME domain must be a fully qualified DNS name")
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("ACME domain must be a valid DNS name")
		}
		for _, ch := range label {
			if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' {
				return "", errors.New("ACME domain must be a valid DNS name")
			}
		}
	}
	return domain, nil
}

func hostIsLoopback(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return address.IsLoopback()
	}
	return false
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
			return Status{}, tls.Certificate{}, fmt.Errorf("manual TLS certificate does not match the configured server name: %w", err)
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
