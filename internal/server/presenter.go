package server

import (
	"sort"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/firewall"
)

func profileMeta(id, tab, label string, available bool) map[string]any {
	name, port, subnet := app.SuggestedTunnelSpec(id)
	return map[string]any{
		"id":               id,
		"tab":              tab,
		"label":            label,
		"available":        available,
		"suggested_name":   name,
		"suggested_port":   port,
		"suggested_subnet": subnet,
	}
}

func publicTunnel(tunnel config.Tunnel, status app.TunnelStatus) map[string]any {
	return publicTunnelWithFirewall(tunnel, status, firewallSummary{})
}

func publicTunnelWithFirewall(tunnel config.Tunnel, status app.TunnelStatus, fw firewallSummary) map[string]any {
	return map[string]any{
		"id":          tunnel.ID,
		"name":        tunnel.Name,
		"interface":   tunnel.InterfaceName,
		"enabled":     tunnel.Enabled,
		"listen_port": tunnel.ListenPort,
		"server_host": tunnel.ServerHost,
		"address":     tunnel.ServerAddress,
		"subnet":      tunnel.IPv4Subnet,
		"dns":         tunnel.DNS,
		"allowed_ips": tunnel.AllowedIPs,
		"keepalive":   tunnel.Keepalive,
		"mtu":         tunnel.MTU,
		"profile":     tunnel.ProtocolProfileID,
		"revision":    tunnel.ConfigRevision,
		"params":      orderedParams(tunnel.ProtocolProfileID, tunnel.ProtocolParams),
		"clients":     publicClients(tunnel),
		"status": map[string]any{
			"up":            status.Up,
			"apply_enabled": status.ApplyEnabled,
			"last_render":   status.LastRenderAt,
			"last_apply":    status.LastApplyAt,
			"last_error":    status.LastError,
			"firewall":      fw,
			"stale_clients": staleClientCount(tunnel),
		},
	}
}

type firewallSummary struct {
	Level   string `json:"level"`
	Label   string `json:"label"`
	Message string `json:"message,omitempty"`
}

func firewallSummaryForTunnel(tunnel config.Tunnel, report firewall.Report, err error) firewallSummary {
	if err != nil {
		return firewallSummary{Level: "warn", Label: "firewall unknown", Message: err.Error()}
	}
	if !report.ApplyEnabled {
		return firewallSummary{Level: "neutral", Label: "firewall manual", Message: "APPLY_CONFIG=false"}
	}
	if !tunnel.Enabled {
		return firewallSummary{Level: "neutral", Label: "firewall disabled"}
	}

	var matched, missing, duplicate, failed int
	for _, item := range report.Results {
		if item.Tunnel != tunnel.Name {
			continue
		}
		matched++
		switch item.Status {
		case "missing":
			missing++
		case "duplicate":
			duplicate++
		case "error":
			failed++
		}
	}

	switch {
	case failed > 0:
		return firewallSummary{Level: "bad", Label: "firewall error", Message: "managed firewall rules could not be checked"}
	case missing > 0:
		return firewallSummary{Level: "bad", Label: "firewall repair", Message: "managed firewall rules are missing"}
	case duplicate > 0:
		return firewallSummary{Level: "warn", Label: "firewall duplicates", Message: "managed firewall rules have duplicates"}
	case matched == 0:
		return firewallSummary{Level: "warn", Label: "firewall unknown", Message: "no managed firewall checks found"}
	default:
		return firewallSummary{Level: "ok", Label: "firewall ok"}
	}
}

func staleClientCount(tunnel config.Tunnel) int {
	if tunnel.ConfigRevision <= 0 {
		return 0
	}
	count := 0
	for _, client := range tunnel.Clients {
		if client.ConfigRevision < tunnel.ConfigRevision {
			count++
		}
	}
	return count
}

func publicClients(tunnel config.Tunnel) []map[string]any {
	out := make([]map[string]any, 0, len(tunnel.Clients))
	for _, client := range tunnel.Clients {
		out = append(out, publicClientForTunnel(tunnel, client))
	}
	return out
}

func publicClient(client config.Client) map[string]any {
	return publicClientForTunnel(config.Tunnel{}, client)
}

func publicClientForTunnel(tunnel config.Tunnel, client config.Client) map[string]any {
	return map[string]any{
		"id":               client.ID,
		"tunnel_id":        client.TunnelID,
		"name":             client.Name,
		"notes":            client.Notes,
		"enabled":          client.Enabled,
		"address":          client.IPv4Address,
		"revision":         client.ConfigRevision,
		"needs_new_config": tunnel.ConfigRevision > 0 && client.ConfigRevision < tunnel.ConfigRevision,
		"created_at":       client.CreatedAt,
		"updated_at":       client.UpdatedAt,
	}
}

func orderedParams(profileID string, params config.ProtocolParams) []map[string]string {
	keys := protocolParamKeys(profileID)
	out := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, map[string]string{"key": key, "value": params[key]})
	}
	return out
}

func protocolParamKeys(profileID string) []string {
	keys := []string{"Jc", "Jmin", "Jmax", "S1", "S2", "H1", "H2", "H3", "H4"}
	switch profileID {
	case "awg_1_5":
		keys = append(keys, "I1", "I2", "I3", "I4", "I5")
	case "awg_2_0":
		keys = append(keys, "S3", "S4", "I1", "I2", "I3", "I4", "I5")
	}
	sort.Strings(keys)
	return keys
}
