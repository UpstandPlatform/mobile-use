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

	"github.com/gorilla/websocket"
	"mobile-use/internal/client"
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
	if status["state"] != "waiting" || status["auto_enroll"] != true {
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
	if status["state"] != "paired" || status["device_id"] != deviceID || status["auto_enroll"] != true || status["auto_enrolled"] != true {
		t.Fatalf("unexpected paired status: %+v", status)
	}
	phonePost(t, f.origin, "", "/companion/enroll", "application/json", body, http.StatusForbidden)

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

func TestConsoleSessionRequestAppearsInPairedPhoneHeartbeat(t *testing.T) {
	f := setup(t)
	pairing := request[map[string]any](t, f.owner, http.MethodPost, "/v1/companion/pairing", map[string]any{
		"auto_enroll": true,
	})
	opening := []byte(wire.Marshal(map[string]string{
		"code":  pairing["code"].(string),
		"label": "Approval feed phone",
	}))
	var enrolled struct {
		DeviceID     string `json:"device_id"`
		Token        string `json:"token"`
		AutoEnrolled bool   `json:"auto_enrolled"`
	}
	if err := json.Unmarshal(phonePost(t, f.origin, "", "/companion/enroll", "application/json", opening, http.StatusCreated), &enrolled); err != nil {
		t.Fatal(err)
	}
	if !enrolled.AutoEnrolled {
		t.Fatal("test companion did not auto-enroll")
	}
	deviceID, phoneToken := enrolled.DeviceID, enrolled.Token
	phonePost(t, f.origin, phoneToken, "/companion/heartbeat", "application/json", []byte(`{"present_session":""}`), http.StatusOK)

	// The console and CLI both create sessions through this authenticated API route.
	session := request[wire.Session](t, f.owner, http.MethodPost, "/v1/sessions", wire.StartSession{
		DeviceID: deviceID, Purpose: "Verify console approval notification", Capabilities: []wire.Capability{wire.Screen},
	})
	if session.State != wire.Pending {
		t.Fatalf("new access request state = %q, want %q", session.State, wire.Pending)
	}

	var heartbeat struct {
		Sessions []wire.Session `json:"sessions"`
	}
	if err := json.Unmarshal(phonePost(t, f.origin, phoneToken, "/companion/heartbeat", "application/json", []byte(`{"present_session":""}`), http.StatusOK), &heartbeat); err != nil {
		t.Fatal(err)
	}
	for _, pending := range heartbeat.Sessions {
		if pending.ID == session.ID && pending.DeviceID == deviceID && pending.State == wire.Pending {
			return
		}
	}
	t.Fatalf("paired phone heartbeat did not include the console request %q: %+v", session.ID, heartbeat.Sessions)
}

func TestCompanionPairingSurvivesRestartAndCanBeCancelled(t *testing.T) {
	f := setup(t)
	actor := wire.Actor{Workspace: f.workspace, Subject: "owner", Role: wire.Admin}
	newCompanionServer := func() (*httptest.Server, *client.Client) {
		t.Helper()
		auth := &service.Auth{Static: []service.Principal{{Token: tokenOwner, Actor: actor}}}
		companion := service.NewCompanion(f.db, f.origin)
		mux := http.NewServeMux()
		companion.ConsoleRoutes(mux, auth, f.origin)
		companion.PhoneRoutes(mux)
		server := httptest.NewServer(mux)
		owner, err := client.New(server.URL, tokenOwner)
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		return server, owner
	}

	first, owner := newCompanionServer()
	created := request[map[string]any](t, owner, "POST", "/v1/companion/pairing", map[string]any{
		"auto_enroll":  true,
		"grant_agents": []string{"agent"},
	})
	first.Close()

	restarted, ownerAfterRestart := newCompanionServer()
	defer restarted.Close()
	pairingID := created["id"].(string)
	status := request[map[string]any](t, ownerAfterRestart, "GET", "/v1/companion/pairing/"+pairingID, nil)
	if status["state"] != "waiting" {
		t.Fatalf("pairing state after service restart = %v, want waiting", status["state"])
	}

	var enrolled map[string]any
	raw := phonePost(t, restarted.URL, "", "/companion/enroll", "application/json", []byte(wire.Marshal(map[string]string{
		"code": created["code"].(string), "label": "Restarted pairing phone",
	})), http.StatusCreated)
	if err := json.Unmarshal(raw, &enrolled); err != nil {
		t.Fatal(err)
	}
	if enrolled["auto_enrolled"] != true {
		t.Fatalf("auto-enrollment did not complete after restart: %+v", enrolled)
	}
	status = request[map[string]any](t, ownerAfterRestart, "GET", "/v1/companion/pairing/"+pairingID, nil)
	if status["state"] != "paired" || status["device_id"] != enrolled["device_id"] || status["auto_enrolled"] != true {
		t.Fatalf("pairing status was not durable: %+v", status)
	}

	cancelled := request[map[string]any](t, ownerAfterRestart, "POST", "/v1/companion/pairing", nil)
	if err := ownerAfterRestart.Request(t.Context(), "DELETE", "/v1/companion/pairing/"+cancelled["id"].(string), nil, nil); err != nil {
		t.Fatalf("cancel pairing: %v", err)
	}
	status = request[map[string]any](t, ownerAfterRestart, "GET", "/v1/companion/pairing/"+cancelled["id"].(string), nil)
	if status["state"] != "cancelled" {
		t.Fatalf("cancelled pairing state = %v, want cancelled", status["state"])
	}
	phonePost(t, restarted.URL, "", "/companion/enroll", "application/json", []byte(wire.Marshal(map[string]string{
		"code": cancelled["code"].(string), "label": "Should not enroll",
	})), http.StatusForbidden)
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

func TestCompanionFrameWebSocketAcksFramesAndFeedsConsole(t *testing.T) {
	f := setup(t)
	code := request[map[string]any](t, f.owner, "POST", "/v1/companion/pairing", nil)["code"].(string)
	var enrolled map[string]string
	opening := []byte(wire.Marshal(map[string]string{"code": code, "label": "Streamed phone"}))
	if err := json.Unmarshal(phonePost(t, f.origin, "", "/companion/enroll", "application/json", opening, 201), &enrolled); err != nil {
		t.Fatal(err)
	}
	token, deviceID := enrolled["token"], enrolled["device_id"]
	phonePost(t, f.origin, token, "/companion/heartbeat", "application/json", []byte(`{"present_session":""}`), 200)
	enroll(t, f, deviceID, wire.Personal, []wire.Capability{wire.Screen})
	session := request[wire.Session](t, f.agent, "POST", "/v1/sessions", wire.StartSession{
		DeviceID: deviceID, Purpose: "Verify acknowledged frame streaming", Capabilities: []wire.Capability{wire.Screen},
	})
	phonePost(t, f.origin, token, "/companion/sessions/"+session.ID+"/approve", "application/json", []byte(`{}`), 200)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	streamRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, f.origin+"/v1/companion/sessions/"+session.ID+"/frames", nil)
	if err != nil {
		t.Fatal(err)
	}
	streamRequest.Header.Set("Authorization", "Bearer "+tokenAgent)
	response, err := http.DefaultClient.Do(streamRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	sse := bufio.NewReader(response.Body)
	if waiting := readSSEBlock(t, sse); !strings.Contains(waiting, `"state":"waiting"`) {
		t.Fatalf("expected initial waiting event, got %q", waiting)
	}

	socketURL := strings.Replace(f.origin, "http://", "ws://", 1) + "/companion/sessions/" + session.ID + "/frames"
	headers := http.Header{"Authorization": []string{"Bearer " + token}}
	socket, _, err := websocket.DefaultDialer.DialContext(ctx, socketURL, headers)
	if err != nil {
		t.Fatalf("open phone frame stream: %v", err)
	}
	defer socket.Close()
	var picture bytes.Buffer
	if err := jpeg.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 80, 120)), nil); err != nil {
		t.Fatal(err)
	}
	if err := socket.WriteMessage(websocket.BinaryMessage, picture.Bytes()); err != nil {
		t.Fatalf("send first captured frame: %v", err)
	}
	_, ackBytes, err := socket.ReadMessage()
	if err != nil {
		t.Fatalf("read first frame acknowledgement: %v", err)
	}
	var ack struct {
		Type    string `json:"type"`
		FrameID string `json:"frame_id"`
	}
	if err := json.Unmarshal(ackBytes, &ack); err != nil || ack.Type != "frame" || ack.FrameID == "" {
		t.Fatalf("invalid first frame acknowledgement: %s (%v)", ackBytes, err)
	}
	consoleFrame := readSSEBlock(t, sse)
	if !strings.Contains(consoleFrame, `"frame_id":"`+ack.FrameID+`"`) {
		t.Fatalf("console received a different frame than the phone was acknowledged: %q", consoleFrame)
	}

	observed := request[wire.Result](t, f.agent, "POST", "/v1/sessions/"+session.ID+"/operations", wire.Operation{
		Kind: "observe", Observe: &wire.ObserveRequest{Screen: true},
	})
	if observed.Observation == nil || observed.Observation.FrameID != ack.FrameID || observed.Observation.Width != 80 || observed.Observation.Height != 120 {
		t.Fatalf("agent did not observe the acknowledged captured frame: %+v", observed.Observation)
	}

	time.Sleep(205 * time.Millisecond)
	if err := socket.WriteMessage(websocket.BinaryMessage, picture.Bytes()); err != nil {
		t.Fatalf("send subsequent frame: %v", err)
	}
	_, nextBytes, err := socket.ReadMessage()
	if err != nil {
		t.Fatalf("read subsequent frame acknowledgement: %v", err)
	}
	var next struct {
		Type    string `json:"type"`
		FrameID string `json:"frame_id"`
	}
	if err := json.Unmarshal(nextBytes, &next); err != nil || next.Type != "frame" || next.FrameID == ack.FrameID {
		t.Fatalf("subsequent frame was not independently accepted: %s (%v)", nextBytes, err)
	}
}

