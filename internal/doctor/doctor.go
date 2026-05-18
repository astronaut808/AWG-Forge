package doctor

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/render"
)

func Run(cfg config.Config, service *app.Service) error {
	state, err := service.Init()
	if err != nil {
		fail("state", err.Error())
	} else {
		ok("state initialized")
	}
	checkRoot()
	checkPath("/dev/net/tun")
	checkCommand("awg")
	checkCommand("awg-quick")
	checkCommand("amneziawg-go")
	checkCommand("iptables")
	checkCommand("ip")
	checkIPTables()
	checkForwarding()
	checkInterface(cfg.ExternalInterface)
	checkDir(cfg.ConfigDir)
	for _, tunnel := range state.Tunnels {
		checkPort(tunnel)
		if _, err := render.ServerConfig(state, tunnel); err != nil {
			fail("render "+tunnel.Name, err.Error())
		} else {
			ok("server config renders for " + tunnel.Name)
		}
	}
	return nil
}

func checkRoot() {
	if os.Geteuid() == 0 {
		ok("running as root")
	} else {
		warn("not running as root; container must have NET_ADMIN and /dev/net/tun")
	}
}

func checkPath(path string) {
	if _, err := os.Stat(path); err != nil {
		fail(path, err.Error())
	} else {
		ok(path + " exists")
	}
}

func checkCommand(name string) {
	if _, err := exec.LookPath(name); err != nil {
		fail(name, "not found in PATH")
	} else {
		ok(name + " found")
	}
}

func checkIPTables() {
	out, err := exec.Command("iptables", "-V").CombinedOutput()
	if err != nil {
		fail("iptables -V", err.Error())
		return
	}
	if strings.Contains(string(out), "nf_tables") {
		ok("iptables uses nf_tables")
	} else {
		warn("iptables does not report nf_tables backend: " + strings.TrimSpace(string(out)))
	}
}

func checkForwarding() {
	b, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		fail("IPv4 forwarding", err.Error())
		return
	}
	if strings.TrimSpace(string(b)) == "1" {
		ok("IPv4 forwarding enabled")
	} else {
		fail("IPv4 forwarding", "net.ipv4.ip_forward is not 1")
	}
}

func checkInterface(name string) {
	if _, err := net.InterfaceByName(name); err != nil {
		fail("external interface", err.Error())
	} else {
		ok("external interface " + name + " exists")
	}
}

func checkPort(tunnel config.Tunnel) {
	port := tunnel.ListenPort
	conn, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", port))
	if err != nil {
		if awgPortMatches(tunnel.InterfaceName, port) {
			ok(fmt.Sprintf("UDP listen port %d is already owned by %s", port, tunnel.InterfaceName))
			return
		}
		fail("UDP listen port", err.Error())
		return
	}
	_ = conn.Close()
	ok(fmt.Sprintf("UDP listen port %d available", port))
}

func awgPortMatches(interfaceName string, port int) bool {
	out, err := exec.Command("awg", "show", interfaceName, "listen-port").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == fmt.Sprintf("%d", port)
}

func checkDir(dir string) {
	info, err := os.Stat(dir)
	if err != nil {
		fail("config directory", err.Error())
		return
	}
	if info.Mode().Perm() == 0700 {
		ok("config directory permissions 0700")
	} else {
		warn(fmt.Sprintf("config directory permissions are %o, expected 0700", info.Mode().Perm()))
	}
}

func ok(msg string)   { fmt.Println("OK   " + msg) }
func warn(msg string) { fmt.Println("WARN " + msg) }
func fail(area, msg string) {
	fmt.Printf("FAIL %s: %s\n", area, msg)
}
