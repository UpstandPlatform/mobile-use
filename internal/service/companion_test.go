package service_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"mobile-use/internal/service"
	"mobile-use/internal/store"
	"mobile-use/internal/wire"
)

func TestNormalizePairingCodeAndBuildURI(t *testing.T) {
	if norm, ok := service.NormalizePairingCode(" k7m9-p4x2 "); !ok || norm != "K7M9P4X2" {
		t.Fatalf("NormalizePairingCode short failed: %q %v", norm, ok)
	}
	if norm, ok := service.NormalizePairingCode("0123456789ABCDEF0123456789abcdef"); !ok || norm != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("NormalizePairingCode legacy hex failed: %q %v", norm, ok)
	}
	for _, bad := range []string{"", "SHORT", "K7M9-P4X!", "0000-1111"} {
		if _, ok := service.NormalizePairingCode(bad); ok {
			t.Fatalf("expected invalid code %q to fail", bad)
		}
	}

	lanURI := service.BuildPairingURI("https://192.168.1.10:8444", "K7M9-P4X2", "abcd1234", "Desk", "lan")
	u, err := url.Parse(lanURI)
	if err != nil || u.Scheme != "mobile-use" || u.Host != "pair" {
		t.Fatalf("invalid URI: %q (%v)", lanURI, err)
	}
	q := u.Query()
	if q.Get("o") != "https://192.168.1.10:8444" || q.Get("c") != "K7M9P4X2" || q.Get("p") != "abcd1234" || q.Get("n") != "Desk" || q.Get("m") != "lan" {
		t.Fatalf("unexpected query params: %v", q)
	}

	remoteURI := service.BuildPairingURI("https://mobile-use.example.com", "K7M9-P4X2", "", "Prod", "remote")
	ru, _ := url.Parse(remoteURI)
	if ru.Query().Has("p") {
		t.Fatalf("remote URI without fingerprint should omit p param: %q", remoteURI)
	}
}

func TestCompanionAutoEnrollAndPairingStatus(t *testing.T) {
	f := setup(t)
	created := request[map[string]any](t, f.owner, "POST", "/v1/companion/pairing", map[string]any{
		"auto_enroll":  true,
		"grant_agents": []string{"agent"},
	})
	pairID := created["id"].(string)
	code := created["code"].(string)
	if !strings.Contains(code, "-") || len(code) != 9 {
		t.Fatalf("expected XXXX-XXXX pairing code, got %q", code)
	}
	if !created["auto_enroll"].(bool) {
		t.Fatal("expected auto_enroll=true")
	}

	status := request[map[string]any](t, f.owner, "GET", "/v1/companion/pairing/"+pairID, nil)
	if status["state"] != "waiting" {
		t.Fatalf("expected waiting state, got %+v", status)
	}

	// Enroll from phone without dashes/mixed case.
	compact := strings.ToLower(strings.ReplaceAll(code, "-", ""))
	body := []byte(wire.Marshal(map[string]string{"code": compact, "label": "Pixel 9"}))
	raw := phonePost(t, f.origin, "", "/companion/enroll", "application/json", body, 201)
	var enrolled map[string]any
	if err := json.Unmarshal(raw, &enrolled); err != nil {
		t.Fatal(err)
	}
	if enrolled["auto_enrolled"] != true {
		t.Fatalf("expected auto_enrolled=true, got %+v", enrolled)
	}
	deviceID := enrolled["device_id"].(string)

	// Status should transition to paired.
	status = request[map[string]any](t, f.owner, "GET", "/v1/companion/pairing/"+pairID, nil)
	if status["state"] != "paired" || status["device_id"] != deviceID || status["auto_enrolled"] != true {
		t.Fatalf("unexpected paired status: %+v", status)
	}

	// Agent should immediately see the auto-enrolled device without extra PUT /v1/devices/{id}.
	agentDevices := request[[]wire.Device](t, f.agent, "GET", "/v1/devices", nil)
	var found bool
	for _, dev := range agentDevices {
		if dev.ID == deviceID && dev.Online && dev.Enabled {
			found = true
		}
	}
	if !found {
		t.Fatalf("auto-enrolled companion not visible to granted agent: %+v", agentDevices)
	}
}

func TestCompanionMultiOriginPhoneHandlerAndRateLimit(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, t.TempDir()+"/store.sqlite", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := service.NewCompanion(db, "https://192.168.1.10:8444")
	c.AllowedOrigins = []string{"https://192.168.1.10:8444", "https://10.0.0.5:8444"}
	handler := c.PhoneHandler()

	// Accepted Host header on secondary LAN interface.
	req := httptest.NewRequest("POST", "https://10.0.0.5:8444/companion/enroll", strings.NewReader(`{"code":"BAD","label":"Phone"}`))
	req.RemoteAddr = "10.0.0.99:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("expected 400 INVALID_PAIRING for secondary allowed host, got %d", rec.Code)
	}

	// Unknown Host rejected by perimeter.
	badHost := httptest.NewRequest("POST", "https://evil.example.com:8444/companion/enroll", strings.NewReader(`{"code":"BAD","label":"Phone"}`))
	badHost.RemoteAddr = "10.0.0.99:12345"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, badHost)
	if rec2.Code != 400 {
		t.Fatalf("expected 400 INVALID_HOST, got %d", rec2.Code)
	}

	// Rate limit kicks in after 15 attempts from same IP.
	for i := range 14 {
		r := httptest.NewRequest("POST", "https://192.168.1.10:8444/companion/enroll", strings.NewReader(`{"code":"BAD","label":"Phone"}`))
		r.RemoteAddr = "10.0.0.99:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("attempt %d returned %d, want 400", i+2, w.Code)
		}
	}
	r16 := httptest.NewRequest("POST", "https://192.168.1.10:8444/companion/enroll", strings.NewReader(`{"code":"BAD","label":"Phone"}`))
	r16.RemoteAddr = "10.0.0.99:12345"
	w16 := httptest.NewRecorder()
	handler.ServeHTTP(w16, r16)
	if w16.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 RATE_LIMITED on 16th attempt, got %d", w16.Code)
	}
}
