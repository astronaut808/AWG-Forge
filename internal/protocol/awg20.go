package protocol

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/astronaut808/awg-forge/internal/config"
)

var awg20Keys = []string{"Jc", "Jmin", "Jmax", "S1", "S2", "S3", "S4", "H1", "H2", "H3", "H4", "I1", "I2", "I3", "I4", "I5"}

type AWG20 struct{}

func (AWG20) ID() string          { return "awg_2_0" }
func (AWG20) DisplayName() string { return "AmneziaWG 2.0" }
func (AWG20) Version() string     { return "2" }

func (AWG20) GenerateDefaults() (config.ProtocolParams, error) {
	params, err := defaultLegacyParams()
	if err != nil {
		return nil, err
	}
	s3, err := randomInt(15, 64)
	if err != nil {
		return nil, err
	}
	s4, err := randomInt(8, 32)
	if err != nil {
		return nil, err
	}
	ranges, err := defaultHeaderRanges()
	if err != nil {
		return nil, err
	}
	params["S3"] = strconv.Itoa(s3)
	params["S4"] = strconv.Itoa(s4)
	params["H1"] = ranges[0]
	params["H2"] = ranges[1]
	params["H3"] = ranges[2]
	params["H4"] = ranges[3]
	params["I1"] = defaultDNSLikeI1
	params["I2"] = defaultTimedNoiseI2
	params["I3"] = defaultDigitsNoiseI3
	params["I4"] = defaultCharsNoiseI4
	params["I5"] = defaultRandomNoiseI5
	return params, nil
}

func (AWG20) Validate(params config.ProtocolParams) error {
	for _, k := range awg20Keys {
		if _, ok := params[k]; !ok {
			return fmt.Errorf("missing protocol parameter %s", k)
		}
	}
	if _, _, err := validateJunkAndBasePadding(params); err != nil {
		return err
	}
	if err := validateIntParam(params, "S3", 0, 64); err != nil {
		return err
	}
	if err := validateIntParam(params, "S4", 0, 32); err != nil {
		return err
	}
	if err := validateHeaderRanges(params); err != nil {
		return err
	}
	for _, k := range []string{"I1", "I2", "I3", "I4", "I5"} {
		if err := validateSignatureParam(k, params[k]); err != nil {
			return err
		}
	}
	return nil
}

func (p AWG20) RenderServerInterface(ctx RenderContext) ([]ConfigLine, error) {
	if err := p.Validate(ctx.Tunnel.ProtocolParams); err != nil {
		return nil, err
	}
	lines, err := baseInterfaceLines(ctx)
	if err != nil {
		return nil, err
	}
	return appendParamKeys(lines, ctx.Tunnel.ProtocolParams, awg20Keys), nil
}

func (AWG20) RenderServerPeer(ctx RenderContext, client config.Client) ([]ConfigLine, error) {
	return Legacy10{}.RenderServerPeer(ctx, client)
}

func (p AWG20) RenderClientInterface(ctx RenderContext, client config.Client) ([]ConfigLine, error) {
	if err := p.Validate(ctx.Tunnel.ProtocolParams); err != nil {
		return nil, err
	}
	lines := []ConfigLine{
		{"PrivateKey", client.PrivateKey},
		{"Address", client.IPv4Address + "/32"},
		{"DNS", ctx.Tunnel.DNS},
	}
	if ctx.Tunnel.MTU > 0 {
		lines = append(lines, ConfigLine{"MTU", strconv.Itoa(ctx.Tunnel.MTU)})
	}
	return appendParamKeys(lines, ctx.Tunnel.ProtocolParams, awg20Keys), nil
}

func (AWG20) RenderClientPeer(ctx RenderContext, client config.Client) ([]ConfigLine, error) {
	return Legacy10{}.RenderClientPeer(ctx, client)
}

func defaultHeaderRanges() ([4]string, error) {
	const width uint32 = 31
	used := make([]headerRange, 0, 4)
	var out [4]string
	for i := 0; i < 4; i++ {
		for {
			base, err := randomUint32Below(2147483000)
			if err != nil {
				return out, err
			}
			start := 1024 + base
			end := start + width
			next := headerRange{start: uint64(start), end: uint64(end)}
			overlaps := false
			for _, prev := range used {
				if next.overlaps(prev) {
					overlaps = true
					break
				}
			}
			if overlaps {
				continue
			}
			used = append(used, next)
			out[i] = fmt.Sprintf("%d-%d", next.start, next.end)
			break
		}
	}
	return out, nil
}

type headerRange struct {
	start uint64
	end   uint64
}

func (r headerRange) overlaps(other headerRange) bool {
	return r.start <= other.end && other.start <= r.end
}

func validateHeaderRanges(params config.ProtocolParams) error {
	seen := map[string]headerRange{}
	for _, key := range []string{"H1", "H2", "H3", "H4"} {
		rng, err := parseHeaderRange(params[key])
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		for prevKey, prev := range seen {
			if rng.overlaps(prev) {
				return fmt.Errorf("%s range overlaps %s", key, prevKey)
			}
		}
		seen[key] = rng
	}
	return nil
}

func parseHeaderRange(value string) (headerRange, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return headerRange{}, fmt.Errorf("header range is empty")
	}
	parts := strings.Split(value, "-")
	if len(parts) > 2 {
		return headerRange{}, fmt.Errorf("invalid header range")
	}
	start, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 32)
	if err != nil {
		return headerRange{}, fmt.Errorf("invalid uint32 value")
	}
	end := start
	if len(parts) == 2 {
		end, err = strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 32)
		if err != nil {
			return headerRange{}, fmt.Errorf("invalid uint32 value")
		}
		if end < start {
			return headerRange{}, fmt.Errorf("range start must be <= end")
		}
	}
	return headerRange{start: start, end: end}, nil
}
