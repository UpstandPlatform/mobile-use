package lan

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"
)

const (
	mdnsAddr        = "224.0.0.251:5353"
	serviceType     = "_mobile-use._tcp.local."
	dnsClassIN      = uint16(1)
	dnsClassINFlush = uint16(0x8001)
	dnsTypeA        = uint16(1)
	dnsTypePTR      = uint16(12)
	dnsTypeTXT      = uint16(16)
	dnsTypeSRV      = uint16(33)
	dnsTypeANY      = uint16(255)
)

// AdvertiseMDNS announces the companion HTTPS service on the local Wi-Fi network via mDNS/DNS-SD.
// It fails gracefully (returning false) if multicast UDP 5353 is unavailable.
func AdvertiseMDNS(ctx context.Context, hostName string, port int, ips []string, fingerprint string) bool {
	if port <= 0 || len(ips) == 0 {
		return false
	}
	var parsedIPs []net.IP
	for _, raw := range ips {
		if ip := net.ParseIP(raw).To4(); ip != nil {
			parsedIPs = append(parsedIPs, ip)
		}
	}
	if len(parsedIPs) == 0 {
		return false
	}
	maddr, err := net.ResolveUDPAddr("udp4", mdnsAddr)
	if err != nil {
		return false
	}
	conn, err := net.ListenMulticastUDP("udp4", nil, maddr)
	if err != nil {
		slog.Debug("mDNS multicast unavailable; QR and manual pairing still work", "error", err)
		return false
	}
	instanceLabel := sanitizeMDNSLabel(hostName)
	hostFQDN := instanceLabel + ".local."
	instanceFQDN := instanceLabel + "." + serviceType

	sendPacket := func(ttl uint32) {
		pkt := buildAnnouncementPacket(instanceFQDN, hostFQDN, hostName, uint16(port), parsedIPs, fingerprint, ttl)
		_, _ = conn.WriteToUDP(pkt, maddr)
	}

	go func() {
		defer conn.Close()
		sendPacket(120)
		buf := make([]byte, 1500)
		lastAnnounce := time.Now()
		for {
			if ctx.Err() != nil {
				sendPacket(0)
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, _, err := conn.ReadFromUDP(buf)
			if err == nil && n > 12 {
				if queriesForService(buf[:n], instanceFQDN, hostFQDN) {
					sendPacket(120)
				}
			}
			if time.Since(lastAnnounce) >= 45*time.Second {
				sendPacket(120)
				lastAnnounce = time.Now()
			}
		}
	}()
	return true
}

func sanitizeMDNSLabel(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else if r == ' ' || r == '_' || r == '.' {
			b.WriteByte('-')
		}
		if b.Len() >= 32 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "mobile-use"
	}
	return out
}

func encodeDNSName(name string) []byte {
	var buf bytes.Buffer
	trimmed := strings.TrimSuffix(name, ".")
	if trimmed != "" {
		for _, label := range strings.Split(trimmed, ".") {
			if len(label) > 63 {
				label = label[:63]
			}
			buf.WriteByte(byte(len(label)))
			buf.WriteString(label)
		}
	}
	buf.WriteByte(0)
	return buf.Bytes()
}

func decodeDNSName(pkt []byte, offset int) (string, int, bool) {
	var labels []string
	cur := offset
	next := -1
	for steps := 0; steps < 64 && cur < len(pkt); steps++ {
		l := int(pkt[cur])
		if l == 0 {
			if next < 0 {
				next = cur + 1
			}
			return strings.Join(labels, ".") + ".", next, true
		}
		if l&0xC0 == 0xC0 {
			if cur+1 >= len(pkt) {
				return "", 0, false
			}
			if next < 0 {
				next = cur + 2
			}
			cur = ((l & 0x3F) << 8) | int(pkt[cur+1])
			continue
		}
		cur++
		if cur+l > len(pkt) {
			return "", 0, false
		}
		labels = append(labels, string(pkt[cur:cur+l]))
		cur += l
	}
	return "", 0, false
}

func queriesForService(pkt []byte, instanceFQDN, hostFQDN string) bool {
	if len(pkt) < 12 {
		return false
	}
	flags := binary.BigEndian.Uint16(pkt[2:4])
	if flags&0x8000 != 0 {
		return false // Ignore responses.
	}
	qdCount := int(binary.BigEndian.Uint16(pkt[4:6]))
	if qdCount > 32 {
		qdCount = 32
	}
	off := 12
	for range qdCount {
		name, next, ok := decodeDNSName(pkt, off)
		if !ok || next+4 > len(pkt) {
			return false
		}
		qtype := binary.BigEndian.Uint16(pkt[next : next+2])
		off = next + 4
		lower := strings.ToLower(name)
		if (lower == serviceType || lower == strings.ToLower(instanceFQDN) || lower == strings.ToLower(hostFQDN)) &&
			(qtype == dnsTypePTR || qtype == dnsTypeSRV || qtype == dnsTypeTXT || qtype == dnsTypeA || qtype == dnsTypeANY) {
			return true
		}
	}
	return false
}

func appendRR(buf *bytes.Buffer, name string, rtype, rclass uint16, ttl uint32, rdata []byte) {
	buf.Write(encodeDNSName(name))
	_ = binary.Write(buf, binary.BigEndian, rtype)
	_ = binary.Write(buf, binary.BigEndian, rclass)
	_ = binary.Write(buf, binary.BigEndian, ttl)
	_ = binary.Write(buf, binary.BigEndian, uint16(len(rdata)))
	buf.Write(rdata)
}

func buildAnnouncementPacket(instanceFQDN, hostFQDN, hostName string, port uint16, ips []net.IP, fingerprint string, ttl uint32) []byte {
	var buf bytes.Buffer
	// Header: ID=0, Flags=0x8400 (QR=1, AA=1), QD=0, AN=3+len(ips), NS=0, AR=0
	var hdr [12]byte
	binary.BigEndian.PutUint16(hdr[2:4], 0x8400)
	binary.BigEndian.PutUint16(hdr[6:8], uint16(3+len(ips)))
	buf.Write(hdr[:])

	// PTR: _mobile-use._tcp.local. -> <instance>._mobile-use._tcp.local.
	appendRR(&buf, serviceType, dnsTypePTR, dnsClassIN, ttl, encodeDNSName(instanceFQDN))

	// SRV: <instance> -> port, <host>.local.
	var srv bytes.Buffer
	_ = binary.Write(&srv, binary.BigEndian, uint16(0)) // priority
	_ = binary.Write(&srv, binary.BigEndian, uint16(0)) // weight
	_ = binary.Write(&srv, binary.BigEndian, port)
	srv.Write(encodeDNSName(hostFQDN))
	appendRR(&buf, instanceFQDN, dnsTypeSRV, dnsClassINFlush, ttl, srv.Bytes())

	// TXT: version, display name, cert fingerprint
	var txt bytes.Buffer
	entries := []string{
		"v=1",
		"name=" + hostName,
	}
	if fingerprint != "" {
		entries = append(entries, "fp="+fingerprint)
	}
	for _, entry := range entries {
		if len(entry) > 250 {
			entry = entry[:250]
		}
		txt.WriteByte(byte(len(entry)))
		txt.WriteString(entry)
	}
	appendRR(&buf, instanceFQDN, dnsTypeTXT, dnsClassINFlush, ttl, txt.Bytes())

	// A records for each LAN IP
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			appendRR(&buf, hostFQDN, dnsTypeA, dnsClassINFlush, ttl, v4)
		}
	}
	_ = fmt.Sprintf
	return buf.Bytes()
}
