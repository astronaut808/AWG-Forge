package doctor

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
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
	c.checkDir(cfg.ConfigDir)
	if !cfg.ApplyConfig {
		c.warn("apply", "APPLY_CONFIG=false; configs render but tunnels are not applied automatically")
	}
	for _, tunnel := range state.Tunnels {
		c.checkPort(tunnel)
		if !config.PortInRanges(tunnel.ListenPort, cfg.PublishedUDPPorts) {
			c.warn("Docker ports "+tunnel.Name, fmt.Sprintf("listen port %d is outside PUBLISHED_UDP_PORTS=%s", tunnel.ListenPort, cfg.PublishedUDPPorts))
		}
		if _, err := render.ServerConfig(state, tunnel); err != nil {
			c.fail("render "+tunnel.Name, err.Error())
		} else {
			c.ok("render "+tunnel.Name, "server config renders")
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
	if tunnel.Enabled {
		if exec.Command("ip", "link", "show", tunnel.InterfaceName).Run() == nil {
			c.ok(area, tunnel.InterfaceName+" link exists")
		} else {
			c.fail(area, tunnel.InterfaceName+" link is not up; restart tunnel or check apply logs")
		}
	} else {
		c.warn(area, "tunnel disabled")
	}
	if tunnel.ProtocolProfileID == "awg_2_0" {
		c.ok("compat "+tunnel.Name, "AWG 2.0 requires compatible AmneziaVPN clients; use .conf import, QR is experimental")
	}
	show, err := awgShow(tunnel.InterfaceName)
	if err != nil {
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
	} else {
		c.warn(area+" handshake", "no handshake yet; check client import, UDP reachability, and published port "+strconv.Itoa(tunnel.ListenPort)+"/udp")
	}
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
