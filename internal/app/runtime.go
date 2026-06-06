package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/firewall"
)

func (s *Service) RestartTunnel() error {
	state, err := s.Init()
	if err != nil {
		return err
	}
	if len(state.Tunnels) == 0 {
		return errors.New("no tunnels configured")
	}
	return s.RestartTunnelByID(state.Tunnels[0].ID)
}

func (s *Service) RestartTunnelByID(tunnelID string) error {
	state, err := s.Init()
	if err != nil {
		return err
	}
	idx, ok := tunnelIndexByID(state, tunnelID)
	if !ok {
		return errors.New("tunnel not found")
	}
	if !s.cfg.ApplyConfig {
		state.Tunnels[idx].LastApplyError = "APPLY_CONFIG=false; tunnel restart skipped"
		state.Tunnels[idx].UpdatedAt = time.Now().UTC()
		state.UpdatedAt = state.Tunnels[idx].UpdatedAt
		if err := s.store.Save(state); err != nil {
			return err
		}
		s.log("warn", "tunnel.restart.skipped", "runtime tunnel restart skipped because APPLY_CONFIG=false", tunnelAuditFields(state.Tunnels[idx]), nil)
		return nil
	}
	_ = exec.Command("awg-quick", "down", state.Tunnels[idx].InterfaceName).Run()
	if err := s.RenderTunnel(tunnelID); err != nil {
		s.log("error", "tunnel.restart.failed", "runtime tunnel restart failed", tunnelAuditFields(state.Tunnels[idx]), err)
		return err
	}
	state, err = s.store.Load()
	if err != nil {
		return err
	}
	idx, ok = tunnelIndexByID(state, tunnelID)
	if !ok {
		return errors.New("tunnel not found")
	}
	if state.Tunnels[idx].LastApplyError != "" {
		err := errors.New(state.Tunnels[idx].LastApplyError)
		s.log("error", "tunnel.restart.failed", "runtime tunnel restart failed", tunnelAuditFields(state.Tunnels[idx]), err)
		return err
	}
	s.log("info", "tunnel.restarted", "runtime tunnel restarted", tunnelAuditFields(state.Tunnels[idx]), nil)
	return nil
}

func (s *Service) TunnelStatus() (TunnelStatus, error) {
	state, err := s.Init()
	if err != nil {
		return TunnelStatus{}, err
	}
	if len(state.Tunnels) == 0 {
		return TunnelStatus{}, errors.New("no tunnels configured")
	}
	return s.TunnelStatusByID(state.Tunnels[0].ID)
}

func (s *Service) TunnelStatusByID(tunnelID string) (TunnelStatus, error) {
	state, err := s.Init()
	if err != nil {
		return TunnelStatus{}, err
	}
	idx, ok := tunnelIndexByID(state, tunnelID)
	if !ok {
		return TunnelStatus{}, errors.New("tunnel not found")
	}
	tunnel := state.Tunnels[idx]
	return TunnelStatus{
		TunnelID:     tunnel.ID,
		ApplyEnabled: s.cfg.ApplyConfig,
		Up:           exec.Command("ip", "link", "show", tunnel.InterfaceName).Run() == nil,
		LastRenderAt: tunnel.LastRenderAt,
		LastApplyAt:  tunnel.LastApplyAt,
		LastError:    tunnel.LastApplyError,
	}, nil
}

