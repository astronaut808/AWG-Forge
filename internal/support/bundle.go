package support

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/audit"
	"github.com/astronaut808/awg-forge/internal/buildinfo"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/doctor"
)

type Bundle struct {
	Name string
	Data []byte
}

type Options struct {
	Now time.Time
}

func Generate(ctx context.Context, cfg config.Config, service *app.Service, opts Options) (Bundle, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	state, err := service.Init()
	if err != nil {
		return Bundle{}, err
	}
	name := fmt.Sprintf("awg-forge-support-%s.zip", now.Format("20060102-150405"))
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := addJSON(zw, "manifest.json", manifest(now)); err != nil {
		return Bundle{}, err
	}
	if err := addJSON(zw, "config.redacted.json", redactedConfig(cfg)); err != nil {
		return Bundle{}, err
	}
	if err := addJSON(zw, "state.redacted.json", redactedState(state)); err != nil {
		return Bundle{}, err
	}
	doctorResults := doctor.Check(cfg, service)
	if err := addJSON(zw, "doctor.json", doctorResults); err != nil {
		return Bundle{}, err
	}
	if err := addText(zw, "doctor.txt", doctorText(doctorResults)); err != nil {
		return Bundle{}, err
	}
	if err := addJSON(zw, "files.json", fileInventory(cfg.ConfigDir)); err != nil {
		return Bundle{}, err
	}
	if err := addAuditLog(zw, cfg); err != nil {
		return Bundle{}, err
	}
	for _, cmd := range runtimeCommands(cfg, state) {
		if err := addText(zw, filepath.Join("runtime", cmd.Name+".txt"), runText(ctx, cmd.Args...)); err != nil {
			return Bundle{}, err
		}
	}
	if err := zw.Close(); err != nil {
		return Bundle{}, err
	}
	return Bundle{Name: name, Data: buf.Bytes()}, nil
}

func WriteFile(ctx context.Context, cfg config.Config, service *app.Service, path string) (string, error) {
	bundle, err := Generate(ctx, cfg, service, Options{})
	if err != nil {
		return "", err
	}
	if path == "" {
		path = bundle.Name
	}
	if err := os.WriteFile(path, bundle.Data, 0600); err != nil {
		return "", err
	}
	return path, nil
}

func manifest(now time.Time) map[string]any {
	return map[string]any{
		"generated_at": now.Format(time.RFC3339),
		"build":        buildinfo.Current(),
		"redaction": []string{
			"private keys removed",
			"preshared keys removed",
			"session/password values removed",
			"WARP private key and preshared key removed",
			"protocol parameter values removed",
			"audit log secret-looking fields redacted",
			"runtime public keys replaced with fingerprints",
			"rendered config contents excluded",
		},
	}
}

func addAuditLog(zw *zip.Writer, cfg config.Config) error {
	events, err := audit.ReadFile(cfg.AuditLogPath, audit.ReadOptions{Tail: 500})
	if err != nil {
		return addJSON(zw, "audit-log.error.json", map[string]string{"error": err.Error()})
	}
	var b strings.Builder
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			continue
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return addText(zw, "audit-log.redacted.jsonl", b.String())
}

func addJSON(zw *zip.Writer, name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return addText(zw, name, string(b)+"\n")
}

func addText(zw *zip.Writer, name, content string) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write([]byte(content))
	return err
}

type configSummary struct {
	ConfigDir           string `json:"config_dir"`
	TunnelName          string `json:"tunnel_name"`
	ServerHost          string `json:"server_host"`
	ListenPort          int    `json:"listen_port"`
	WebUIHost           string `json:"webui_host"`
	WebUIPort           int    `json:"webui_port"`
	ExternalInterface   string `json:"external_interface"`
	IPv4Subnet          string `json:"ipv4_subnet"`
	DNS                 string `json:"dns"`
	AllowedIPs          string `json:"allowed_ips"`
	PersistentKeepalive int    `json:"persistent_keepalive"`
	MTU                 int    `json:"mtu"`
	ProtocolProfile     string `json:"protocol_profile"`
	ApplyConfig         bool   `json:"apply_config"`
	PublishedUDPPorts   string `json:"published_udp_ports"`
	AuditLogEnabled     bool   `json:"audit_log_enabled"`
	AuditLogMaxSize     int64  `json:"audit_log_max_size"`
	AuditLogMaxFiles    int    `json:"audit_log_max_files"`
	PasswordSet         bool   `json:"password_set"`
	SessionSecretSet    bool   `json:"session_secret_set"`
}

