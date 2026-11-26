package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/firewall"
	"github.com/astronaut808/awg-forge/internal/warp"
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
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.initLocked()
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
	tunnel := state.Tunnels[idx]
	if err := s.runtimeOps.removeTunnel(tunnel); err != nil {
		restoreErr := s.renderTunnelLocked(tunnelID, true)
		restartErr := &ApplyError{Err: fmt.Errorf("remove tunnel runtime before restart: %w", err)}
		if restoreErr != nil {
			restartErr.Err = errors.Join(restartErr.Err, fmt.Errorf("restore tunnel runtime: %w", restoreErr))
		}
		s.log("error", "tunnel.restart.failed", "runtime tunnel restart failed", tunnelAuditFields(tunnel), restartErr)
		return restartErr
	}
	if err := s.renderTunnelLocked(tunnelID, true); err != nil {
		s.log("error", "tunnel.restart.failed", "runtime tunnel restart failed", tunnelAuditFields(tunnel), err)
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

// TunnelStatuses checks the requested tunnels against one current state read.
// A tunnel removed since the caller's snapshot has no status in the result.
func (s *Service) TunnelStatuses(tunnels []config.Tunnel) (map[string]TunnelStatus, error) {
	if len(tunnels) == 0 {
		return map[string]TunnelStatus{}, nil
	}
	state, err := s.Init()
	if err != nil {
		return nil, err
	}
	statuses := make(map[string]TunnelStatus, len(tunnels))
	for _, requested := range tunnels {
		idx, ok := tunnelIndexByID(state, requested.ID)
		if !ok {
			continue
		}
		tunnel := state.Tunnels[idx]
		statuses[requested.ID] = TunnelStatus{
			TunnelID:     tunnel.ID,
			ApplyEnabled: s.cfg.ApplyConfig,
			Up:           exec.Command("ip", "link", "show", tunnel.InterfaceName).Run() == nil,
			LastRenderAt: tunnel.LastRenderAt,
			LastApplyAt:  tunnel.LastApplyAt,
			LastError:    tunnel.LastApplyError,
		}
	}
	return statuses, nil
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
	firewallReport := firewall.Check(s.cfg, config.State{Tunnels: []config.Tunnel{tunnel}}, firewall.IPTablesRunner{})
	for _, result := range firewallReport.Results {
		if result.Status == "ok" {
			continue
		}
		health.Warnings = append(health.Warnings, "firewall "+result.Rule+": "+result.Status)
	}
	now := time.Now().UTC()
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
		if config.ClientExpired(client, now) {
			item.Status = "expired"
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
	unlock, err := lockRelayNetwork()
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.checkRelayPortReservation(tunnel.ListenPort); err != nil {
		return err
	}

	serverPath := filepath.Join(s.cfg.ConfigDir, "tunnels", tunnel.InterfaceName, "server.conf")
	runtimePath, err := runtimeConfigPath(tunnel.InterfaceName)
	if err != nil {
		return err
	}
	if err := s.migrateLegacyFirewallRules(tunnel, runtimePath); err != nil {
		return err
	}
	if err := copyRuntimeConfig(serverPath, runtimePath); err != nil {
		return err
	}
	if err := exec.Command("ip", "link", "show", tunnel.InterfaceName).Run(); err != nil {
		if err := runAWGQuickForTunnel(tunnel, "up", tunnel.InterfaceName); err != nil {
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
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("awg syncconf failed: %w", err)
	}
	return s.ensureFirewallRules(tunnel)
}

func (s *Service) removeTunnelRuntime(tunnel config.Tunnel) error {
	var removeErrors []error
	if exec.Command("ip", "link", "show", tunnel.InterfaceName).Run() == nil {
		if err := runAWGQuickForTunnel(tunnel, "down", tunnel.InterfaceName); err != nil {
			removeErrors = append(removeErrors, err)
		}
	}
	if err := s.cleanupFirewallRules(tunnel); err != nil {
		removeErrors = append(removeErrors, err)
	}
	runtimePath, err := runtimeConfigPath(tunnel.InterfaceName)
	if err != nil {
		removeErrors = append(removeErrors, err)
	} else if err := os.Remove(runtimePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		removeErrors = append(removeErrors, fmt.Errorf("remove runtime config: %w", err))
	}
	return errors.Join(removeErrors...)
}

func (s *Service) reconcileWarpRuntime(state config.State) error {
	commands := warpRuntimeCommands{
		interfaceExists: func(interfaceName string) bool {
			return exec.Command("ip", "link", "show", interfaceName).Run() == nil
		},
		verifyOwner: verifyWarpRuntimeOwner,
		down: func(configPath string) error {
			return runAWGQuick("down", configPath)
		},
		up: func(interfaceName string) error {
			// WARP CPS parameters are validated for the bundled userspace backend.
			return runAWGQuickUserspace("up", interfaceName)
		},
	}
	return reconcileWarpRuntime(state, runtimeConfigDir, commands)
}

type warpRuntimeCommands struct {
	interfaceExists func(string) bool
	verifyOwner     func(config.Warp) error
	down            func(string) error
	up              func(string) error
}

func reconcileWarpRuntime(state config.State, runtimeDir string, commands warpRuntimeCommands) error {
	routes := warp.RoutesForState(state)
	interfaceName := state.Warp.RuntimeInterface()
	if err := validateTunnelInterfaceName(interfaceName); err != nil {
		return fmt.Errorf("invalid WARP interface name: %w", err)
	}
	runtimePath := filepath.Join(runtimeDir, interfaceName+".conf")
	if len(routes) == 0 {
		if commands.interfaceExists(interfaceName) {
			_, err := stopWarpRuntime(state, runtimePath, commands)
			return err
		}
		return nil
	}
	if !state.Warp.Configured() {
		return errors.New("WARP egress is enabled but WARP config is not imported")
	}
	conf, err := warp.RenderConfig(state.Warp, routes)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		return err
	}
	// Prepare the complete replacement before stopping the working interface.
	stagedPath, err := stageRuntimeConfig(runtimePath, []byte(conf))
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(stagedPath) }()

	var canRestore bool
	if commands.interfaceExists(interfaceName) {
		canRestore, err = stopWarpRuntime(state, runtimePath, commands)
		if err != nil {
			return err
		}
	}
	if err := os.Rename(stagedPath, runtimePath); err != nil {
		var restoreErr error
		if canRestore {
			if err := commands.up(interfaceName); err != nil {
				restoreErr = fmt.Errorf("restore previous WARP runtime: %w", err)
			}
		}
		return errors.Join(fmt.Errorf("replace WARP runtime config: %w", err), restoreErr)
	}
	if err := commands.up(interfaceName); err != nil {
		return fmt.Errorf("start WARP runtime: %w", err)
	}
	return nil
}

// stopWarpRuntime reports whether the original config is available for restart.
func stopWarpRuntime(state config.State, runtimePath string, commands warpRuntimeCommands) (bool, error) {
	_, err := os.Lstat(runtimePath)
	if err == nil {
		if err := commands.down(runtimePath); err != nil {
			return false, fmt.Errorf("stop WARP runtime: %w", err)
		}
		return true, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect WARP runtime config: %w", err)
	}
	if err := commands.verifyOwner(state.Warp); err != nil {
		return false, fmt.Errorf("recover missing WARP runtime config: %w", err)
	}

	// An interrupted change can leave rules for a tunnel now using WAN or disabled.
	// Scope cleanup to the recorded tunnel subnets and their routes in the WARP table.
	routes := make([]warp.TunnelRoute, 0, len(state.Tunnels))
	for _, tunnel := range state.Tunnels {
		if err := validateTunnelInterfaceName(tunnel.InterfaceName); err != nil {
			return false, fmt.Errorf("recover WARP route: %w", err)
		}
		routes = append(routes, warp.TunnelRoute{InterfaceName: tunnel.InterfaceName, Subnet: tunnel.IPv4Subnet})
	}
	conf, err := warp.RenderTeardownConfig(routes)
	if err != nil {
		return false, err
	}
	runtimeDir := filepath.Dir(runtimePath)
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		return false, err
	}
	dir, err := os.MkdirTemp(runtimeDir, ".warp-teardown-*")
	if err != nil {
		return false, err
	}
	recoveredPath := filepath.Join(dir, filepath.Base(runtimePath))
	defer func() {
		_ = os.Remove(recoveredPath)
		_ = os.Remove(dir)
	}()
	if err := os.WriteFile(recoveredPath, []byte(conf), 0600); err != nil {
		return false, err
	}
	if err := commands.down(recoveredPath); err != nil {
		return false, fmt.Errorf("stop WARP runtime with recovered config: %w", err)
	}
	return false, nil
}

