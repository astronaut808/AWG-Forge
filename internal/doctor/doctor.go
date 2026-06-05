package doctor

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/firewall"
	"github.com/astronaut808/awg-forge/internal/render"
)

type Result struct {
	Level   string `json:"level"`
	Area    string `json:"area"`
	Message string `json:"message"`
}

func Run(cfg config.Config, service *app.Service) error {
	for _, result := range Check(cfg, service) {
		fmt.Printf("%-4s %s", strings.ToUpper(result.Level), result.Area)
		if result.Message != "" {
			fmt.Printf(": %s", result.Message)
		}
		fmt.Println()
	}
	return nil
}

func Check(cfg config.Config, service *app.Service) []Result {
	c := checker{}
	state, err := service.Init()
	if err != nil {
		c.fail("state", err.Error())
	} else {
		c.ok("state", "initialized")
	}
	c.checkRoot()
	c.checkPath("/dev/net/tun")
	c.checkCommand("awg")
	c.checkCommand("awg-quick")
	c.checkCommand("amneziawg-go")
	c.checkCommand("iptables")
	c.checkCommand("ip")
	c.checkIPTables()
	c.checkForwarding()
	c.checkInterface(cfg.ExternalInterface)
	c.checkExternalRoute(cfg.ExternalInterface)
	c.checkRPFilter("all", "all")
	c.checkRPFilter("default", "default")
	c.checkRPFilter("external interface", cfg.ExternalInterface)
	c.checkDir(cfg.ConfigDir)
	if !cfg.ApplyConfig {
		c.warn("apply", "APPLY_CONFIG=false; configs render but tunnels are not applied automatically")
	}
	for _, tunnel := range state.Tunnels {
		c.checkPort(tunnel)
		if cfg.ApplyConfig && tunnel.Enabled {
			c.checkUDPListener(tunnel)
			c.checkRuntimeConfig(tunnel)
			c.checkRPFilter("tunnel "+tunnel.Name, tunnel.InterfaceName)
		}
		if !config.PortInRanges(tunnel.ListenPort, cfg.PublishedUDPPorts) {
			c.warn("Docker ports "+tunnel.Name, fmt.Sprintf("listen port %d is outside PUBLISHED_UDP_PORTS=%s", tunnel.ListenPort, cfg.PublishedUDPPorts))
		}
		if _, err := render.ServerConfig(state, tunnel); err != nil {
			c.fail("render "+tunnel.Name, err.Error())
		} else {
			c.ok("render "+tunnel.Name, "server config renders")
		}
		if cfg.ApplyConfig {
			c.checkFirewallRules(cfg, tunnel)
		} else {
			c.warn("firewall "+tunnel.Name, "APPLY_CONFIG=false; firewall runtime rules are not expected")
		}
		c.checkTunnelRuntime(tunnel)
	}
	return c.results
}

type checker struct {
	results []Result
}

func (c *checker) checkRoot() {
	if os.Geteuid() == 0 {
		c.ok("runtime", "running as root")
	} else {
		c.warn("runtime", "not running as root; container must have NET_ADMIN and /dev/net/tun")
	}
}

func (c *checker) checkPath(path string) {
	if _, err := os.Stat(path); err != nil {
		c.fail(path, err.Error())
	} else {
		c.ok(path, "exists")
	}
}

func (c *checker) checkCommand(name string) {
	if _, err := exec.LookPath(name); err != nil {
		c.fail(name, "not found in PATH")
	} else {
		c.ok(name, "found")
	}
}

func (c *checker) checkIPTables() {
	out, err := exec.Command("iptables", "-V").CombinedOutput()
	if err != nil {
		c.fail("iptables -V", err.Error())
		return
	}
	if strings.Contains(string(out), "nf_tables") {
		c.ok("iptables", "uses nf_tables")
	} else {
		c.warn("iptables", "does not report nf_tables backend: "+strings.TrimSpace(string(out)))
	}
}

func (c *checker) checkForwarding() {
	b, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		c.fail("IPv4 forwarding", err.Error())
		return
	}
	if strings.TrimSpace(string(b)) == "1" {
		c.ok("IPv4 forwarding", "enabled")
	} else {
		c.fail("IPv4 forwarding", "net.ipv4.ip_forward is not 1")
	}
}

func (c *checker) checkInterface(name string) {
	if _, err := net.InterfaceByName(name); err != nil {
		c.fail("external interface", err.Error())
	} else {
		c.ok("external interface", name+" exists")
	}
}

func (c *checker) checkExternalRoute(interfaceName string) {
	out, err := exec.Command("ip", "route", "get", "1.1.1.1").CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		c.fail("external route", "ip route get 1.1.1.1 failed: "+msg)
		return
	}
	dev := parseRouteDev(string(out))
	if dev == "" {
		c.warn("external route", "could not detect egress interface from ip route get 1.1.1.1")
		return
	}
	if dev != interfaceName {
		c.fail("external route", fmt.Sprintf("IPv4 egress uses %s, but EXTERNAL_INTERFACE=%s", dev, interfaceName))
		return
	}
	c.ok("external route", "IPv4 egress uses "+dev)
}

