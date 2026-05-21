package config

import "time"

type State struct {
	SchemaVersion     int       `json:"schema_version"`
	SessionSecret     string    `json:"session_secret"`
	ServerHost        string    `json:"server_host"`
	ExternalInterface string    `json:"external_interface"`
	Tunnels           []Tunnel  `json:"tunnels"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type ProtocolParams map[string]string

type Tunnel struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	InterfaceName     string         `json:"interface_name"`
	Enabled           bool           `json:"enabled"`
	ListenPort        int            `json:"listen_port"`
	ServerHost        string         `json:"server_host,omitempty"`
	ServerAddress     string         `json:"server_address"`
	IPv4Subnet        string         `json:"ipv4_subnet"`
	DNS               string         `json:"dns"`
	AllowedIPs        string         `json:"allowed_ips"`
	Keepalive         int            `json:"persistent_keepalive"`
	MTU               int            `json:"mtu"`
	ServerPrivateKey  string         `json:"server_private_key"`
	ServerPublicKey   string         `json:"server_public_key"`
	ProtocolProfileID string         `json:"protocol_profile_id"`
	ProtocolParams    ProtocolParams `json:"protocol_params"`
	ConfigRevision    int            `json:"config_revision"`
	Clients           []Client       `json:"clients"`
	LastRenderAt      time.Time      `json:"last_render_at,omitempty"`
	LastApplyAt       time.Time      `json:"last_apply_at,omitempty"`
	LastApplyError    string         `json:"last_apply_error,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

type Client struct {
	ID             string    `json:"id"`
	TunnelID       string    `json:"tunnel_id"`
	Name           string    `json:"name"`
	Enabled        bool      `json:"enabled"`
	IPv4Address    string    `json:"ipv4_address"`
	PrivateKey     string    `json:"private_key"`
	PublicKey      string    `json:"public_key"`
	PresharedKey   string    `json:"preshared_key"`
	ConfigRevision int       `json:"config_revision"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
