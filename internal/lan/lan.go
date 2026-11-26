package lan

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// Address describes a reachable local network IPv4 address for LAN pairing.
type Address struct {
	IP        string `json:"ip"`
	Interface string `json:"interface"`
	Primary   bool   `json:"primary"`
}

var virtualPrefixes = []string{"docker", "br-", "veth", "wsl", "vethernet", "vboxnet", "vmnet", "virbr", "zt", "tailscale", "tun", "tap"}

func isVirtualInterface(name string) bool {
	lower := strings.ToLower(name)
	for _, prefix := range virtualPrefixes {
		if strings.HasPrefix(lower, prefix) || strings.Contains(lower, "wsl") || strings.Contains(lower, "hyper-v") {
			return true
		}
	}
	return false
}

func primaryOutboundIP() string {
	conn, err := net.DialTimeout("udp4", "192.0.2.1:80", 200*time.Millisecond)
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil || addr.IP.IsLoopback() || addr.IP.IsLinkLocalUnicast() {
		return ""
	}
	if v4 := addr.IP.To4(); v4 != nil {
		return v4.String()
	}
	return ""
}

// DetectAddresses returns active non-loopback IPv4 addresses sorted with the primary LAN route first.
func DetectAddresses() []Address {
	primary := primaryOutboundIP()
	ifaces, err := net.Interfaces()
	if err != nil {
		if primary != "" {
			return []Address{{IP: primary, Interface: "default", Primary: true}}
		}
		return nil
	}
	type candidate struct {
		Address
		virtual bool
		private bool
	}
	seen := make(map[string]bool)
	var list []candidate
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		virtual := isVirtualInterface(iface.Name)
		for _, raw := range addrs {
			ipNet, ok := raw.(*net.IPNet)
			if !ok || ipNet.IP == nil {
				continue
			}
			v4 := ipNet.IP.To4()
			if v4 == nil || v4.IsLoopback() || v4.IsLinkLocalUnicast() || v4.IsMulticast() || v4.IsUnspecified() {
				continue
			}
			ipStr := v4.String()
			if seen[ipStr] {
				continue
			}
			seen[ipStr] = true
			list = append(list, candidate{
				Address: Address{IP: ipStr, Interface: iface.Name, Primary: ipStr == primary},
				virtual: virtual,
				private: v4.IsPrivate(),
			})
		}
	}
	if primary != "" && !seen[primary] {
		list = append(list, candidate{Address: Address{IP: primary, Interface: "default", Primary: true}, private: true})
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Primary != b.Primary {
			return a.Primary
		}
		if a.virtual != b.virtual {
			return !a.virtual
		}
		if a.private != b.private {
			return a.private
		}
		return a.IP < b.IP
	})
	if len(list) > 0 && !list[0].Primary {
		list[0].Primary = true
	}
	result := make([]Address, len(list))
	for i, item := range list {
		result[i] = item.Address
	}
	return result
}

// HostLabel returns a short display name for the local computer.
func HostLabel() string {
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		return "mobile-use workstation"
	}
	name = strings.TrimSuffix(strings.TrimSpace(name), ".local")
	if len(name) > 48 {
		name = name[:48]
	}
	return name
}

// FormatFingerprint formats a 64-char hex SHA-256 certificate digest as an easy-to-verify prefix.
func FormatFingerprint(hexDigest string) string {
	clean := strings.ToUpper(strings.TrimSpace(hexDigest))
	if len(clean) < 16 {
		return clean
	}
	var parts []string
	for i := 0; i < 16; i += 2 {
		parts = append(parts, clean[i:i+2])
	}
	return strings.Join(parts, ":")
}

// EnsureCompanionTLS loads or issues a persistent ECDSA P-256 certificate for LAN companion HTTPS.
// It returns the certificate path, key path, and lowercase 64-char SHA-256 DER fingerprint.
func EnsureCompanionTLS(dir string, ips []string) (string, string, string, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", "", "", err
	}
	certPath := filepath.Join(dir, "companion.crt")
	keyPath := filepath.Join(dir, "companion.key")

	desiredIPs := []string{"127.0.0.1", "::1"}
	for _, raw := range ips {
		if ip := net.ParseIP(raw); ip != nil && !slices.Contains(desiredIPs, ip.String()) {
			desiredIPs = append(desiredIPs, ip.String())
		}
	}

	if fp, ok := validExistingCert(certPath, keyPath, desiredIPs); ok {
		return certPath, keyPath, fp, nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", "", err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "mobile-use local companion"},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}
	if host, err := os.Hostname(); err == nil && host != "" && !strings.ContainsAny(host, " /@*:") {
		if !slices.Contains(template.DNSNames, host) {
			template.DNSNames = append(template.DNSNames, host)
		}
		if !strings.HasSuffix(host, ".local") {
			template.DNSNames = append(template.DNSNames, host+".local")
		}
	}
	for _, raw := range desiredIPs {
		if ip := net.ParseIP(raw); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return "", "", "", err
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", "", err
	}
	if err := writeReplace(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), 0600); err != nil {
		return "", "", "", err
	}
	if err := writeReplace(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644); err != nil {
		return "", "", "", err
	}
	sum := sha256.Sum256(der)
	return certPath, keyPath, hex.EncodeToString(sum[:]), nil
}

func validExistingCert(certPath, keyPath string, requiredIPs []string) (string, bool) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return "", false
	}
	// The key and certificate are replaced in two steps; a partial write leaves a pair that
	// the TLS listener cannot load, so reuse only a matching, loadable pair.
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		return "", false
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return "", false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", false
	}
	now := time.Now()
	if now.Before(cert.NotBefore) || cert.NotAfter.Before(now.Add(14*24*time.Hour)) {
		return "", false
	}
	have := make(map[string]bool, len(cert.IPAddresses))
	for _, ip := range cert.IPAddresses {
		have[ip.String()] = true
	}
	for _, req := range requiredIPs {
		if !have[req] {
			return "", false
		}
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:]), true
}

func writeReplace(path string, data []byte, perms os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perms); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		if errors.Is(err, os.ErrPermission) {
			return os.WriteFile(path, data, perms)
		}
		return err
	}
	return nil
}