func parseRouteDev(out string) string {
	fields := strings.Fields(out)
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == "dev" {
			return fields[i+1]
		}
	}
	return ""
}

func (c *checker) checkRPFilter(area, interfaceName string) {
	path := filepath.Join("/proc/sys/net/ipv4/conf", interfaceName, "rp_filter")
	b, err := os.ReadFile(path)
	if err != nil {
		c.warn("rp_filter "+area, "could not read "+path+": "+err.Error())
		return
	}
	value := strings.TrimSpace(string(b))
	switch value {
	case "0":
		c.ok("rp_filter "+area, "disabled")
	case "1":
		c.warn("rp_filter "+area, "strict mode may drop asymmetric VPN traffic")
	case "2":
		c.ok("rp_filter "+area, "loose mode")
	default:
		c.warn("rp_filter "+area, "unexpected value "+value)
	}
}

func (c *checker) checkPort(tunnel config.Tunnel) {
	port := tunnel.ListenPort
	conn, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", port))
	if err != nil {
		if awgPortMatches(tunnel.InterfaceName, port) {
			c.ok("UDP "+tunnel.Name, fmt.Sprintf("listen port %d is already owned by %s", port, tunnel.InterfaceName))
			return
		}
		c.fail("UDP "+tunnel.Name, err.Error())
		return
	}
	_ = conn.Close()
	c.ok("UDP "+tunnel.Name, fmt.Sprintf("listen port %d available", port))
}

func (c *checker) checkUDPListener(tunnel config.Tunnel) {
	if _, err := exec.LookPath("ss"); err != nil {
		c.warn("UDP "+tunnel.Name+"/listener", "ss not found; cannot inspect UDP socket owner")
		return
	}
	out, err := exec.Command("ss", "-H", "-lunp", "sport", "=", ":"+strconv.Itoa(tunnel.ListenPort)).CombinedOutput()
	if err != nil {
		c.warn("UDP "+tunnel.Name+"/listener", "ss failed: "+strings.TrimSpace(string(out)))
		return
	}
	line := firstNonEmptyLine(string(out))
	if line == "" {
		c.warn("UDP "+tunnel.Name+"/listener", fmt.Sprintf("no UDP listener reported for %d/udp; tunnel may not be applied", tunnel.ListenPort))
		return
	}
	c.ok("UDP "+tunnel.Name+"/listener", redactProcessLine(line))
}

func firstNonEmptyLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

var ssPIDRE = regexp.MustCompile(`pid=[0-9]+`)

func redactProcessLine(line string) string {
	line = strings.TrimSpace(line)
	line = ssPIDRE.ReplaceAllString(line, "pid=<pid>")
	if len(line) > 240 {
		line = line[:240] + "..."
	}
	return line
}

func (c *checker) checkRuntimeConfig(tunnel config.Tunnel) {
	path := filepath.Join("/etc/amnezia/amneziawg", tunnel.InterfaceName+".conf")
	if _, err := os.Stat(path); err != nil {
		c.fail("runtime config "+tunnel.Name, err.Error())
		return
	}
	if err := exec.Command("awg-quick", "strip", path).Run(); err != nil {
		c.fail("runtime config "+tunnel.Name, "awg-quick strip failed; check rendered runtime config syntax")
		return
	}
	c.ok("runtime config "+tunnel.Name, "exists and awg-quick strip succeeds")
}

func (c *checker) checkFirewallRules(cfg config.Config, tunnel config.Tunnel) {
	report := firewall.Check(cfg, config.State{Tunnels: []config.Tunnel{tunnel}}, firewall.IPTablesRunner{})
	for _, result := range report.Results {
		area := "firewall " + tunnel.Name + "/" + result.Rule
		switch result.Status {
		case "ok":
			c.ok(area, result.Spec)
		case "duplicate":
			c.warn(area, fmt.Sprintf("duplicate managed rule count=%d; run awg-forge firewall repair", result.Count))
		case "missing":
			c.fail(area, "missing managed rule; run awg-forge firewall repair")
		default:
			c.fail(area, result.Message)
		}
	}
}

func awgPortMatches(interfaceName string, port int) bool {
	out, err := exec.Command("awg", "show", interfaceName, "listen-port").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == fmt.Sprintf("%d", port)
}