func redactedConfig(cfg config.Config) configSummary {
	return configSummary{
		ConfigDir:           cfg.ConfigDir,
		TunnelName:          cfg.TunnelName,
		ServerHost:          cfg.ServerHost,
		ListenPort:          cfg.ListenPort,
		WebUIHost:           cfg.WebUIHost,
		WebUIPort:           cfg.WebUIPort,
		ExternalInterface:   cfg.ExternalInterface,
		IPv4Subnet:          cfg.IPv4Subnet,
		DNS:                 cfg.DNS,
		AllowedIPs:          cfg.AllowedIPs,
		PersistentKeepalive: cfg.PersistentKeepalive,
		MTU:                 cfg.MTU,
		ProtocolProfile:     cfg.ProtocolProfile,
		ApplyConfig:         cfg.ApplyConfig,
		PublishedUDPPorts:   cfg.PublishedUDPPorts,
		AuditLogEnabled:     cfg.AuditLogEnabled,
		AuditLogMaxSize:     cfg.AuditLogMaxSize,
		AuditLogMaxFiles:    cfg.AuditLogMaxFiles,
		PasswordSet:         cfg.Password != "",
		SessionSecretSet:    cfg.SessionSecret != "",
	}
}

type stateSummary struct {
	SchemaVersion     int             `json:"schema_version"`
	ServerHost        string          `json:"server_host"`
	ExternalInterface string          `json:"external_interface"`
	Warp              warpSummary     `json:"warp,omitempty"`
	Tunnels           []tunnelSummary `json:"tunnels"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type warpSummary struct {
	Configured          bool      `json:"configured"`
	InterfaceName       string    `json:"interface_name"`
	Endpoint            string    `json:"endpoint,omitempty"`
	AddressV4           string    `json:"address_v4,omitempty"`
	MTU                 int       `json:"mtu,omitempty"`
	PersistentKeepalive int       `json:"persistent_keepalive,omitempty"`
	LastApplyAt         time.Time `json:"last_apply_at,omitempty"`
	LastApplyError      string    `json:"last_apply_error,omitempty"`
}

type tunnelSummary struct {
	ID                  string          `json:"id"`
	Name                string          `json:"name"`
	InterfaceName       string          `json:"interface_name"`
	Enabled             bool            `json:"enabled"`
	EgressMode          string          `json:"egress_mode"`
	ListenPort          int             `json:"listen_port"`
	ServerAddress       string          `json:"server_address"`
	IPv4Subnet          string          `json:"ipv4_subnet"`
	DNS                 string          `json:"dns"`
	AllowedIPs          string          `json:"allowed_ips"`
	Keepalive           int             `json:"persistent_keepalive"`
	MTU                 int             `json:"mtu"`
	ServerPublicKeyHash string          `json:"server_public_key_hash,omitempty"`
	ProtocolProfileID   string          `json:"protocol_profile_id"`
	ProtocolParamKeys   []string        `json:"protocol_param_keys,omitempty"`
	ConfigRevision      int             `json:"config_revision"`
	Clients             []clientSummary `json:"clients"`
	LastRenderAt        time.Time       `json:"last_render_at,omitempty"`
	LastApplyAt         time.Time       `json:"last_apply_at,omitempty"`
	LastApplyError      string          `json:"last_apply_error,omitempty"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

type clientSummary struct {
	ID            string    `json:"id"`
	TunnelID      string    `json:"tunnel_id"`
	Name          string    `json:"name"`
	Enabled       bool      `json:"enabled"`
	Active        bool      `json:"active"`
	Expired       bool      `json:"expired"`
	IPv4Address   string    `json:"ipv4_address"`
	PublicKeyHash string    `json:"public_key_hash,omitempty"`
	Revision      int       `json:"revision"`
	EverConnected bool      `json:"ever_connected,omitempty"`
	LastSeenAt    time.Time `json:"last_seen_at,omitempty"`
	ExpiresAt     time.Time `json:"expires_at,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func redactedState(state config.State) stateSummary {
	out := stateSummary{
		SchemaVersion:     state.SchemaVersion,
		ServerHost:        state.ServerHost,
		ExternalInterface: state.ExternalInterface,
		Warp: warpSummary{
			Configured:          state.Warp.Configured(),
			InterfaceName:       state.Warp.RuntimeInterface(),
			Endpoint:            state.Warp.Endpoint,
			AddressV4:           state.Warp.AddressV4,
			MTU:                 state.Warp.MTU,
			PersistentKeepalive: state.Warp.PersistentKeepalive,
			LastApplyAt:         state.Warp.LastApplyAt,
			LastApplyError:      state.Warp.LastApplyError,
		},
		CreatedAt: state.CreatedAt,
		UpdatedAt: state.UpdatedAt,
	}
	for _, tunnel := range state.Tunnels {
		item := tunnelSummary{
			ID:                  tunnel.ID,
			Name:                tunnel.Name,
			InterfaceName:       tunnel.InterfaceName,
			Enabled:             tunnel.Enabled,
			EgressMode:          tunnel.EgressMode,
			ListenPort:          tunnel.ListenPort,
			ServerAddress:       tunnel.ServerAddress,
			IPv4Subnet:          tunnel.IPv4Subnet,
			DNS:                 tunnel.DNS,
			AllowedIPs:          tunnel.AllowedIPs,
			Keepalive:           tunnel.Keepalive,
			MTU:                 tunnel.MTU,
			ServerPublicKeyHash: hashValue(tunnel.ServerPublicKey),
			ProtocolProfileID:   tunnel.ProtocolProfileID,
			ProtocolParamKeys:   protocolParamKeys(tunnel.ProtocolParams),
			ConfigRevision:      tunnel.ConfigRevision,
			LastRenderAt:        tunnel.LastRenderAt,
			LastApplyAt:         tunnel.LastApplyAt,
			LastApplyError:      tunnel.LastApplyError,
			CreatedAt:           tunnel.CreatedAt,
			UpdatedAt:           tunnel.UpdatedAt,
		}
		now := time.Now().UTC()
		for _, client := range tunnel.Clients {
			item.Clients = append(item.Clients, clientSummary{
				ID:            client.ID,
				TunnelID:      client.TunnelID,
				Name:          client.Name,
				Enabled:       client.Enabled,
				Active:        config.ClientActive(client, now),
				Expired:       config.ClientExpired(client, now),
				IPv4Address:   client.IPv4Address,
				PublicKeyHash: hashValue(client.PublicKey),
				Revision:      client.ConfigRevision,
				EverConnected: client.EverConnected,
				LastSeenAt:    client.LastSeenAt,
				ExpiresAt:     client.ExpiresAt,
				CreatedAt:     client.CreatedAt,
				UpdatedAt:     client.UpdatedAt,
			})
		}
		out.Tunnels = append(out.Tunnels, item)
	}
	return out
}

func protocolParamKeys(params config.ProtocolParams) []string {
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func hashValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])[:16]
}

type fileItem struct {
	Path  string `json:"path"`
	Mode  string `json:"mode,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Type  string `json:"type"`
	Error string `json:"error,omitempty"`
}

func fileInventory(root string) []fileItem {
	var out []fileItem
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		if rel == "." {
			rel = "."
		}
		item := fileItem{Path: filepath.ToSlash(rel)}
		if err != nil {
			item.Error = err.Error()
			out = append(out, item)
			//nolint:nilerr // Keep walking while preserving per-file inventory errors in the bundle.
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			item.Error = statErr.Error()
			out = append(out, item)
			//nolint:nilerr // Keep walking while preserving per-file inventory errors in the bundle.
			return nil
		}
		item.Mode = fmt.Sprintf("%04o", info.Mode().Perm())
		item.Size = info.Size()
		if d.IsDir() {
			item.Type = "dir"
		} else {
			item.Type = "file"
		}
		out = append(out, item)
		return nil
	})
	if err != nil {
		return []fileItem{{Path: ".", Type: "error", Error: err.Error()}}
	}
	return out
}

