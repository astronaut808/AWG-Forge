package config

import (
	"strconv"
	"strings"
)

func PortInRanges(port int, spec string) bool {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return true
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			min, errMin := strconv.Atoi(strings.TrimSpace(bounds[0]))
			max, errMax := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if errMin == nil && errMax == nil && port >= min && port <= max {
				return true
			}
			continue
		}
		value, err := strconv.Atoi(part)
		if err == nil && port == value {
			return true
		}
	}
	return false
}
