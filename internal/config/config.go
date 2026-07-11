package config

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultConfigDir = "/etc/awg-forge"
	DefaultTunnel    = "awg0"
)

type Config struct {
	ConfigDir              string
	TunnelName             string
	ServerHost             string
	ListenPort             int
	WebUIHost              string
	WebUIPort              int
	Password               string
	SessionSecret          string
	SessionCookieSecure    string
	WebUITLSMode           string
	WebUITLSCertFile       string
	WebUITLSKeyFile        string
	WebUITLSServerName     string
	WebUITrustProxyHeaders bool
	WebUITrustedProxyCIDRs []netip.Prefix
	ExternalInterface      string
	IPv4Subnet             string
	DNS                    string
	AllowedIPs             string
	PersistentKeepalive    int
	MTU                    int
	ProtocolProfile        string
	ApplyConfig            bool
	PublishedUDPPorts      string
	AuditLogEnabled        bool
	AuditLogPath           string
	AuditLogMaxSize        int64
	AuditLogMaxFiles       int
	DatabaseMode           string
	DatabasePath           string
	DatabaseDSN            string
	DatabaseRetention      int
	DatabaseBusyTimeout    time.Duration
	DatabaseQueryTimeout   time.Duration
	DatabaseMaxOpenConns   int
	DatabaseMaxIdleConns   int
	LegacyTunnelEnvVars    []string
}

func FromEnv() (Config, error) {
	configDir := getenv("CONFIG_DIR", DefaultConfigDir)
	cfg := Config{
		ConfigDir:              configDir,
		TunnelName:             getenv("TUNNEL_NAME", ""),
		ServerHost:             getenv("SERVER_HOST", "127.0.0.1"),
		ListenPort:             getenvInt("LISTEN_PORT", 0),
		WebUIHost:              getenv("WEBUI_HOST", "127.0.0.1"),
		WebUIPort:              getenvInt("WEBUI_PORT", 51821),
		Password:               os.Getenv("PASSWORD"),
		SessionSecret:          os.Getenv("SESSION_SECRET"),
		SessionCookieSecure:    getenv("SESSION_COOKIE_SECURE", "auto"),
		WebUITLSMode:           getenv("WEBUI_TLS_MODE", "off"),
		WebUITLSCertFile:       os.Getenv("WEBUI_TLS_CERT_FILE"),
		WebUITLSKeyFile:        os.Getenv("WEBUI_TLS_KEY_FILE"),
		WebUITLSServerName:     os.Getenv("WEBUI_TLS_SERVER_NAME"),
		WebUITrustProxyHeaders: getenvBool("WEBUI_TRUST_PROXY_HEADERS", false),
		ExternalInterface:      getenv("EXTERNAL_INTERFACE", "eth0"),
		IPv4Subnet:             getenv("IPV4_SUBNET", ""),
		DNS:                    getenv("DNS", "1.1.1.1"),
		AllowedIPs:             getenv("ALLOWED_IPS", "0.0.0.0/0"),
		PersistentKeepalive:    getenvInt("PERSISTENT_KEEPALIVE", 0),
		MTU:                    getenvInt("MTU", 0),
		ProtocolProfile:        getenv("PROTOCOL_PROFILE", "awg_2_0"),
		ApplyConfig:            getenvBool("APPLY_CONFIG", false),
		PublishedUDPPorts:      os.Getenv("PUBLISHED_UDP_PORTS"),
		AuditLogEnabled:        getenvBool("AUDIT_LOG_ENABLED", true),
		AuditLogPath:           getenv("AUDIT_LOG_PATH", filepath.Join(configDir, "audit.log")),
		AuditLogMaxSize:        getenvInt64("AUDIT_LOG_MAX_SIZE", 5*1024*1024),
		AuditLogMaxFiles:       getenvInt("AUDIT_LOG_MAX_FILES", 3),
		DatabaseMode:           getenv("DATABASE_MODE", "off"),
		DatabasePath:           getenv("DATABASE_PATH", filepath.Join(configDir, "awg-forge.db")),
		DatabaseDSN:            os.Getenv("DATABASE_DSN"),
		DatabaseRetention:      getenvInt("DATABASE_RETENTION_DAYS", 90),
		DatabaseBusyTimeout:    getenvDuration("DATABASE_BUSY_TIMEOUT", 5*time.Second),
		DatabaseQueryTimeout:   getenvDuration("DATABASE_QUERY_TIMEOUT", 2*time.Second),
		DatabaseMaxOpenConns:   getenvInt("DATABASE_MAX_OPEN_CONNS", 1),
		DatabaseMaxIdleConns:   getenvInt("DATABASE_MAX_IDLE_CONNS", 1),
		LegacyTunnelEnvVars:    legacyTunnelEnvVars(),
	}
	if cfg.WebUIHost == "0.0.0.0" || cfg.WebUIHost == "::" {
		if cfg.Password == "" {
			return Config{}, errors.New("PASSWORD is required when WEBUI_HOST is public")
		}
	}
	switch cfg.SessionCookieSecure {
	case "auto", "true", "false":
	default:
		return Config{}, errors.New("SESSION_COOKIE_SECURE must be auto, true, or false")
	}
	if err := configureWebTLS(&cfg); err != nil {
		return Config{}, err
	}
	if cfg.IPv4Subnet != "" {
		if _, _, err := net.ParseCIDR(cfg.IPv4Subnet); err != nil {
			return Config{}, err
		}
	}
	switch cfg.DatabaseMode {
	case "off", "sqlite", "postgres":
	default:
		return Config{}, errors.New("DATABASE_MODE must be off, sqlite, or postgres")
	}
	if cfg.DatabaseRetention < 1 {
		return Config{}, errors.New("DATABASE_RETENTION_DAYS must be positive")
	}
	if cfg.DatabaseBusyTimeout <= 0 {
		return Config{}, errors.New("DATABASE_BUSY_TIMEOUT must be positive")
	}
	if cfg.DatabaseQueryTimeout <= 0 {
		return Config{}, errors.New("DATABASE_QUERY_TIMEOUT must be positive")
	}
	if cfg.DatabaseMaxOpenConns < 1 {
		return Config{}, errors.New("DATABASE_MAX_OPEN_CONNS must be positive")
	}
	if cfg.DatabaseMaxIdleConns < 0 {
		return Config{}, errors.New("DATABASE_MAX_IDLE_CONNS must not be negative")
	}
	return cfg, nil
}