func TestCompanionApprovedControlChannelSupportsAppsTreeTouchTextAndBrowser(t *testing.T) {
	f := setup(t)
	pairing := request[map[string]any](t, f.owner, http.MethodPost, "/v1/companion/pairing", map[string]any{
		"auto_enroll":  true,
		"grant_agents": []string{"agent"},
	})
	var phone struct {
		DeviceID string `json:"device_id"`
		Token    string `json:"token"`
	}
	opening := []byte(wire.Marshal(map[string]string{"code": pairing["code"].(string), "label": "Companion control phone"}))
	if err := json.Unmarshal(phonePost(t, f.origin, "", "/companion/enroll", "application/json", opening, http.StatusCreated), &phone); err != nil {
		t.Fatal(err)
	}
	caps := []wire.Capability{wire.Tree, wire.Touch, wire.Text, wire.Apps, wire.Browser, wire.MultiTouch}
	phonePost(t, f.origin, phone.Token, "/companion/heartbeat", "application/json", []byte(wire.Marshal(map[string]any{"present_session": "", "capabilities": caps})), http.StatusOK)
	session := request[wire.Session](t, f.agent, http.MethodPost, "/v1/sessions", wire.StartSession{
		DeviceID: phone.DeviceID, Purpose: "Exercise approved companion controls", Capabilities: caps,
	})
	phonePost(t, f.origin, phone.Token, "/companion/sessions/"+session.ID+"/approve", "application/json", []byte(`{}`), http.StatusOK)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	socketURL := strings.Replace(f.origin, "http://", "ws://", 1) + "/companion/sessions/" + session.ID + "/control"
	socket, _, err := websocket.DefaultDialer.DialContext(ctx, socketURL, http.Header{"Authorization": []string{"Bearer " + phone.Token}})
	if err != nil {
		t.Fatalf("open approved phone control stream: %v", err)
	}
	defer socket.Close()
	type command struct {
		Type     string        `json:"type"`
		ID       string        `json:"id"`
		Dispatch wire.Dispatch `json:"dispatch"`
	}
	type outcome struct {
		result wire.Result
		err    error
	}
	startOperation := func(op wire.Operation) <-chan outcome {
		done := make(chan outcome, 1)
		go func() {
			var result wire.Result
			err := f.agent.Request(ctx, http.MethodPost, "/v1/sessions/"+session.ID+"/operations", op, &result)
			done <- outcome{result: result, err: err}
		}()
		return done
	}
	readCommand := func(want string) command {
		t.Helper()
		_ = socket.SetReadDeadline(time.Now().Add(5 * time.Second))
		var received command
		if err := socket.ReadJSON(&received); err != nil {
			t.Fatalf("read %s control command: %v", want, err)
		}
		if received.Type != "command" || received.Dispatch.Operation.Kind != want {
			t.Fatalf("received control command %q (%q), want %q", received.Type, received.Dispatch.Operation.Kind, want)
		}
		return received
	}
	respond := func(received command, result wire.Result) {
		t.Helper()
		result.ID = received.ID
		if result.State == "" {
			result.State = wire.Succeeded
		}
		if err := socket.WriteJSON(map[string]any{"type": "result", "id": received.ID, "result": result}); err != nil {
			t.Fatalf("write control response: %v", err)
		}
	}
	wait := func(done <-chan outcome, want string) wire.Result {
		t.Helper()
		select {
		case value := <-done:
			if value.err != nil {
				t.Fatalf("%s operation: %v", want, value.err)
			}
			if value.result.State != wire.Succeeded {
				t.Fatalf("%s operation state = %q, error=%+v", want, value.result.State, value.result.Fault)
			}
			return value.result
		case <-ctx.Done():
			t.Fatalf("%s operation did not finish: %v", want, ctx.Err())
			return wire.Result{}
		}
	}

	done := startOperation(wire.Operation{Kind: "apps_list"})
	appCommand := readCommand("apps_list")
	respond(appCommand, wire.Result{Apps: []string{"com.instagram.android"}, InstalledApps: []wire.AppInfo{{ID: "com.instagram.android", Name: "Instagram", Launchable: true}}})
	apps := wait(done, "apps_list")
	if len(apps.InstalledApps) != 1 || apps.InstalledApps[0].Name != "Instagram" || apps.InstalledApps[0].ID != "com.instagram.android" {
		t.Fatalf("app display names and package IDs were not preserved: %+v", apps)
	}

	done = startOperation(wire.Operation{Kind: "observe", Observe: &wire.ObserveRequest{Tree: true}})
	treeCommand := readCommand("observe")
	if !treeCommand.Dispatch.Operation.Observe.Tree {
		t.Fatal("companion observe command did not request the accessibility tree")
	}
	phoneObservation := wire.Observation{FrameID: wire.NewID(), CapturedAt: time.Now(), Width: 100, Height: 200, Orientation: "portrait", Nodes: []wire.Node{{
		ID: "instagram-button", Label: "Instagram", Bounds: wire.Rect{X: 10, Y: 20, Width: 30, Height: 12}, Enabled: true, Visible: true, Clickable: true,
	}}}
	respond(treeCommand, wire.Result{Observation: &phoneObservation})
	observed := wait(done, "observe tree")
	if observed.Observation == nil || len(observed.Observation.Nodes) != 1 || observed.Observation.Width != 100 {
		t.Fatalf("phone accessibility tree was not returned: %+v", observed.Observation)
	}

	done = startOperation(wire.Operation{Kind: "act", Action: &wire.Action{Type: "tap", FrameID: observed.Observation.FrameID, NodeID: "instagram-button"}})
	tapCommand := readCommand("act")
	if tapCommand.Dispatch.Operation.Action.NodeID != "instagram-button" {
		t.Fatal("semantic tap was not forwarded to the phone")
	}
	respond(tapCommand, wire.Result{})
	_ = wait(done, "semantic tap")

	done = startOperation(wire.Operation{Kind: "browser_contexts"})
	browserCommand := readCommand("browser_contexts")
	respond(browserCommand, wire.Result{Contexts: []wire.BrowserContext{{ID: "com.android.chrome", Title: "Chrome"}}})
	browsers := wait(done, "browser contexts")
	if len(browsers.Contexts) != 1 || browsers.Contexts[0].ID != "com.android.chrome" {
		t.Fatalf("browser contexts were not returned: %+v", browsers.Contexts)
	}
}

