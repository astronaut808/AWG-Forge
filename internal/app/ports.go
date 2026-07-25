package app

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net"

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

func (s *Service) automaticUDPPortAvailable(port int) bool {
	if !s.cfg.ApplyConfig {
		return true
	}
	return udpPortAvailable(port)
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
