package qr

import (
	"strings"
	"testing"
)

func TestEncodeBasicAndPairingURI(t *testing.T) {
	code, err := Encode("hello")
	if err != nil {
		t.Fatal(err)
	}
	if code.Version != 1 || code.Size != 21 {
		t.Fatalf("got version=%d size=%d, want 1/21", code.Version, code.Size)
	}
	// Verify top-left finder outer ring and center.
	for i := range 7 {
		if !code.At(i, 0) || !code.At(i, 6) || !code.At(0, i) || !code.At(6, i) {
			t.Fatalf("missing finder border at %d", i)
		}
	}
	if !code.At(3, 3) || code.At(1, 1) {
		t.Fatal("invalid finder interior")
	}
	// Dark module always at (8, Size-8).
	if !code.At(8, code.Size-8) {
		t.Fatal("missing dark module")
	}

	uri := "mobile-use://pair?o=https%3A%2F%2F192.168.1.42%3A8444&c=K7M9P4X2&p=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef&n=Workstation&m=lan"
	pairCode, err := Encode(uri)
	if err != nil {
		t.Fatal(err)
	}
	if pairCode.Version < 7 {
		t.Fatalf("expected version >= 7 for %d-byte pairing URI, got %d", len(uri), pairCode.Version)
	}
	term := pairCode.Terminal(true)
	if !strings.Contains(term, "\x1b[30;47m") || !strings.Contains(term, "█") {
		t.Fatal("unexpected terminal QR output")
	}
	plain := pairCode.Terminal(false)
	if strings.Contains(plain, "\x1b[") {
		t.Fatal("plain terminal output should not contain ANSI escapes")
	}
}

func TestEncodeErrors(t *testing.T) {
	if _, err := Encode(""); err == nil {
		t.Fatal("expected error for empty payload")
	}
	if _, err := Encode(strings.Repeat("a", 500)); err == nil {
		t.Fatal("expected error for oversized payload")
	}
}

func TestBCHFormatAndVersion(t *testing.T) {
	// ISO/IEC 18004 standard check: EC level M (00), mask 0 -> 101010000010010 (0x5412).
	if got := bchFormat(0b00, 0); got != 0x5412 {
		t.Fatalf("bchFormat(00, 0) = %#x, want 0x5412", got)
	}
	// Version 7 check: 000111110010010100 = 0x07c94.
	if got := bchVersion(7); got != 0x07c94 {
		t.Fatalf("bchVersion(7) = %#x, want 0x07c94", got)
	}
}
