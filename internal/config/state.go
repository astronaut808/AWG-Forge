package config

import "time"

type State struct {
	SchemaVersion     int       `json:"schema_version"`
	SessionSecret     string    `json:"session_secret"`
	ServerHost        string    `json:"server_host"`
	ExternalInterface string    `json:"external_interface"`
	Warp              Warp      `json:"warp,omitempty"`
	Tunnels           []Tunnel  `json:"tunnels"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type ProtocolParams map[string]string

const (
	EgressWAN  = "wan"
	EgressWarp = "warp"
)

type Warp struct {
	InterfaceName       string    `json:"interface_name,omitempty"`
	DeviceID            string    `json:"device_id,omitempty"`
	AccessToken         string    `json:"access_token,omitempty"`
	LicenseKey          string    `json:"license_key,omitempty"`
	ClientID            string    `json:"client_id,omitempty"`
	PrivateKey          string    `json:"private_key,omitempty"`
	PeerPublicKey       string    `json:"peer_public_key,omitempty"`
	PresharedKey        string    `json:"preshared_key,omitempty"`
	Endpoint            string    `json:"endpoint,omitempty"`
	AddressV4           string    `json:"address_v4,omitempty"`
	AddressV6           string    `json:"address_v6,omitempty"`
	MTU                 int       `json:"mtu,omitempty"`
	PersistentKeepalive int       `json:"persistent_keepalive,omitempty"`
	RegisteredAt        time.Time `json:"registered_at,omitempty"`
	LastApplyAt         time.Time `json:"last_apply_at,omitempty"`
	LastApplyError      string    `json:"last_apply_error,omitempty"`
	UpdatedAt           time.Time `json:"updated_at,omitempty"`
}

func (w Warp) RuntimeInterface() string {
	if w.InterfaceName == "" {
		return "warp0"
	}
	return w.InterfaceName
}

func (w Warp) Configured() bool {
	return w.PrivateKey != "" && w.PeerPublicKey != "" && w.Endpoint != "" && w.AddressV4 != ""
}

func (w Warp) Registered() bool {
	return w.DeviceID != "" && w.AccessToken != ""
}

type Tunnel struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	InterfaceName     string         `json:"interface_name"`
	EgressMode        string         `json:"egress_mode,omitempty"`
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
	Notes          string    `json:"notes,omitempty"`
	Enabled        bool      `json:"enabled"`
	IPv4Address    string    `json:"ipv4_address"`
	PrivateKey     string    `json:"private_key"`
	PublicKey      string    `json:"public_key"`
	PresharedKey   string    `json:"preshared_key"`
	ConfigRevision int       `json:"config_revision"`
	EverConnected  bool      `json:"ever_connected,omitempty"`
	LastSeenAt     time.Time `json:"last_seen_at,omitempty"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func ClientExpired(client Client, now time.Time) bool {
	return !client.ExpiresAt.IsZero() && !client.ExpiresAt.After(now)
}

func ClientActive(client Client, now time.Time) bool {
	return client.Enabled && !ClientExpired(client, now)
}
