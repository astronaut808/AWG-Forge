package app

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/keys"
	"github.com/astronaut808/awg-forge/internal/protocol"
)

// The temporary peer has no AllowedIPs on either side and the interface has
// no addresses or routes. An authenticated keepalive triggers the handshake.
func checkRelayHandshake(parent context.Context, tunnel config.Tunnel, relay config.Relay) string {
	ctx, cancel := context.WithTimeout(parent, relayProbeTimeout)
	defer cancel()

	var nameSuffix [6]byte
	if _, err := rand.Read(nameSuffix[:]); err != nil {
		return "unavailable"
	}
	name := fmt.Sprintf("awrp%x", nameSuffix)[:15]
	host, err := relayProbeIPv4(ctx, relay.Host)
	if err != nil {
		return "unreachable"
	}
	route, err := exec.CommandContext(ctx, "ip", "-4", "route", "get", host).Output()
	if err != nil {
		return "unreachable"
	}
	if strings.HasPrefix(string(route), "local ") {
		return "unavailable"
	}

	private, public, err := keys.PrivateKey()
	if err != nil {
		return "unavailable"
	}
	profile, ok := protocol.ByID(tunnel.ProtocolProfileID)
	if !ok {
		return "unavailable"
	}
	lines, err := profile.RenderClientInterface(protocol.RenderContext{Tunnel: tunnel}, config.Client{PrivateKey: private})
	if err != nil {
		return "unavailable"
	}
	var conf strings.Builder
	conf.WriteString("[Interface]\n")
	for _, line := range lines {
		switch line.Key {
		case "Address", "DNS", "MTU":
			continue
		}
		fmt.Fprintf(&conf, "%s = %s\n", line.Key, line.Value)
	}
	fmt.Fprintf(&conf, "[Peer]\nPublicKey = %s\nEndpoint = %s:%d\n", tunnel.ServerPublicKey, host, relay.Port)

	cmd := exec.CommandContext(ctx, "amneziawg-go", "-f", name)
	cmd.Env = append(cmd.Environ(), "GOMAXPROCS=2")
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return "unavailable"
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for exec.CommandContext(ctx, "awg", "show", name, "public-key").Run() != nil {
		select {
		case <-ctx.Done():
			return "unavailable"
		case <-done:
			return "unavailable"
		case <-ticker.C:
		}
	}
	if err := exec.CommandContext(ctx, "ip", "link", "set", "dev", name, "addrgenmode", "none").Run(); err != nil {
		return "unavailable"
	}
	set := exec.CommandContext(ctx, "awg", "setconf", name, "/dev/stdin")
	set.Stdin = strings.NewReader(conf.String())
	if err := set.Run(); err != nil {
		return "unavailable"
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(parent), 2*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanup, "awg", "set", tunnel.InterfaceName, "peer", public, "remove").Run()
	}()
	if err := exec.CommandContext(ctx, "awg", "set", tunnel.InterfaceName, "peer", public).Run(); err != nil {
		return "unavailable"
	}
	if err := exec.CommandContext(ctx, "ip", "link", "set", "dev", name, "up").Run(); err != nil {
		return "unavailable"
	}
	// Wait for userspace to open its UDP socket before triggering keepalive.
	for {
		out, err := exec.CommandContext(ctx, "awg", "show", name, "listen-port").Output()
		if port := strings.TrimSpace(string(out)); err == nil && port != "" && port != "0" {
			break
		}
		select {
		case <-ctx.Done():
			return "unavailable"
		case <-done:
			return "unavailable"
		case <-ticker.C:
		}
	}
	if err := exec.CommandContext(ctx, "awg", "set", name, "peer", tunnel.ServerPublicKey, "persistent-keepalive", "1").Run(); err != nil {
		return "unavailable"
	}
	for {
		out, err := exec.CommandContext(ctx, "awg", "show", name, "latest-handshakes").Output()
		fields := strings.Fields(string(out))
		if err == nil && len(fields) == 2 && fields[0] == tunnel.ServerPublicKey && fields[1] != "0" {
			return "reachable"
		}
		select {
		case <-ctx.Done():
			// A concurrent syncconf can remove the temporary peer while the
			// probe is waiting. In that case there is no result to publish.
			verify, stop := context.WithTimeout(parent, time.Second)
			defer stop()
			return relayTimeoutStatus(verify, tunnel.InterfaceName, public)
		case <-done:
			return "unavailable"
		case <-ticker.C:
		}
	}
}

func relayTimeoutStatus(ctx context.Context, interfaceName, publicKey string) string {
	peers, err := exec.CommandContext(ctx, "awg", "show", interfaceName, "peers").Output()
	if err != nil || !slices.Contains(strings.Fields(string(peers)), publicKey) {
		return relayProbeInterrupted
	}
	return "unreachable"
}

func relayProbeIPv4(ctx context.Context, host string) (string, error) {
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
	if err != nil {
		return "", err
	}
	for _, address := range addresses {
		address = address.Unmap()
		if config.IsRelayIPv4(address) {
			return address.String(), nil
		}
	}
	return "", fmt.Errorf("relay hostname has no usable IPv4 address")
}