func TestSuccessfulDeviceGestureStreamsToLinkedCompanion(t *testing.T) {
	f := setup(t)
	pairing := request[map[string]any](t, f.owner, "POST", "/v1/companion/pairing", map[string]any{
		"auto_enroll":  true,
		"grant_agents": []string{"agent"},
	})
	var phone struct {
		DeviceID string `json:"device_id"`
		Token    string `json:"token"`
	}
	opening := []byte(wire.Marshal(map[string]string{
		"code": pairing["code"].(string), "label": "Gesture pointer phone",
	}))
	if err := json.Unmarshal(phonePost(t, f.origin, "", "/companion/enroll", "application/json", opening, http.StatusCreated), &phone); err != nil {
		t.Fatal(err)
	}
	phonePost(t, f.origin, phone.Token, "/companion/heartbeat", "application/json", []byte(`{"present_session":""}`), http.StatusOK)

	controlCaps := []wire.Capability{wire.Screen, wire.Touch}
	enroll(t, f, f.device, wire.Personal, controlCaps)
	gestureTargetPath := "/v1/companion/gesture-target/" + phone.DeviceID
	request[map[string]string](t, f.owner, http.MethodPut, gestureTargetPath, map[string]string{"target_device_id": f.device})
	if target := request[map[string]string](t, f.owner, http.MethodGet, gestureTargetPath, nil); target["target_device_id"] != f.device {
		t.Fatalf("gesture target = %q, want %q", target["target_device_id"], f.device)
	}

	sharing := request[wire.Session](t, f.owner, http.MethodPost, "/v1/sessions", wire.StartSession{
		DeviceID: phone.DeviceID, Purpose: "Watch agent gesture feedback", Capabilities: []wire.Capability{wire.Screen},
	})
	phonePost(t, f.origin, phone.Token, "/companion/sessions/"+sharing.ID+"/approve", "application/json", []byte(`{}`), http.StatusOK)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	socketURL := "ws" + strings.TrimPrefix(f.origin, "http") + "/companion/sessions/" + sharing.ID + "/gestures"
	socket, response, err := websocket.DefaultDialer.DialContext(ctx, socketURL, http.Header{
		"Authorization": []string{"Bearer " + phone.Token},
	})
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		t.Fatalf("open companion gesture stream: %v", err)
	}
	defer socket.Close()

	control := request[wire.Session](t, f.agent, http.MethodPost, "/v1/sessions", wire.StartSession{
		DeviceID: f.device, Purpose: "Verify streamed touch feedback", Capabilities: controlCaps,
	})
	request[wire.Session](t, f.owner, http.MethodPost, "/v1/sessions/"+control.ID+"/approve", nil)
	observed := request[wire.Result](t, f.agent, http.MethodPost, "/v1/sessions/"+control.ID+"/operations", wire.Operation{
		Kind: "observe", Observe: &wire.ObserveRequest{Screen: true},
	})
	if observed.Observation == nil {
		t.Fatal("control observation omitted its frame")
	}
	result := request[wire.Result](t, f.agent, http.MethodPost, "/v1/sessions/"+control.ID+"/operations", wire.Operation{
		Kind: "act",
		Action: &wire.Action{
			Type: "tap", FrameID: observed.Observation.FrameID,
			Point: &wire.Point{X: 0.25, Y: 0.75}, DurationMS: 160,
		},
	})
	if result.State != wire.Succeeded {
		t.Fatalf("agent tap state = %q, want succeeded: %+v", result.State, result.Fault)
	}

	if err := socket.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, payload, err := socket.ReadMessage()
	if err != nil {
		t.Fatalf("read gesture pointer event: %v", err)
	}
	var event struct {
		Type       string           `json:"type"`
		Gesture    string           `json:"gesture"`
		DurationMS int              `json:"duration_ms"`
		Paths      []wire.TouchPath `json:"paths"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "gesture" || event.Gesture != "tap" || event.DurationMS != 160 ||
		len(event.Paths) != 1 || event.Paths[0].Start != (wire.Point{X: 0.25, Y: 0.75}) ||
		event.Paths[0].End != event.Paths[0].Start {
		t.Fatalf("unexpected gesture pointer event: %+v", event)
	}
}

func TestDeleteDeviceEndsSessionRevokesCompanionAndHidesInventory(t *testing.T) {
	f := setup(t)
	code := request[map[string]any](t, f.owner, "POST", "/v1/companion/pairing", nil)["code"].(string)
	var enrolled map[string]string
	opening := []byte(wire.Marshal(map[string]string{"code": code, "label": "Removable phone"}))
	if err := json.Unmarshal(phonePost(t, f.origin, "", "/companion/enroll", "application/json", opening, 201), &enrolled); err != nil {
		t.Fatal(err)
	}
	token, deviceID := enrolled["token"], enrolled["device_id"]
	phonePost(t, f.origin, token, "/companion/heartbeat", "application/json", []byte(`{"present_session":""}`), 200)
	enroll(t, f, deviceID, wire.Personal, []wire.Capability{wire.Screen})
	session := request[wire.Session](t, f.agent, "POST", "/v1/sessions", wire.StartSession{
		DeviceID: deviceID, Purpose: "Remove a phone from inventory", Capabilities: []wire.Capability{wire.Screen},
	})
	phonePost(t, f.origin, token, "/companion/sessions/"+session.ID+"/approve", "application/json", []byte(`{}`), 200)

	request[map[string]bool](t, f.owner, http.MethodDelete, "/v1/devices/"+deviceID, nil)
	devices := request[[]wire.Device](t, f.owner, http.MethodGet, "/v1/devices", nil)
	for _, device := range devices {
		if device.ID == deviceID {
			t.Fatal("removed phone is still present in the inventory")
		}
	}
	ended := request[wire.Session](t, f.owner, http.MethodGet, "/v1/sessions/"+session.ID, nil)
	if ended.State != wire.Ended || ended.Reason != "device_removed" {
		t.Fatalf("active session was not ended while preserving its history: %+v", ended)
	}
	phonePost(t, f.origin, token, "/companion/heartbeat", "application/json", []byte(`{"present_session":""}`), 403)

	actor := request[wire.Actor](t, f.owner, http.MethodGet, "/v1/me", nil)
	if err := f.db.Register(t.Context(), actor.Workspace, "companion."+deviceID, []wire.Inventory{{
		ID: deviceID, Label: "Removable phone", Platform: "android", Mode: "companion",
		Capabilities: wire.Capabilities{wire.Screen: true},
	}}, time.Now()); err != nil {
		t.Fatalf("source rediscovery after removal: %v", err)
	}
	devices = request[[]wire.Device](t, f.owner, http.MethodGet, "/v1/devices", nil)
	for _, device := range devices {
		if device.ID == deviceID {
			t.Fatal("removed phone reappeared when its connector reported it again")
		}
	}
	if err := f.owner.Request(t.Context(), http.MethodPut, "/v1/devices/"+deviceID, wire.Enrollment{
		Owner: "owner", Profile: wire.Personal,
	}, nil); wire.Public(err).Status != http.StatusNotFound {
		t.Fatalf("removed phone enrollment returned %v, want 404", err)
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