func configureWebTLS(cfg *Config) error {
	trustedProxyCIDRs, err := parseTrustedProxyCIDRs(os.Getenv("WEBUI_TRUSTED_PROXY_CIDRS"))
	if err != nil {
		return err
	}
	cfg.WebUITrustedProxyCIDRs = trustedProxyCIDRs
	if cfg.WebUITrustProxyHeaders && len(cfg.WebUITrustedProxyCIDRs) == 0 {
		return errors.New("WEBUI_TRUSTED_PROXY_CIDRS is required when WEBUI_TRUST_PROXY_HEADERS=true")
	}
	return nil
}

func parseTrustedProxyCIDRs(raw string) ([]netip.Prefix, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	prefixes := make([]netip.Prefix, 0, len(parts))
	for _, part := range parts {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(part))
		if err != nil || !prefix.IsValid() {
			return nil, errors.New("WEBUI_TRUSTED_PROXY_CIDRS must contain valid CIDRs")
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

func (c Config) LegacyTunnelEnvPresent() bool {
	return len(c.LegacyTunnelEnvVars) > 0
}

func legacyTunnelEnvVars() []string {
	keys := []string{
		"SERVER_HOST",
		"TUNNEL_NAME",
		"LISTEN_PORT",
		"IPV4_SUBNET",
		"DNS",
		"ALLOWED_IPS",
		"PERSISTENT_KEEPALIVE",
		"MTU",
		"PROTOCOL_PROFILE",
	}
	var present []string
	for _, key := range keys {
		if os.Getenv(key) != "" {
			present = append(present, key)
		}
	}
	return present
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func getenvInt64(key string, fallback int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func getenvBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return v == "1" || v == "true" || v == "TRUE" || v == "yes"
}

func getenvDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err == nil {
		return d
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return time.Duration(n) * time.Second
}
