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