func (s *Service) TunnelHealthByID(tunnelID string, sampleSeconds int) (TunnelHealth, error) {
	if sampleSeconds <= 0 {
		sampleSeconds = 2
	}
	if sampleSeconds > 10 {
		sampleSeconds = 10
	}
	state, err := s.Init()
	if err != nil {
		return TunnelHealth{}, err
	}
	idx, ok := tunnelIndexByID(state, tunnelID)
	if !ok {
		return TunnelHealth{}, errors.New("tunnel not found")
	}
	tunnel := state.Tunnels[idx]
	first, err := runtimeAWGShow(tunnel.InterfaceName)
	if err != nil {
		return TunnelHealth{}, err
	}
	time.Sleep(time.Duration(sampleSeconds) * time.Second)
	second, err := runtimeAWGShow(tunnel.InterfaceName)
	if err != nil {
		return TunnelHealth{}, err
	}
	health := TunnelHealth{
		TunnelID:      tunnel.ID,
		Name:          tunnel.Name,
		InterfaceName: tunnel.InterfaceName,
		SampleSeconds: sampleSeconds,
	}
	if !hasNATRule(tunnel.IPv4Subnet, s.cfg.ExternalInterface) {
		health.Warnings = append(health.Warnings, "possible NAT issue: missing MASQUERADE for "+tunnel.IPv4Subnet+" on "+s.cfg.ExternalInterface)
	}
	if !hasFilterRule("FORWARD", "-i", tunnel.InterfaceName, "-j", "ACCEPT") || !hasFilterRule("FORWARD", "-o", tunnel.InterfaceName, "-j", "ACCEPT") {
		health.Warnings = append(health.Warnings, "possible forwarding issue: missing FORWARD accept rules for "+tunnel.InterfaceName)
	}
	for _, client := range tunnel.Clients {
		item := ClientHealth{
			ID:      client.ID,
			Name:    client.Name,
			Enabled: client.Enabled,
			Address: client.IPv4Address,
			Status:  "disabled",
		}
		if !client.Enabled {
			health.Clients = append(health.Clients, item)
			continue
		}
		nextPeer, ok := second.Peers[client.PublicKey]
		if !ok {
			item.Status = "missing runtime peer"
			item.Warning = "enabled client is not present in awg runtime"
			health.Clients = append(health.Clients, item)
			continue
		}
		item.Present = true
		item.LatestHandshake = nextPeer.LatestHandshake
		item.RxBytes = nextPeer.RxBytes
		item.TxBytes = nextPeer.TxBytes
		if prevPeer, ok := first.Peers[client.PublicKey]; ok {
			item.RxDeltaBytes = byteDelta(prevPeer.RxBytes, nextPeer.RxBytes)
			item.TxDeltaBytes = byteDelta(prevPeer.TxBytes, nextPeer.TxBytes)
		}
		switch {
		case item.LatestHandshake == "":
			item.Status = "never connected"
			item.Warning = "no handshake yet"
		case item.RxDeltaBytes >= healthTrafficWarningThresholdBytes && item.TxDeltaBytes == 0:
			item.Status = "client sends traffic, server sends 0 bytes back"
			item.Warning = "possible NAT, forwarding, route, DNS, or upstream firewall issue"
		case item.RxDeltaBytes < healthTrafficWarningThresholdBytes && item.TxDeltaBytes == 0:
			item.Status = "idle, handshake ok"
		case item.RxDeltaBytes == 0 && item.TxDeltaBytes > 0:
			item.Status = "outbound only"
			item.Warning = "server sent traffic, but client traffic did not increase during sample window"
		default:
			item.Status = "traffic flowing"
		}
		health.Clients = append(health.Clients, item)
	}
	return health, nil
}

func (s *Service) FirewallCheck() (firewall.Report, error) {
	state, err := s.Init()
	if err != nil {
		return firewall.Report{}, err
	}
	return firewall.Check(s.cfg, state, firewall.IPTablesRunner{}), nil
}

func (s *Service) FirewallRepair() (firewall.Report, error) {
	state, err := s.Init()
	if err != nil {
		return firewall.Report{}, err
	}
	report, err := firewall.Repair(s.cfg, state, firewall.IPTablesRunner{})
	level := "info"
	event := "firewall.repaired"
	message := "managed firewall rules repaired"
	if err != nil {
		level = "error"
		event = "firewall.repair.failed"
		message = "managed firewall repair failed"
	}
	s.log(level, event, message, firewallReportFields(report), err)
	return report, err
}

func (s *Service) apply(tunnel config.Tunnel) error {
	serverPath := filepath.Join(s.cfg.ConfigDir, "tunnels", tunnel.InterfaceName, "server.conf")
	runtimePath := filepath.Join("/etc/amnezia/amneziawg", tunnel.InterfaceName+".conf")
	if err := copyRuntimeConfig(serverPath, runtimePath); err != nil {
		return err
	}
	if err := exec.Command("ip", "link", "show", tunnel.InterfaceName).Run(); err != nil {
		if err := runCommand("awg-quick", "up", tunnel.InterfaceName); err != nil {
			return err
		}
		return s.ensureFirewallRules(tunnel)
	}
	stripped, err := exec.Command("awg-quick", "strip", runtimePath).Output()
	if err != nil {
		return err
	}
	cmd := exec.Command("awg", "syncconf", tunnel.InterfaceName, "/dev/stdin")
	cmd.Stdin = strings.NewReader(string(stripped))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("awg syncconf failed: %s", strings.TrimSpace(string(out)))
	}
	return s.ensureFirewallRules(tunnel)
}

func (s *Service) ensureFirewallRules(tunnel config.Tunnel) error {
	report, err := firewall.Repair(s.cfg, config.State{Tunnels: []config.Tunnel{tunnel}}, firewall.IPTablesRunner{})
	if err != nil {
		s.log("error", "firewall.repair.failed", "managed firewall repair failed during apply", firewallReportFields(report), err)
	}
	return err
}

func firewallReportFields(report firewall.Report) map[string]any {
	fields := map[string]any{
		"apply_enabled": report.ApplyEnabled,
		"results":       len(report.Results),
	}
	missing := 0
	errorsCount := 0
	duplicates := 0
	for _, result := range report.Results {
		switch result.Status {
		case "missing":
			missing++
		case "error":
			errorsCount++
		case "duplicate":
			duplicates++
		}
	}
	fields["missing"] = missing
	fields["errors"] = errorsCount
	fields["duplicates"] = duplicates
	return fields
}

