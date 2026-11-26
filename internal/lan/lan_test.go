package lan

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"net"
	"os"
	"testing"
)

func TestEnsureCompanionTLSReuseAndRegen(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, fp1, err := EnsureCompanionTLS(dir, []string{"192.168.1.50"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fp1) != 64 {
		t.Fatalf("expected 64-char hex fingerprint, got %q", fp1)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	var hasLAN bool
	for _, ip := range cert.IPAddresses {
		if ip.Equal(net.ParseIP("192.168.1.50")) {
			hasLAN = true
		}
	}
	if !hasLAN {
		t.Fatal("certificate missing requested LAN IP SAN")
	}

	// Same IPs should reuse existing cert and fingerprint.
	_, _, fp2, err := EnsureCompanionTLS(dir, []string{"192.168.1.50"})
	if err != nil {
		t.Fatal(err)
	}
	if fp1 != fp2 {
		t.Fatalf("expected reused certificate fingerprint %s, got %s", fp1, fp2)
	}

	// New IP should regenerate certificate with updated SANs.
	_, _, fp3, err := EnsureCompanionTLS(dir, []string{"192.168.1.50", "10.0.0.42"})
	if err != nil {
		t.Fatal(err)
	}
	if fp3 == fp1 {
		t.Fatal("expected regenerated certificate when new IP added")
	}
}

func TestEnsureCompanionTLSRegeneratesMismatchedKey(t *testing.T) {
	dir := t.TempDir()
	_, keyPath, fp1, err := EnsureCompanionTLS(dir, []string{"192.168.1.50"})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an interrupted rotation: a new key was written but the certificate was not.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), 0600); err != nil {
		t.Fatal(err)
	}
	certPath, keyPath, fp2, err := EnsureCompanionTLS(dir, []string{"192.168.1.50"})
	if err != nil {
		t.Fatal(err)
	}
	if fp2 == fp1 {
		t.Fatal("expected a new certificate when the stored key does not match")
	}
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		t.Fatalf("regenerated pair must load: %v", err)
	}
}

func TestFormatFingerprint(t *testing.T) {
	got := FormatFingerprint("a1b2c3d4e5f60718999999999999999999999999999999999999999999999999")
	if got != "A1:B2:C3:D4:E5:F6:07:18" {
		t.Fatalf("got %q", got)
	}
}

func TestMDNSPacketAndQueryMatch(t *testing.T) {
	pkt := buildAnnouncementPacket("desk._mobile-use._tcp.local.", "desk.local.", "Desk", 8444, []net.IP{net.ParseIP("192.168.1.10")}, "abcd", 120)
	if len(pkt) < 40 {
		t.Fatalf("packet too short: %d", len(pkt))
	}
	if binary.BigEndian.Uint16(pkt[6:8]) != 4 {
		t.Fatalf("expected 4 answer records, got %d", binary.BigEndian.Uint16(pkt[6:8]))
	}

	// Build a minimal DNS query for _mobile-use._tcp.local. PTR.
	var q bytes.Buffer
	q.Write(make([]byte, 12))
	q.Bytes()[5] = 1
	q.Write(encodeDNSName("_mobile-use._tcp.local."))
	_ = binary.Write(&q, binary.BigEndian, dnsTypePTR)
	_ = binary.Write(&q, binary.BigEndian, dnsClassIN)
	if !queriesForService(q.Bytes(), "desk._mobile-use._tcp.local.", "desk.local.") {
		t.Fatal("expected query for _mobile-use._tcp.local. to match")
	}
}