func verifyWarpRuntimeOwner(w config.Warp) error {
	pubkey := exec.Command("awg", "pubkey")
	key, _, _ := strings.Cut(w.PrivateKey, "#")
	pubkey.Stdin = strings.NewReader(strings.TrimSpace(key) + "\n")
	expected, err := pubkey.Output()
	if err != nil {
		return fmt.Errorf("derive WARP public key: %w", err)
	}
	actual, err := exec.Command("awg", "show", w.RuntimeInterface(), "public-key").Output()
	if err != nil {
		return fmt.Errorf("read live WARP public key: %w", err)
	}
	if strings.TrimSpace(string(actual)) != strings.TrimSpace(string(expected)) {
		return errors.New("live interface does not match the stored WARP public key")
	}
	return nil
}

func stageRuntimeConfig(path string, contents []byte) (string, error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".warp-runtime-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	ok := false
	closed := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return "", err
	}
	if _, err := tmp.Write(contents); err != nil {
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	closed = true
	ok = true
	return tmpPath, nil
}

func warpRuntimeRequired(state config.State) bool {
	return state.Warp.Configured() || len(warp.RoutesForState(state)) > 0
}

func warpRoutesChanged(previous, current config.State) bool {
	return !slices.Equal(warp.RoutesForState(previous), warp.RoutesForState(current))
}