func (s *Service) cleanupFirewallRules(tunnel config.Tunnel) error {
	rules := []iptablesRule{
		{table: "nat", args: []string{"POSTROUTING", "-s", tunnel.IPv4Subnet, "-o", s.cfg.ExternalInterface, "-j", "MASQUERADE"}},
		{args: []string{"INPUT", "-p", "udp", "-m", "udp", "--dport", strconv.Itoa(tunnel.ListenPort), "-j", "ACCEPT"}},
		{args: []string{"FORWARD", "-i", tunnel.InterfaceName, "-j", "ACCEPT"}},
		{args: []string{"FORWARD", "-o", tunnel.InterfaceName, "-j", "ACCEPT"}},
	}
	var errs []string
	for _, rule := range rules {
		if err := deleteAllIPTablesRules(rule); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

type iptablesRule struct {
	table string
	args  []string
}

func deleteAllIPTablesRules(rule iptablesRule) error {
	for i := 0; i < 64; i++ {
		if !iptablesRuleExists(rule) {
			return nil
		}
		args := append([]string{}, iptablesTableArgs(rule.table)...)
		args = append(args, "-D")
		args = append(args, rule.args...)
		out, err := exec.Command("iptables", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("iptables %s failed: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
		}
	}
	return fmt.Errorf("iptables duplicate cleanup limit reached for %s", strings.Join(rule.args, " "))
}

func iptablesRuleExists(rule iptablesRule) bool {
	if err := iptablesCheck(rule); err != nil {
		return false
	}
	return true
}

func iptablesCheck(rule iptablesRule) error {
	args := append([]string{}, iptablesTableArgs(rule.table)...)
	args = append(args, "-C")
	args = append(args, rule.args...)
	return exec.Command("iptables", args...).Run()
}

func iptablesTableArgs(table string) []string {
	if table == "" {
		return nil
	}
	return []string{"-t", table}
}

type runtimeInterface struct {
	Peers map[string]runtimePeer
}

type runtimePeer struct {
	LatestHandshake string
	RxBytes         uint64
	TxBytes         uint64
}

func runtimeAWGShow(interfaceName string) (runtimeInterface, error) {
	out, err := exec.Command("awg", "show", interfaceName).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return runtimeInterface{}, fmt.Errorf("awg show %s failed: %s", interfaceName, msg)
	}
	return parseRuntimeAWGShow(string(out)), nil
}

func (s *Service) ClientRuntimeSnapshot(state config.State) map[string]map[string]ClientRuntimeStatus {
	out := map[string]map[string]ClientRuntimeStatus{}
	for _, tunnel := range state.Tunnels {
		clients := map[string]ClientRuntimeStatus{}
		if !tunnel.Enabled {
			out[tunnel.ID] = clients
			continue
		}
		show, err := runtimeAWGShow(tunnel.InterfaceName)
		if err != nil {
			out[tunnel.ID] = clients
			continue
		}
		for _, client := range tunnel.Clients {
			peer, ok := show.Peers[client.PublicKey]
			if !ok {
				continue
			}
			clients[client.ID] = ClientRuntimeStatus{
				Present:         true,
				LatestHandshake: peer.LatestHandshake,
				RxBytes:         peer.RxBytes,
				TxBytes:         peer.TxBytes,
			}
		}
		out[tunnel.ID] = clients
	}
	return out
}

func parseRuntimeAWGShow(out string) runtimeInterface {
	result := runtimeInterface{Peers: map[string]runtimePeer{}}
	var currentKey string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "peer: ") {
			currentKey = strings.TrimSpace(strings.TrimPrefix(line, "peer: "))
			result.Peers[currentKey] = runtimePeer{}
			continue
		}
		if currentKey == "" {
			continue
		}
		peer := result.Peers[currentKey]
		switch {
		case strings.HasPrefix(line, "latest handshake: "):
			peer.LatestHandshake = strings.TrimSpace(strings.TrimPrefix(line, "latest handshake: "))
		case transferRE.MatchString(line):
			match := transferRE.FindStringSubmatch(line)
			peer.RxBytes = parseByteQuantity(match[1])
			peer.TxBytes = parseByteQuantity(match[2])
		}
		result.Peers[currentKey] = peer
	}
	return result
}

func parseByteQuantity(value string) uint64 {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	unit := "B"
	if len(fields) > 1 {
		unit = strings.ToLower(fields[1])
	}
	multiplier := float64(1)
	switch unit {
	case "kib":
		multiplier = 1024
	case "mib":
		multiplier = 1024 * 1024
	case "gib":
		multiplier = 1024 * 1024 * 1024
	case "tib":
		multiplier = 1024 * 1024 * 1024 * 1024
	case "kb":
		multiplier = 1000
	case "mb":
		multiplier = 1000 * 1000
	case "gb":
		multiplier = 1000 * 1000 * 1000
	case "tb":
		multiplier = 1000 * 1000 * 1000 * 1000
	}
	if n <= 0 {
		return 0
	}
	return uint64(n * multiplier)
}

func byteDelta(before, after uint64) uint64 {
	if after < before {
		return 0
	}
	return after - before
}

func hasNATRule(subnet, externalInterface string) bool {
	return exec.Command("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", subnet, "-o", externalInterface, "-j", "MASQUERADE").Run() == nil
}

func hasFilterRule(chain string, args ...string) bool {
	cmdArgs := append([]string{"-C", chain}, args...)
	return exec.Command("iptables", cmdArgs...).Run() == nil
}

func runCommand(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s failed: %s", name, strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func copyRuntimeConfig(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0600)
}
