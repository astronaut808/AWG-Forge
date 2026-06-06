package render

import (
	"bytes"
	"fmt"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/protocol"
)

func ServerConfig(state config.State, tunnel config.Tunnel) (string, error) {
	p, ok := protocol.ByID(tunnel.ProtocolProfileID)
	if !ok {
		return "", fmt.Errorf("unsupported protocol profile %q", tunnel.ProtocolProfileID)
	}
	ctx := protocol.RenderContext{State: state, Tunnel: tunnel}
	iface, err := p.RenderServerInterface(ctx)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	writeSection(&b, "Interface", iface)
	now := time.Now().UTC()
	for _, c := range tunnel.Clients {
		if !config.ClientActive(c, now) {
			continue
		}
		peer, err := p.RenderServerPeer(ctx, c)
		if err != nil {
			return "", err
		}
		b.WriteByte('\n')
		writeSection(&b, "Peer", peer)
	}
	return b.String(), nil
}

func ClientConfig(state config.State, tunnel config.Tunnel, client config.Client) (string, error) {
	p, ok := protocol.ByID(tunnel.ProtocolProfileID)
	if !ok {
		return "", fmt.Errorf("unsupported protocol profile %q", tunnel.ProtocolProfileID)
	}
	ctx := protocol.RenderContext{State: state, Tunnel: tunnel}
	iface, err := p.RenderClientInterface(ctx, client)
	if err != nil {
		return "", err
	}
	peer, err := p.RenderClientPeer(ctx, client)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	writeSection(&b, "Interface", iface)
	b.WriteByte('\n')
	writeSection(&b, "Peer", peer)
	return b.String(), nil
}

func writeSection(b *bytes.Buffer, name string, lines []protocol.ConfigLine) {
	fmt.Fprintf(b, "[%s]\n", name)
	for _, line := range lines {
		fmt.Fprintf(b, "%s = %s\n", line.Key, line.Value)
	}
}