func (c *checker) checkTunnelRuntime(tunnel config.Tunnel) {
	area := "runtime " + tunnel.Name
	if tunnel.LastApplyError != "" {
		c.fail(area, "last apply error: "+tunnel.LastApplyError)
	}
	linkExists := false
	if tunnel.Enabled {
		if exec.Command("ip", "link", "show", tunnel.InterfaceName).Run() == nil {
			linkExists = true
			c.ok(area, tunnel.InterfaceName+" link exists")
		} else {
			c.fail(area, tunnel.InterfaceName+" link is not up; restart tunnel or check apply logs")
		}
	} else {
		c.warn(area, "tunnel disabled")
	}
	if tunnel.ProtocolProfileID == "awg_2_0" {
		c.ok("compat "+tunnel.Name, "AWG 2.0 requires compatible AmneziaVPN clients; use .conf import")
	}
	show, err := awgShow(tunnel.InterfaceName)
	if err != nil {
		if linkExists && isProtocolNotSupported(err.Error()) {
			c.fail("runtime "+tunnel.Name+"/awg", tunnel.InterfaceName+" link exists, but awg cannot access it: Protocol not supported; restart tunnel or remove stale link")
			return
		}
		if tunnel.Enabled {
			c.fail("awg "+tunnel.Name, err.Error())
		} else {
			c.warn("awg "+tunnel.Name, err.Error())
		}
		return
	}
	if show.ListenPort == tunnel.ListenPort {
		c.ok("awg "+tunnel.Name, fmt.Sprintf("runtime listen port %d matches state", show.ListenPort))
	} else {
		c.fail("awg "+tunnel.Name, fmt.Sprintf("runtime listen port %d does not match state %d", show.ListenPort, tunnel.ListenPort))
	}
	for _, client := range tunnel.Clients {
		c.checkClientRuntime(tunnel, client, show)
	}
}

func (c *checker) checkClientRuntime(tunnel config.Tunnel, client config.Client, show awgInterface) {
	area := "peer " + tunnel.Name + "/" + client.Name
	if !client.Enabled {
		c.warn(area, "client disabled")
		return
	}
	if tunnel.ConfigRevision > 0 && client.ConfigRevision < tunnel.ConfigRevision {
		c.warn(area, "client config is stale; download and import a fresh .conf")
	}
	peer, ok := show.Peers[client.PublicKey]
	if !ok {
		c.fail(area, "enabled client is missing from runtime peers; restart tunnel or check render/apply")
		return
	}
	if peer.AllowedIPs != "" && !strings.Contains(peer.AllowedIPs, client.IPv4Address+"/32") {
		c.fail(area, "runtime allowed IPs do not include "+client.IPv4Address+"/32")
	} else {
		c.ok(area, "runtime peer present")
	}
	if peer.LatestHandshake != "" {
		msg := "latest handshake " + peer.LatestHandshake
		if peer.Transfer != "" {
			msg += "; transfer " + peer.Transfer
		}
		c.ok(area+" handshake", msg)
	} else if peer.Transfer != "" && peer.Transfer != "0 B received, 0 B sent" {
		c.warn(area+" handshake", "no latest handshake reported by awg show, but transfer counters exist: "+peer.Transfer)
	} else {
		c.warn(area+" handshake", "no handshake yet; check client import, UDP reachability, and published port "+strconv.Itoa(tunnel.ListenPort)+"/udp")
	}
}

func isProtocolNotSupported(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "protocol not supported")
}

type awgInterface struct {
	ListenPort int
	Peers      map[string]awgPeer
}

type awgPeer struct {
	AllowedIPs      string
	LatestHandshake string
	Transfer        string
}

func awgShow(interfaceName string) (awgInterface, error) {
	out, err := exec.Command("awg", "show", interfaceName).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return awgInterface{}, fmt.Errorf("awg show %s failed: %s", interfaceName, msg)
	}
	return parseAWGShow(string(out)), nil
}

var listenPortRE = regexp.MustCompile(`^listening port:\s+([0-9]+)$`)

func parseAWGShow(out string) awgInterface {
	result := awgInterface{Peers: map[string]awgPeer{}}
	var currentKey string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if match := listenPortRE.FindStringSubmatch(line); match != nil {
			result.ListenPort, _ = strconv.Atoi(match[1])
			continue
		}
		if strings.HasPrefix(line, "peer: ") {
			currentKey = strings.TrimSpace(strings.TrimPrefix(line, "peer: "))
			result.Peers[currentKey] = awgPeer{}
			continue
		}
		if currentKey == "" {
			continue
		}
		peer := result.Peers[currentKey]
		switch {
		case strings.HasPrefix(line, "allowed ips: "):
			peer.AllowedIPs = strings.TrimSpace(strings.TrimPrefix(line, "allowed ips: "))
		case strings.HasPrefix(line, "latest handshake: "):
			peer.LatestHandshake = strings.TrimSpace(strings.TrimPrefix(line, "latest handshake: "))
		case strings.HasPrefix(line, "transfer: "):
			peer.Transfer = strings.TrimSpace(strings.TrimPrefix(line, "transfer: "))
		}
		result.Peers[currentKey] = peer
	}
	return result
}

func (c *checker) checkDir(dir string) {
	info, err := os.Stat(dir)
	if err != nil {
		c.fail("config directory", err.Error())
		return
	}
	if info.Mode().Perm() == 0700 {
		c.ok("config directory", "permissions 0700")
	} else {
		c.warn("config directory", fmt.Sprintf("permissions are %o, expected 0700", info.Mode().Perm()))
	}
}

func (c *checker) ok(area, msg string) {
	c.results = append(c.results, Result{Level: "ok", Area: area, Message: msg})
}

func (c *checker) warn(area, msg string) {
	c.results = append(c.results, Result{Level: "warn", Area: area, Message: msg})
}

func (c *checker) fail(area, msg string) {
	c.results = append(c.results, Result{Level: "fail", Area: area, Message: msg})
}
