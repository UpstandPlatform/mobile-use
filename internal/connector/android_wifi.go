package connector

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mobile-use/internal/wire"
)

// WirelessService describes an Android Wireless Debugging endpoint discovered via adb mDNS.
type WirelessService struct {
	Name    string `json:"name"`
	Type    string `json:"type"` // "pairing" or "connect"
	Address string `json:"address"`
}

var (
	wirelessCodeMatch = regexp.MustCompile(`^[0-9]{6}$`)
	validHostMatch    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)
)

func ValidateWirelessAddress(address string) (string, error) {
	trimmed := strings.TrimSpace(address)
	if trimmed == "" || strings.HasPrefix(trimmed, "-") || strings.ContainsAny(trimmed, " \t\r\n") {
		return "", wire.Error("INVALID_ADDRESS", "Enter an address in HOST:PORT format.", 400)
	}
	host, portStr, err := net.SplitHostPort(trimmed)
	if err != nil || host == "" {
		return "", wire.Error("INVALID_ADDRESS", "Enter an address in HOST:PORT format.", 400)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", wire.Error("INVALID_ADDRESS", "Enter a valid TCP port (1-65535).", 400)
	}
	if ip := net.ParseIP(host); ip == nil && !validHostMatch.MatchString(host) {
		return "", wire.Error("INVALID_ADDRESS", "Enter a valid IP address or hostname.", 400)
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func ValidateWirelessCode(code string) (string, error) {
	clean := strings.TrimSpace(code)
	if !wirelessCodeMatch.MatchString(clean) {
		return "", wire.Error("INVALID_PAIRING_CODE", "Enter the 6-digit Wi-Fi pairing code shown on the phone.", 400)
	}
	return clean, nil
}

// DiscoverWireless queries `adb mdns services` for Android Wireless Debugging pairing and connect services.
func (a Android) DiscoverWireless(ctx context.Context) ([]WirelessService, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	out, err := runProcess(ctx, a.path(), "mdns", "services")
	if err != nil {
		return nil, err
	}
	return parseMDNSServices(string(out)), nil
}

func parseMDNSServices(raw string) []WirelessService {
	var services []WirelessService
	seen := make(map[string]bool)
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		name, svcType, addr := fields[0], fields[1], fields[2]
		kind := ""
		switch {
		case strings.Contains(svcType, "_adb-tls-pairing._tcp"):
			kind = "pairing"
		case strings.Contains(svcType, "_adb-tls-connect._tcp"):
			kind = "connect"
		default:
			continue
		}
		normAddr, err := ValidateWirelessAddress(addr)
		if err != nil {
			continue
		}
		key := kind + "/" + normAddr
		if seen[key] {
			continue
		}
		seen[key] = true
		services = append(services, WirelessService{Name: name, Type: kind, Address: normAddr})
	}
	return services
}

// PairWireless pairs ADB over Wi-Fi using Android's 6-digit Wireless Debugging pairing code.
func (a Android) PairWireless(ctx context.Context, address, code string) (string, error) {
	addr, err := ValidateWirelessAddress(address)
	if err != nil {
		return "", err
	}
	cleanCode, err := ValidateWirelessCode(code)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	out, err := runCombinedBounded(ctx, a.path(), "pair", addr, cleanCode)
	text := strings.TrimSpace(string(out))
	if err != nil || !strings.Contains(strings.ToLower(text), "successfully paired") {
		if text == "" && err != nil {
			return "", err
		}
		if text == "" {
			text = "ADB pairing did not succeed. Check the host, port, and 6-digit code."
		}
		return "", wire.Error("ADB_PAIR_FAILED", text, 400)
	}
	return text, nil
}

// ConnectWireless connects ADB to a paired Android device over Wi-Fi.
func (a Android) ConnectWireless(ctx context.Context, address string) (string, error) {
	addr, err := ValidateWirelessAddress(address)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := runCombinedBounded(ctx, a.path(), "connect", addr)
	text := strings.TrimSpace(string(out))
	lower := strings.ToLower(text)
	if err != nil || (!strings.Contains(lower, "connected to") && !strings.Contains(lower, "already connected")) {
		if text == "" && err != nil {
			return "", err
		}
		if text == "" {
			text = "Could not connect to the Android device over Wi-Fi."
		}
		return "", wire.Error("ADB_CONNECT_FAILED", text, 400)
	}
	return text, nil
}

// DisconnectWireless disconnects an ADB Wi-Fi target.
func (a Android) DisconnectWireless(ctx context.Context, address string) error {
	addr, err := ValidateWirelessAddress(address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	_, err = runCombinedBounded(ctx, a.path(), "disconnect", addr)
	return err
}

func runCombinedBounded(ctx context.Context, path string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = time.Second
	var buf boundedBuffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			return buf.Bytes(), wire.Error("ADB_TIMEOUT", "The ADB wireless operation timed out.", 504)
		}
		var missing *exec.Error
		if errors.As(err, &missing) {
			return nil, wire.Error("TOOL_MISSING", "Android platform-tools (adb) was not found in PATH.", 503)
		}
		return buf.Bytes(), err
	}
	return buf.Bytes(), nil
}

type boundedBuffer struct{ buffer bytes.Buffer }

func (b *boundedBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	const max = 64 << 10
	if b.buffer.Len() >= max {
		return len(p), nil
	}
	remain := max - b.buffer.Len()
	if len(p) > remain {
		_, _ = b.buffer.Write(p[:remain])
		return len(p), nil
	}
	_, _ = io.Copy(&b.buffer, bytes.NewReader(p))
	return len(p), nil
}
