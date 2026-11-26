package service_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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

func TestCompanionFrameStreamPushesFramesWithoutPollingOperations(t *testing.T) {
	f := setup(t)
	code := request[map[string]any](t, f.owner, "POST", "/v1/companion/pairing", nil)["code"].(string)
	var enrolled map[string]string
	opening := []byte(wire.Marshal(map[string]string{"code": code, "label": "Streaming phone"}))
	if err := json.Unmarshal(phonePost(t, f.origin, "", "/companion/enroll", "application/json", opening, 201), &enrolled); err != nil {
		t.Fatal(err)
	}
	token, deviceID := enrolled["token"], enrolled["device_id"]
	phonePost(t, f.origin, token, "/companion/heartbeat", "application/json", []byte(`{"present_session":""}`), 200)
	caps := []wire.Capability{wire.Screen, wire.Camera, wire.Microphone}
	enroll(t, f, deviceID, wire.Personal, caps)
	session := request[wire.Session](t, f.agent, "POST", "/v1/sessions", wire.StartSession{DeviceID: deviceID, Purpose: "Verify pushed screen frames", Capabilities: caps})
	phonePost(t, f.origin, token, "/companion/sessions/"+session.ID+"/approve", "application/json", []byte(`{}`), 200)

	deniedReq, err := http.NewRequest(http.MethodGet, f.origin+"/v1/companion/sessions/"+session.ID+"/frames", nil)
	if err != nil {
		t.Fatal(err)
	}
	deniedReq.Header.Set("Authorization", "Bearer other-workspace-test-token")
	deniedResp, err := http.DefaultClient.Do(deniedReq)
	if err != nil {
		t.Fatal(err)
	}
	deniedResp.Body.Close()
	if deniedResp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-workspace frame stream returned %d, want 404", deniedResp.StatusCode)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.origin+"/v1/companion/sessions/"+session.ID+"/frames", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tokenAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("frame stream returned %d (%q)", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(resp.Body)
	if waiting := readSSEBlock(t, reader); !strings.Contains(waiting, "event: status") || !strings.Contains(waiting, `"state":"waiting"`) {
		t.Fatalf("stream did not report its initial waiting state: %q", waiting)
	}

	var picture bytes.Buffer
	if err := jpeg.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 80, 120)), nil); err != nil {
		t.Fatal(err)
	}
	phonePost(t, f.origin, token, "/companion/sessions/"+session.ID+"/frame", "image/jpeg", picture.Bytes(), 204)
	frame := readSSEBlock(t, reader)
	if !strings.Contains(frame, "event: frame") || !strings.Contains(frame, `"image":{"mime":"image/jpeg","data":"`) {
		t.Fatalf("stream did not push the uploaded JPEG: %q", frame)
	}

	// Confirm a second frame is pushed independently of the operation endpoint.
	time.Sleep(210 * time.Millisecond)
	phonePost(t, f.origin, token, "/companion/sessions/"+session.ID+"/frame", "image/jpeg", picture.Bytes(), 204)
	second := readSSEBlock(t, reader)
	if !strings.Contains(second, "event: frame") || !strings.Contains(second, `"frame_id"`) {
		t.Fatalf("stream did not push a subsequent frame: %q", second)
	}
}

func readSSEBlock(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	var block strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		block.WriteString(line)
		if strings.TrimSpace(line) == "" {
			return block.String()
		}
	}
}
