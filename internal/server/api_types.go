package server

import (
	"encoding/json"

	"github.com/astronaut808/awg-forge/internal/config"
)

type warpImportRequest struct {
	Config string `json:"config"`
}

type loginRequest struct {
	Password string `json:"password"`
}

type backupRequest struct {
	Password string `json:"password"`
}

type createTunnelRequest struct {
	Profile    string `json:"profile"`
	Name       string `json:"name"`
	EgressMode string `json:"egress_mode"`
	Port       int    `json:"port"`
	AutoPort   bool   `json:"automatic_port"`
	Subnet     string `json:"subnet"`
}

type updateTunnelSettingsRequest struct {
	Name       string `json:"name"`
	ServerHost string `json:"server_host"`
	EgressMode string `json:"egress_mode"`
	Port       int    `json:"port"`
	Subnet     string `json:"subnet"`
	DNS        string `json:"dns"`
	AllowedIPs string `json:"allowed_ips"`
	Keepalive  int    `json:"keepalive"`
	MTU        int    `json:"mtu"`
	Enabled    bool   `json:"enabled"`
}

type deleteTunnelRequest struct {
	ConfirmationName string `json:"confirmation_name"`
}

type updateProtocolRequest struct {
	Profile string                `json:"profile"`
	Params  config.ProtocolParams `json:"params"`
}

type regenerateProtocolRequest struct {
	Profile string `json:"profile"`
}

type createClientRequest struct {
	TunnelID           string          `json:"tunnel_id"`
	Name               string          `json:"name"`
	ExpiresAt          string          `json:"expires_at"`
	TrafficLimitBytes  json.RawMessage `json:"traffic_limit_bytes"`
	TrafficLimitPeriod string          `json:"traffic_limit_period"`
}

type updateClientSettingsRequest struct {
	Name      string `json:"name"`
	Notes     string `json:"notes"`
	ExpiresAt string `json:"expires_at"`
}

type updateClientTrafficLimitRequest struct {
	LimitBytes  json.RawMessage `json:"limit_bytes"`
	LimitPeriod string          `json:"limit_period"`
}