func (s *Service) reconcileWarpRuntimePersisted() error {
	state, err := s.store.Load()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	applyErr := s.reconcileWarpRuntimeStatus(&state, now)
	if saveErr := s.store.Save(state); saveErr != nil {
		if applyErr != nil {
			return errors.Join(applyErr, fmt.Errorf("save WARP apply status: %w", saveErr))
		}
		return saveErr
	}
	return applyErr
}

func (s *Service) reconcileWarpRuntimeStatus(state *config.State, now time.Time) error {
	err := s.runtimeOps.reconcileWarp(*state)
	recordWarpApplyResult(state, now, err)
	return err
}

func recordWarpApplyResult(state *config.State, now time.Time, err error) {
	state.Warp.UpdatedAt = now
	state.UpdatedAt = now
	if err != nil {
		state.Warp.LastApplyError = err.Error()
		return
	}
	state.Warp.LastApplyAt = now
	state.Warp.LastApplyError = ""
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
	return firewall.RemoveRulesForTunnel(s.cfg, tunnel, firewall.IPTablesRunner{})
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
	out, err := exec.Command("awg", "show", interfaceName).Output()
	if err != nil {
		return runtimeInterface{}, fmt.Errorf("awg show %s failed: %w", interfaceName, err)
	}
	return parseRuntimeAWGShow(string(out)), nil
}

var handshakeAgePartRE = regexp.MustCompile(`(?i)(\d+)\s+(day|hour|minute|second)s?`)

func (s *Service) ClientRuntimeSnapshot(state config.State) (config.State, map[string]map[string]ClientRuntimeStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if latest, err := s.initLocked(); err == nil {
		state = latest
	}
	out := map[string]map[string]ClientRuntimeStatus{}
	changed := false
	now := time.Now().UTC()
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
		ti, ok := tunnelIndexByID(state, tunnel.ID)
		if !ok {
			out[tunnel.ID] = clients
			continue
		}
		for ci, client := range state.Tunnels[ti].Clients {
			peer, ok := show.Peers[client.PublicKey]
			if !ok {
				continue
			}
			seenAt := handshakeSeenAt(now, peer.LatestHandshake)
			clients[client.ID] = ClientRuntimeStatus{
				Present:         true,
				LatestHandshake: peer.LatestHandshake,
				LastSeenAt:      seenAt,
				RxBytes:         peer.RxBytes,
				TxBytes:         peer.TxBytes,
			}
			if !seenAt.IsZero() && shouldUpdateClientLastSeen(client, seenAt) {
				state.Tunnels[ti].Clients[ci].EverConnected = true
				state.Tunnels[ti].Clients[ci].LastSeenAt = seenAt
				changed = true
			}
		}
		out[tunnel.ID] = clients
	}
	if changed {
		state.UpdatedAt = now
		if err := s.store.Save(state); err != nil {
			s.log("warn", "client.last_seen.persist_failed", "client last seen update failed", nil, err)
		}
	}
	return state, out
}

func shouldUpdateClientLastSeen(client config.Client, seenAt time.Time) bool {
	if !client.EverConnected || client.LastSeenAt.IsZero() {
		return true
	}
	return seenAt.After(client.LastSeenAt.Add(30 * time.Second))
}