type runtimeCommand struct {
	Name string
	Args []string
}

func runtimeCommands(cfg config.Config, state config.State) []runtimeCommand {
	cmds := []runtimeCommand{
		{Name: "ip-route", Args: []string{"ip", "route"}},
		{Name: "ip-route-all", Args: []string{"ip", "route", "show", "table", "all"}},
		{Name: "ip-route-get-1-1-1-1", Args: []string{"ip", "route", "get", "1.1.1.1"}},
		{Name: "ip-rule", Args: []string{"ip", "rule"}},
		{Name: "ip-addr", Args: []string{"ip", "-brief", "addr"}},
		{Name: "ss-udp-listeners", Args: []string{"ss", "-lunp"}},
		{Name: "iptables-filter", Args: []string{"iptables", "-S"}},
		{Name: "iptables-filter-counters", Args: []string{"iptables", "-L", "FORWARD", "-v", "-n"}},
		{Name: "iptables-nat", Args: []string{"iptables", "-t", "nat", "-S"}},
		{Name: "iptables-nat-counters", Args: []string{"iptables", "-t", "nat", "-L", "POSTROUTING", "-v", "-n"}},
		{Name: "sysctl-ip-forward", Args: []string{"sysctl", "net.ipv4.ip_forward"}},
		{Name: "sysctl-rp-filter-all", Args: []string{"sysctl", "net.ipv4.conf.all.rp_filter"}},
		{Name: "sysctl-rp-filter-default", Args: []string{"sysctl", "net.ipv4.conf.default.rp_filter"}},
		{Name: "sysctl-rp-filter-external", Args: []string{"sysctl", "net.ipv4.conf." + cfg.ExternalInterface + ".rp_filter"}},
		{Name: "awg-show", Args: []string{"awg", "show"}},
	}
	for _, tunnel := range state.Tunnels {
		cmds = append(cmds, runtimeCommand{
			Name: "awg-show-" + safeName(tunnel.InterfaceName),
			Args: []string{"awg", "show", tunnel.InterfaceName},
		})
		cmds = append(cmds, runtimeCommand{
			Name: "ip-link-" + safeName(tunnel.InterfaceName),
			Args: []string{"ip", "link", "show", tunnel.InterfaceName},
		})
		cmds = append(cmds, runtimeCommand{
			Name: "ss-udp-" + safeName(tunnel.InterfaceName),
			Args: []string{"ss", "-lunp", "sport", "=", ":" + strconv.Itoa(tunnel.ListenPort)},
		})
		cmds = append(cmds, runtimeCommand{
			Name: "sysctl-rp-filter-" + safeName(tunnel.InterfaceName),
			Args: []string{"sysctl", "net.ipv4.conf." + tunnel.InterfaceName + ".rp_filter"},
		})
	}
	return cmds
}

