package doctor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/sqldb"
)

func (c *checker) checkTrafficLimits(cfg config.Config, state config.State) {
	if cfg.DatabaseMode != sqldb.ModeSQLite {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.DatabaseQueryTimeout)
	defer cancel()
	exceeded, err := sqldb.ListExceededTrafficLimits(ctx, cfg, time.Now().UTC())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return
		}
		c.warn(categoryDatabase, "traffic limits", "traffic limit diagnostics unavailable: "+err.Error())
		return
	}
	if len(exceeded) == 0 {
		return
	}
	clients := trafficLimitClientIndex(state)
	for _, item := range exceeded {
		tunnelName, clientName, enabled := trafficLimitClientLabel(clients, item)
		area := "traffic limit " + tunnelName + "/" + clientName
		detail := fmt.Sprintf("total=%d bytes limit=%d bytes", item.TotalBytes, item.LimitBytes)
		if enabled {
			c.warn(categoryClients, area, "enabled client is over traffic limit; enforcement should disable it; "+detail)
			continue
		}
		c.warn(categoryClients, area, "traffic limit exceeded; increase or clear the limit before enabling; "+detail)
	}
}

type trafficLimitClientInfo struct {
	tunnelName string
	clientName string
	enabled    bool
}

func trafficLimitClientIndex(state config.State) map[string]trafficLimitClientInfo {
	out := make(map[string]trafficLimitClientInfo)
	for _, tunnel := range state.Tunnels {
		for _, client := range tunnel.Clients {
			out[tunnel.ID+"\x00"+client.ID] = trafficLimitClientInfo{
				tunnelName: tunnel.Name,
				clientName: client.Name,
				enabled:    client.Enabled,
			}
		}
	}
	return out
}

func trafficLimitClientLabel(clients map[string]trafficLimitClientInfo, item sqldb.ExceededTrafficLimit) (string, string, bool) {
	if client, ok := clients[item.TunnelID+"\x00"+item.ClientID]; ok {
		return client.tunnelName, client.clientName, client.enabled
	}
	return item.TunnelID, item.ClientID, false
}