func handshakeSeenAt(now time.Time, latest string) time.Time {
	age, ok := parseHandshakeAge(latest)
	if !ok {
		return time.Time{}
	}
	return now.Add(-age).UTC()
}

func parseHandshakeAge(latest string) (time.Duration, bool) {
	matches := handshakeAgePartRE.FindAllStringSubmatch(latest, -1)
	if len(matches) == 0 {
		return 0, false
	}
	var total time.Duration
	for _, match := range matches {
		value, err := strconv.Atoi(match[1])
		if err != nil {
			return 0, false
		}
		switch strings.ToLower(match[2]) {
		case "day":
			total += time.Duration(value) * 24 * time.Hour
		case "hour":
			total += time.Duration(value) * time.Hour
		case "minute":
			total += time.Duration(value) * time.Minute
		case "second":
			total += time.Duration(value) * time.Second
		}
	}
	return total, true
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

func runAWGQuick(args ...string) error {
	return runAWGQuickWithEnv(nil, args...)
}

func runAWGQuickForTunnel(tunnel config.Tunnel, args ...string) error {
	if !isAWG3Profile(tunnel.ProtocolProfileID) {
		return runAWGQuick(args...)
	}
	return runAWGQuickUserspace(args...)
}

func runAWGQuickUserspace(args ...string) error {
	return runAWGQuickWithEnv([]string{"AWG_QUICK_FORCE_USERSPACE=1"}, args...)
}

func runAWGQuickWithEnv(extraEnv []string, args ...string) error {
	cmd := exec.Command("awg-quick", args...)
	if len(extraEnv) > 0 {
		cmd.Env = mergedEnv(os.Environ(), extraEnv)
	}
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("awg-quick %s failed: %w", strings.Join(args, " "), err)
	}
	return nil
}

func mergedEnv(base, overrides []string) []string {
	result := append([]string(nil), base...)
	for _, override := range overrides {
		key, _, ok := strings.Cut(override, "=")
		if !ok {
			continue
		}
		prefix := key + "="
		replaced := false
		for idx, value := range result {
			if strings.HasPrefix(value, prefix) {
				result[idx] = override
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, override)
		}
	}
	return result
}

func runtimeConfigHasLegacyFirewallRules(path string, tunnel config.Tunnel) (bool, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	text := string(contents)
	return strings.Contains(text, "iptables -C FORWARD -i "+tunnel.InterfaceName+" -j ACCEPT") &&
		strings.Contains(text, "iptables -C FORWARD -o "+tunnel.InterfaceName+" -j ACCEPT"), nil
}

func (s *Service) migrateLegacyFirewallRules(tunnel config.Tunnel, runtimePath string) error {
	legacyConfig, err := runtimeConfigHasLegacyFirewallRules(runtimePath, tunnel)
	if err != nil {
		return err
	}
	legacyRules, err := firewall.LegacyRulesPresent(s.cfg, tunnel, firewall.IPTablesRunner{})
	if err != nil {
		return err
	}
	if !legacyConfig && !legacyRules {
		return nil
	}
	report, err := firewall.MigrateLegacyRules(s.cfg, tunnel, firewall.IPTablesRunner{})
	if err != nil {
		s.log("error", "firewall.legacy_migration.failed", "legacy tunnel firewall migration failed", firewallReportFields(report), err)
		return err
	}
	if legacyConfig {
		contents, err := os.ReadFile(runtimePath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(runtimePath, []byte(withoutLegacyFirewallDirectives(string(contents), tunnel)), 0600); err != nil {
			return err
		}
	}
	fields := tunnelAuditFields(tunnel)
	fields["legacy_runtime_config"] = legacyConfig
	fields["legacy_host_rules"] = legacyRules
	s.log("info", "firewall.legacy_rules.migrated", "legacy tunnel firewall rules migrated", fields, nil)
	return nil
}

func withoutLegacyFirewallDirectives(contents string, tunnel config.Tunnel) string {
	lines := strings.Split(contents, "\n")
	filtered := lines[:0]
	for _, line := range lines {
		if (strings.HasPrefix(line, "PostUp = ") && strings.Contains(line, "iptables -C FORWARD -i "+tunnel.InterfaceName+" -j ACCEPT")) ||
			(strings.HasPrefix(line, "PostDown = ") && strings.Contains(line, "while iptables -C FORWARD -i "+tunnel.InterfaceName+" -j ACCEPT")) {
			continue
		}
		filtered = append(filtered, line)
	}
	return strings.Join(filtered, "\n")
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
