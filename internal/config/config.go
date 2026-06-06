package config

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
)

const (
	DefaultConfigDir = "/etc/awg-forge"
	DefaultTunnel    = "awg0"
)

type Config struct {
	ConfigDir           string
	TunnelName          string
	ServerHost          string
	ListenPort          int
	WebUIHost           string
	WebUIPort           int
	Password            string
	SessionSecret       string
	SessionCookieSecure string
	ExternalInterface   string
	IPv4Subnet          string
	DNS                 string
	AllowedIPs          string
	PersistentKeepalive int
	MTU                 int
	ProtocolProfile     string
	ApplyConfig         bool
	PublishedUDPPorts   string
	AuditLogEnabled     bool
	AuditLogPath        string
	AuditLogMaxSize     int64
	AuditLogMaxFiles    int
}

func FromEnv() (Config, error) {
	configDir := getenv("CONFIG_DIR", DefaultConfigDir)
	cfg := Config{
		ConfigDir:           configDir,
		TunnelName:          getenv("TUNNEL_NAME", DefaultTunnel),
		ServerHost:          getenv("SERVER_HOST", "127.0.0.1"),
		ListenPort:          getenvInt("LISTEN_PORT", 51820),
		WebUIHost:           getenv("WEBUI_HOST", "127.0.0.1"),
		WebUIPort:           getenvInt("WEBUI_PORT", 51821),
		Password:            os.Getenv("PASSWORD"),
		SessionSecret:       os.Getenv("SESSION_SECRET"),
		SessionCookieSecure: getenv("SESSION_COOKIE_SECURE", "auto"),
		ExternalInterface:   getenv("EXTERNAL_INTERFACE", "eth0"),
		IPv4Subnet:          getenv("IPV4_SUBNET", "10.8.0.0/24"),
		DNS:                 getenv("DNS", "1.1.1.1"),
		AllowedIPs:          getenv("ALLOWED_IPS", "0.0.0.0/0"),
		PersistentKeepalive: getenvInt("PERSISTENT_KEEPALIVE", 0),
		MTU:                 getenvInt("MTU", 0),
		ProtocolProfile:     getenv("PROTOCOL_PROFILE", "awg_legacy_1_0"),
		ApplyConfig:         getenvBool("APPLY_CONFIG", false),
		PublishedUDPPorts:   os.Getenv("PUBLISHED_UDP_PORTS"),
		AuditLogEnabled:     getenvBool("AUDIT_LOG_ENABLED", true),
		AuditLogPath:        getenv("AUDIT_LOG_PATH", filepath.Join(configDir, "audit.log")),
		AuditLogMaxSize:     getenvInt64("AUDIT_LOG_MAX_SIZE", 5*1024*1024),
		AuditLogMaxFiles:    getenvInt("AUDIT_LOG_MAX_FILES", 3),
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
	if _, _, err := net.ParseCIDR(cfg.IPv4Subnet); err != nil {
		return Config{}, err
	}
	return cfg, nil
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
