package app

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/astronaut808/awg-forge/internal/config"
)

func selectAutomaticUDPPort(cfg config.Config, tunnels []config.Tunnel, randomIndex func(int) (int, error), available func(int) bool) (int, string, error) {
	ranges, err := config.EffectiveTunnelUDPPortRanges(cfg)
	if err != nil {
		return 0, "", err
	}
	used := make(map[int]bool, len(tunnels))
	for _, tunnel := range tunnels {
		used[tunnel.ListenPort] = true
	}
	candidates := make([]int, 0)
	seen := make(map[int]bool)
	for _, item := range ranges {
		for port := item.First; port <= item.Last; port++ {
			if port < 1024 || automaticUDPPortDenied(port) || used[port] || seen[port] {
				continue
			}
			seen[port] = true
			candidates = append(candidates, port)
		}
	}
	if len(candidates) == 0 {
		return 0, config.FormatPortRanges(ranges), errors.New("automatic UDP port range has no eligible ports")
	}
	start, err := randomIndex(len(candidates))
	if err != nil {
		return 0, config.FormatPortRanges(ranges), fmt.Errorf("select random UDP port: %w", err)
	}
	for offset := range candidates {
		port := candidates[(start+offset)%len(candidates)]
		if available(port) {
			return port, config.FormatPortRanges(ranges), nil
		}
	}
	return 0, config.FormatPortRanges(ranges), errors.New("no free UDP port is available in the automatic range")
}

func cryptoRandomIndex(limit int) (int, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(int64(limit)))
	if err != nil {
		return 0, err
	}
	return int(value.Int64()), nil
}

func udpPortAvailable(port int) bool {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: port})
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (s *Service) automaticUDPPortCheck() (func(int) bool, error) {
	if !s.cfg.ApplyConfig {
		return func(int) bool { return true }, nil
	}
	reserved, err := s.runtimeOps.managedRelayPorts()
	if err != nil {
		return nil, err
	}
	return func(port int) bool { return !reserved[port] && udpPortAvailable(port) }, nil
}

func (s *Service) checkRelayPortReservation(port int) error {
	reserved, err := s.runtimeOps.managedRelayPorts()
	if err != nil {
		return err
	}
	if reserved[port] {
		return fmt.Errorf("UDP port %d is reserved by an awg-forge relay", port)
	}
	return nil
}

func automaticUDPPortDenied(port int) bool {
	switch port {
	case 53, 123, 443, 500, 3478, 4500, 5349:
		return true
	default:
		return false
	}
}

func automaticUDPPortEligible(cfg config.Config, port int) (bool, string, error) {
	ranges, err := config.EffectiveTunnelUDPPortRanges(cfg)
	if err != nil {
		return false, "", err
	}
	rangeSpec := config.FormatPortRanges(ranges)
	if port < 1024 || automaticUDPPortDenied(port) {
		return false, rangeSpec, nil
	}
	for _, item := range ranges {
		if port >= item.First && port <= item.Last {
			return true, rangeSpec, nil
		}
	}
	return false, rangeSpec, nil
}

// managedRelayPorts reads only the public ports reserved by Forge relay DNAT rules.
func managedRelayPorts() (map[int]bool, error) {
	if runtime.GOOS != "linux" {
		return map[int]bool{}, nil
	}
	output, err := exec.Command("iptables", "-w", "5", "-t", "nat", "-S").Output()
	if err != nil {
		return nil, fmt.Errorf("inspect managed relay ports: %w", err)
	}
	return parseManagedRelayPorts(string(output)), nil
}

var managedRelayComment = regexp.MustCompile(`^awg-forge-relay-[0-9a-f]{16}-dnat$`)

func parseManagedRelayPorts(listing string) map[int]bool {
	ports := make(map[int]bool)
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "-A" || fields[1] != "PREROUTING" {
			continue
		}
		var owned bool
		var port int
		for i := 2; i+1 < len(fields); i++ {
			switch fields[i] {
			case "--comment":
				owned = managedRelayComment.MatchString(strings.Trim(fields[i+1], `"`))
			case "--dport":
				port, _ = strconv.Atoi(fields[i+1])
			}
		}
		if owned && port > 0 && port <= 65535 {
			ports[port] = true
		}
	}
	return ports
}
