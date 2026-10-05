package controlpki

import "testing"

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		name, bind, advertised string
		port                   int
		valid                  bool
	}{
		{"dns", "127.0.0.1", "control.example.com", 8443, true},
		{"ip", "::1", "2001:db8::1", 8443, true},
		{"url", "127.0.0.1", "https://example.com", 8443, false},
		{"wildcard bind", "0.0.0.0", "control.example.com", 8443, false},
		{"mapped wildcard bind", "::ffff:0.0.0.0", "control.example.com", 8443, false},
		{"unspecified advertised", "127.0.0.1", "0.0.0.0", 8443, false},
		{"mapped advertised", "127.0.0.1", "::ffff:192.0.2.1", 8443, false},
		{"zone", "fe80::1%eth0", "control.example.com", 8443, false},
		{"ambiguous dns", "127.0.0.1", "localhost", 8443, false},
		{"wildcard dns", "127.0.0.1", "*.example.com", 8443, false},
		{"web port", "127.0.0.1", "control.example.com", 51821, false},
		{"acme port", "127.0.0.1", "control.example.com", 80, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NormalizeEndpoint(tt.bind, tt.advertised, tt.port, 51821)
			if (err == nil) != tt.valid {
				t.Fatalf("NormalizeEndpoint error = %v, valid = %v", err, tt.valid)
			}
		})
	}
}

func TestValidateControllerURLExternalAndUnsafeForms(t *testing.T) {
	for _, value := range []string{"https://127.0.0.1:8443", "https://[::1]:8443/", "https://192.0.2.10:8443", "https://[2001:db8::10]:8443", "https://control.example.test:8443"} {
		if err := ValidateControllerURL(value); err != nil {
			t.Fatalf("valid URL %q: %v", value, err)
		}
	}
	for _, value := range []string{"http://192.0.2.10:8443", "https://control.example.test", "https://control.example.test:0", "https://control.example.test:65536", "https://control.example.test:080", "https://control.example.test:80", "https://user@control.example.test:8443", "https://control.example.test:8443?", "https://control.example.test:8443?token=x", "https://control.example.test:8443/#x", "https://control.example.test:8443/%2f", "https://control.example.test:8443/other", "https://0.0.0.0:8443", "https://[::]:8443", "https://224.0.0.1:8443", "https://[fe80::1%25eth0]:8443", "https://[::ffff:192.0.2.10]:8443", "https://*.example.test:8443", "https://[192.0.2.10]:8443"} {
		if err := ValidateControllerURL(value); err == nil {
			t.Fatalf("unsafe URL %q accepted", value)
		}
	}
}