func safeName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer("/", "-", "\\", "-", " ", "-")
	return replacer.Replace(value)
}

func runText(ctx context.Context, args ...string) string {
	if len(args) == 0 {
		return ""
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	out, err := cmd.CombinedOutput()
	text := "$ " + strings.Join(args, " ") + "\n"
	if err != nil {
		text += "error: " + err.Error() + "\n"
	}
	return sanitizeText(text + string(out))
}

func doctorText(results []doctor.Result) string {
	var b strings.Builder
	for _, result := range results {
		fmt.Fprintf(&b, "%-4s %s", strings.ToUpper(result.Level), result.Area)
		if result.Message != "" {
			fmt.Fprintf(&b, ": %s", result.Message)
		}
		b.WriteByte('\n')
	}
	return sanitizeText(b.String())
}

var (
	keyLineRE       = regexp.MustCompile(`(?m)^(\s*(?:private key|preshared key):\s+).+$`)
	protocolParamRE = regexp.MustCompile(`(?mi)^(\s*(?:jc|jmin|jmax|s[1-4]|h[1-4]|i[1-5]):\s+).+$`)
	pubKeyLineRE    = regexp.MustCompile(`(?m)^(\s*(?:public key|peer):\s+)(\S+).*$`)
)

func sanitizeText(text string) string {
	text = keyLineRE.ReplaceAllString(text, `${1}<redacted>`)
	text = protocolParamRE.ReplaceAllString(text, `${1}<redacted>`)
	return pubKeyLineRE.ReplaceAllStringFunc(text, func(line string) string {
		match := pubKeyLineRE.FindStringSubmatch(line)
		if len(match) != 3 {
			return line
		}
		return match[1] + hashValue(match[2])
	})
}
