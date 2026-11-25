package connector

import "testing"

func TestParseMDNSServices(t *testing.T) {
	raw := `List of discovered mdns services
adb-123456-ABCDEF	_adb-tls-pairing._tcp.	192.168.1.42:37199
adb-123456-ABCDEF	_adb-tls-connect._tcp.	192.168.1.42:41235
adb-123456-ABCDEF	_adb-tls-connect._tcp.	192.168.1.42:41235
other-service	_http._tcp.	192.168.1.1:80
`
	services := parseMDNSServices(raw)
	if len(services) != 2 {
		t.Fatalf("got %d services, want 2: %+v", len(services), services)
	}
	if services[0].Type != "pairing" || services[0].Address != "192.168.1.42:37199" {
		t.Fatalf("unexpected first service: %+v", services[0])
	}
	if services[1].Type != "connect" || services[1].Address != "192.168.1.42:41235" {
		t.Fatalf("unexpected second service: %+v", services[1])
	}
}

func TestValidateWirelessInputs(t *testing.T) {
	goodAddrs := []string{"192.168.1.10:5555", "phone.local:37199", " [::1]:5555 "}
	for _, addr := range goodAddrs {
		if _, err := ValidateWirelessAddress(addr); err != nil {
			t.Fatalf("valid address %q rejected: %v", addr, err)
		}
	}
	badAddrs := []string{"", "192.168.1.10", "-s 192.168.1.10:5555", "192.168.1.10:0", "192.168.1.10:70000", "192.168.1.10:5555;ls"}
	for _, addr := range badAddrs {
		if _, err := ValidateWirelessAddress(addr); err == nil {
			t.Fatalf("bad address %q accepted", addr)
		}
	}
	if code, err := ValidateWirelessCode(" 123456 "); err != nil || code != "123456" {
		t.Fatalf("valid code rejected: %v", err)
	}
	for _, bad := range []string{"12345", "1234567", "12345a", "-12345"} {
		if _, err := ValidateWirelessCode(bad); err == nil {
			t.Fatalf("bad code %q accepted", bad)
		}
	}
}
