package config

import (
	"errors"
	"strconv"
	"strings"
)

const DefaultTunnelUDPPortRange = "30000-49999"

type PortRange struct {
	First int
	Last  int
}

func PortInRanges(port int, spec string) bool {
	if strings.TrimSpace(spec) == "" {
		return true
	}
	ranges, err := ParsePortRanges(spec)
	if err != nil {
		return false
	}
	for _, item := range ranges {
		if port >= item.First && port <= item.Last {
			return true
		}
	}
	return false
}

func ParsePortRanges(spec string) ([]PortRange, error) {
	var ranges []PortRange
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		first, last, err := parsePortRange(part)
		if err != nil {
			return nil, err
		}
		ranges = append(ranges, PortRange{First: first, Last: last})
	}
	if len(ranges) == 0 {
		return nil, errors.New("UDP port range is empty")
	}
	return ranges, nil
}

func EffectiveTunnelUDPPortRanges(cfg Config) ([]PortRange, error) {
	spec := strings.TrimSpace(cfg.TunnelUDPPortRange)
	publishedSpec := strings.TrimSpace(cfg.PublishedUDPPorts)
	if spec == "" && publishedSpec != "" {
		spec = publishedSpec
	} else if spec == "" {
		spec = DefaultTunnelUDPPortRange
	}
	automatic, err := ParsePortRanges(spec)
	if err != nil {
		return nil, err
	}
	if publishedSpec == "" {
		return automatic, nil
	}
	published, err := ParsePortRanges(publishedSpec)
	if err != nil {
		return nil, errors.New("PUBLISHED_UDP_PORTS is invalid")
	}
	intersection := intersectPortRanges(automatic, published)
	if len(intersection) == 0 {
		return nil, errors.New("automatic UDP port range does not overlap PUBLISHED_UDP_PORTS")
	}
	return intersection, nil
}

func FormatPortRanges(ranges []PortRange) string {
	parts := make([]string, 0, len(ranges))
	for _, item := range ranges {
		if item.First == item.Last {
			parts = append(parts, strconv.Itoa(item.First))
			continue
		}
		parts = append(parts, strconv.Itoa(item.First)+"-"+strconv.Itoa(item.Last))
	}
	return strings.Join(parts, ",")
}

func parsePortRange(part string) (int, int, error) {
	firstRaw, lastRaw := part, part
	if strings.Contains(part, "-") {
		bounds := strings.Split(part, "-")
		if len(bounds) != 2 {
			return 0, 0, errors.New("UDP port range must contain ports or inclusive ranges")
		}
		firstRaw, lastRaw = strings.TrimSpace(bounds[0]), strings.TrimSpace(bounds[1])
	}
	first, errFirst := strconv.Atoi(firstRaw)
	last, errLast := strconv.Atoi(lastRaw)
	if errFirst != nil || errLast != nil || first < 1 || last > 65535 || first > last {
		return 0, 0, errors.New("UDP port range must contain ports from 1 to 65535")
	}
	return first, last, nil
}

func intersectPortRanges(left, right []PortRange) []PortRange {
	var result []PortRange
	for _, a := range left {
		for _, b := range right {
			first := max(a.First, b.First)
			last := min(a.Last, b.Last)
			if first <= last {
				result = append(result, PortRange{First: first, Last: last})
			}
		}
	}
	return result
}
