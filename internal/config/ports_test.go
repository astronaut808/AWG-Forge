package config

import "testing"

func TestPortInRanges(t *testing.T) {
	if !PortInRanges(51820, "") {
		t.Fatal("empty range should allow all ports")
	}
	if !PortInRanges(51825, "51820-51840") {
		t.Fatal("expected port inside range")
	}
	if !PortInRanges(7443, "51820-51840,7443") {
		t.Fatal("expected explicit port match")
	}
	if PortInRanges(51900, "51820-51840,7443") {
		t.Fatal("expected port outside range")
	}
}

func TestParsePortRanges(t *testing.T) {
	ranges, err := ParsePortRanges("30000-30049, 45000")
	if err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 2 || ranges[0] != (PortRange{First: 30000, Last: 30049}) || ranges[1] != (PortRange{First: 45000, Last: 45000}) {
		t.Fatalf("unexpected ranges: %#v", ranges)
	}

	for _, spec := range []string{"", "0", "65536", "40000-30000", "30000-x", "1-2-3"} {
		t.Run(spec, func(t *testing.T) {
			if _, err := ParsePortRanges(spec); err == nil {
				t.Fatalf("expected %q to fail", spec)
			}
		})
	}
}

func TestEffectiveTunnelUDPPortRanges(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "default", cfg: Config{}, want: DefaultTunnelUDPPortRange},
		{name: "configured", cfg: Config{TunnelUDPPortRange: "31000-31100"}, want: "31000-31100"},
		{name: "bridge intersection", cfg: Config{TunnelUDPPortRange: "30000-49999", PublishedUDPPorts: "30000-30049"}, want: "30000-30049"},
		{name: "existing bridge range", cfg: Config{PublishedUDPPorts: "51820-51840"}, want: "51820-51840"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ranges, err := EffectiveTunnelUDPPortRanges(tt.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if got := FormatPortRanges(ranges); got != tt.want {
				t.Fatalf("ranges = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEffectiveTunnelUDPPortRangesRejectsDisjointPublishedRange(t *testing.T) {
	_, err := EffectiveTunnelUDPPortRanges(Config{
		TunnelUDPPortRange: "30000-30049",
		PublishedUDPPorts:  "40000-40049",
	})
	if err == nil {
		t.Fatal("expected disjoint ranges to fail")
	}
}
