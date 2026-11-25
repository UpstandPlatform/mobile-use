package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"mobile-use/internal/agent"
	"mobile-use/internal/client"
	"mobile-use/internal/connector"
	"mobile-use/internal/relay"
	"mobile-use/internal/service"
	"mobile-use/internal/store"
	"mobile-use/internal/wire"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fixtureDriver struct {
	actions atomic.Int32
	shot    []byte
}

func (d *fixtureDriver) Display(context.Context) (connector.Display, error) {
	return connector.Display{Width: 100, Height: 200, AppID: "org.example.app", Orientation: "portrait"}, nil
}
func (d *fixtureDriver) Screenshot(context.Context) (*wire.Image, error) {
	return &wire.Image{MIME: "image/png", Data: d.shot}, nil
}
func (d *fixtureDriver) Tree(context.Context) ([]wire.Node, error) {
	return []wire.Node{{ID: "button", Label: "Open", Enabled: true, Clickable: true, Bounds: wire.Rect{X: 10, Y: 10, Width: 40, Height: 40}}}, nil
}
func (d *fixtureDriver) Action(_ context.Context, _ wire.Action, _ connector.Display, _ *wire.Rect) error {
	d.actions.Add(1)
	return nil
}
func (*fixtureDriver) Apps(context.Context) ([]string, error) {
	return []string{"org.example.app"}, nil
}
func (*fixtureDriver) App(context.Context, string, bool) error { return nil }
func (*fixtureDriver) Browser(context.Context, wire.Operation) (wire.Result, error) {
	return wire.Result{}, wire.ErrUnsupported
}
func (*fixtureDriver) Close(context.Context) {}

type fixtureBackend struct {
	driver *fixtureDriver
	size   int
}

func (b fixtureBackend) Open(context.Context, connector.Found) (connector.Driver, error) {
	return b.driver, nil
}
func (b fixtureBackend) Discover(context.Context) ([]connector.Found, error) {
	devices := make([]connector.Found, 0, b.size)
	for i := range b.size {
		devices = append(devices, connector.Found{NativeID: fmt.Sprintf("testing-%d", i), Inventory: wire.Inventory{
			Label: fmt.Sprintf("Sandbox phone %d", i+1), Platform: "android", Mode: "adb",
			Capabilities: wire.Capabilities{wire.Screen: true, wire.Tree: true, wire.Touch: true},
		}})
	}
	return devices, nil
}

type fixture struct {
	db                    *store.Store
	owner, agent, foreign *client.Client
	origin                string
	device                string
	driver                *fixtureDriver
	companion             *service.Companion
}

const tokenOwner = "fixture-owner-token-not-a-secret"
const tokenAgent = "fixture-agent-token-not-a-secret"

func setup(t *testing.T) fixture {
	return setupFleet(t, 1)
}

func setupFleet(t *testing.T, deviceCount int) fixture {
	t.Helper()
	ctx := t.Context()
	db, err := store.Open(ctx, t.TempDir()+"/store.sqlite", os.Getenv("MOBILE_USE_TEST_POSTGRES"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	workspace := "test-" + wire.NewID()
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 100, 200))); err != nil {
		t.Fatal(err)
	}
	driver := &fixtureDriver{shot: imageBytes.Bytes()}
	manager := connector.New(workspace, "local", fixtureBackend{driver: driver, size: deviceCount})
	devices := manager.Inventory(ctx)
	if err := db.Register(ctx, workspace, "local", devices, time.Now()); err != nil {
		t.Fatal(err)
	}
	if deviceCount > 1 {
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			ticker := time.NewTicker(3 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case now := <-ticker.C:
					if err := db.Register(ctx, workspace, "local", manager.Inventory(ctx), now); err != nil && ctx.Err() == nil {
						t.Error(err)
					}
				}
			}
		}()
		t.Cleanup(func() { <-stopped })
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	server.Listener.Close()
	server.Listener = listener
	origin := "http://" + listener.Addr().String()
	companion := service.NewCompanion(db, origin)
	svc := &service.Service{Store: db, Hub: &relay.Hub{Store: db, LocalWorkspace: workspace, Local: manager, Companion: companion}, DigestKey: []byte("test-only-operation-digest-key")}
	auth := &service.Auth{Static: []service.Principal{
		{Token: tokenOwner, Actor: wire.Actor{Workspace: workspace, Subject: "owner", Role: wire.Admin}},
		{Token: tokenAgent, Actor: wire.Actor{Workspace: workspace, Subject: "agent", Role: wire.Agent}},
		{Token: "other-workspace-test-token", Actor: wire.Actor{Workspace: "another", Subject: "owner", Role: wire.Admin}},
	}}
	server.Config.Handler = svc.HTTP(service.HTTPOptions{Origin: origin, Auth: auth, MCP: agent.HTTP(svc), Extra: func(mux *http.ServeMux) { companion.ConsoleRoutes(mux, auth, origin); companion.PhoneRoutes(mux) }})
	server.Start()
	t.Cleanup(server.Close)
	owner, _ := client.New(origin, tokenOwner)
	worker, _ := client.New(origin, tokenAgent)
	foreign, _ := client.New(origin, "other-workspace-test-token")
	return fixture{db, owner, worker, foreign, origin, devices[0].ID, driver, companion}
}

func request[T any](t *testing.T, c *client.Client, method, path string, body any) T {
	t.Helper()
	var result T
	if err := c.Request(t.Context(), method, path, body, &result); err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return result
}

func enroll(t *testing.T, f fixture, id, profile string, caps []wire.Capability) {
	t.Helper()
	request[wire.Device](t, f.owner, "PUT", "/v1/devices/"+id, wire.Enrollment{Owner: "owner", Profile: profile, Grants: []wire.Grant{{Subject: "agent", Capabilities: caps}}})
}

func TestFleetControlPlane(t *testing.T) {
	const devices, simultaneous = 100, 25
	f := setupFleet(t, devices)
	caps := []wire.Capability{wire.Screen, wire.Touch}
	inventory := request[[]wire.Device](t, f.owner, "GET", "/v1/devices", nil)
	if len(inventory) != devices {
		t.Fatalf("inventory has %d devices, want %d", len(inventory), devices)
	}
	for _, d := range inventory {
		enroll(t, f, d.ID, wire.Dedicated, caps)
	}
	var wg sync.WaitGroup
	var sessions [simultaneous]wire.Session
	for i, d := range inventory[:simultaneous] {
		wg.Go(func() {
			req := wire.StartSession{DeviceID: d.ID, Purpose: "Concurrent fleet verification", Capabilities: caps}
			if err := f.agent.Request(t.Context(), "POST", "/v1/sessions", req, &sessions[i]); err != nil {
				t.Errorf("start device %s: %v", d.ID, err)
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	active := request[[]wire.Session](t, f.agent, "GET", "/v1/sessions", nil)
	if len(active) != simultaneous {
		t.Fatalf("active sessions=%d, want %d", len(active), simultaneous)
	}
	for _, session := range sessions {
		wg.Go(func() {
			base := "/v1/sessions/" + session.ID
			var result wire.Result
			observe := wire.Operation{Kind: "observe", Observe: &wire.ObserveRequest{Screen: true}}
			if err := f.agent.Request(t.Context(), "POST", base+"/operations", observe, &result); err != nil || result.Observation == nil {
				t.Errorf("observe session %s: %v, %v", session.ID, err, result.Fault)
				return
			}
			action := wire.Operation{ID: wire.NewID(), Kind: "act", Action: &wire.Action{Type: "tap", FrameID: result.Observation.FrameID, Point: &wire.Point{X: 0.2, Y: 0.3}}}
			result = wire.Result{}
			if err := f.agent.Request(t.Context(), "POST", base+"/operations", action, &result); err != nil || result.State != wire.Succeeded {
				t.Errorf("act on session %s: %v, %v", session.ID, err, result.Fault)
			}
			if err := f.agent.Request(t.Context(), "POST", base+"/end", nil, nil); err != nil {
				t.Errorf("end session %s: %v", session.ID, err)
			}
		})
	}
	wg.Wait()
	if count := f.driver.actions.Load(); count != simultaneous {
		t.Errorf("ran %d actions, want %d", count, simultaneous)
	}
}

func TestPersonalSessionMCPAndControl(t *testing.T) {
	f := setup(t)
	caps := []wire.Capability{wire.Screen, wire.Tree, wire.Touch}
	if result := request[[]wire.Device](t, f.agent, "GET", "/v1/devices", nil); len(result) != 0 {
		t.Fatal("unenrolled device visible")
	}
	enroll(t, f, f.device, wire.Personal, caps)
	if result := request[[]wire.Device](t, f.foreign, "GET", "/v1/devices", nil); len(result) != 0 {
		t.Fatal("device leaked across workspaces")
	}
	session := request[wire.Session](t, f.agent, "POST", "/v1/sessions", wire.StartSession{DeviceID: f.device, Purpose: "Observe and tap a test app", Capabilities: caps})
	if session.State != wire.Pending {
		t.Fatal(session.State)
	}
	base := "/v1/sessions/" + session.ID
	if err := f.agent.Request(t.Context(), "POST", base+"/approve", nil, nil); err == nil {
		t.Fatal("agent approved its own request")
	}
	request[wire.Session](t, f.owner, "POST", base+"/approve", nil)
	ctx := t.Context()
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "mobile-use-integration", Version: "1"}, nil)
	connection, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: f.origin + "/mcp", HTTPClient: &http.Client{Transport: authTransport{tokenAgent}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	tools, err := connection.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) < 14 {
		t.Fatalf("MCP discovery: %v", err)
	}
	shot, err := connection.CallTool(ctx, &mcp.CallToolParams{Name: "observe", Arguments: map[string]any{"session_id": session.ID, "screen": true, "tree": false}})
	if err != nil || shot.IsError || len(shot.Content) != 2 {
		t.Fatalf("MCP screenshot: %v, %#v", err, shot)
	}
	if imageContent, ok := shot.Content[1].(*mcp.ImageContent); !ok || imageContent.MIMEType != "image/png" {
		t.Fatal("MCP omitted the image")
	}
	opURL := base + "/operations"
	observation := request[wire.Result](t, f.agent, "POST", opURL, wire.Operation{Kind: "observe", Observe: &wire.ObserveRequest{Screen: true, Tree: true}})
	if observation.Observation == nil || len(observation.Observation.Nodes) != 1 {
		t.Fatalf("missing observation: %+v", observation)
	}
	tap := wire.Operation{ID: "tap-once", Kind: "act", Action: &wire.Action{Type: "tap", FrameID: observation.Observation.FrameID, NodeID: "button"}}
	for range 2 {
		if result := request[wire.Result](t, f.agent, "POST", opURL, tap); result.State != wire.Succeeded {
			t.Fatalf("tap failed: %+v", result)
		}
	}
	if f.driver.actions.Load() != 1 {
		t.Fatal("operation replayed")
	}
	request[wire.Session](t, f.owner, "POST", base+"/pause", nil)
	if err := f.agent.Request(ctx, "POST", opURL, tap, nil); err == nil {
		t.Fatal("agent input accepted while paused")
	}
	request[wire.Session](t, f.owner, "POST", base+"/takeover", nil)
	if err := f.agent.Request(ctx, "POST", opURL, tap, nil); err == nil {
		t.Fatal("agent input accepted after takeover")
	}
	request[wire.Session](t, f.owner, "POST", base+"/end", nil)
	if err := f.owner.Request(ctx, "POST", opURL, tap, nil); err == nil {
		t.Fatal("input accepted after stop")
	}
	if events := request[[]wire.AuditEvent](t, f.owner, "GET", "/v1/audit", nil); len(events) < 10 {
		t.Fatal("audit events were missing")
	}
}

func TestExclusiveDedicatedLease(t *testing.T) {
	f := setup(t)
	enroll(t, f, f.device, wire.Dedicated, []wire.Capability{wire.Screen})
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for range 12 {
		wg.Go(func() {
			results <- f.agent.Request(t.Context(), "POST", "/v1/sessions", wire.StartSession{DeviceID: f.device, Purpose: "Parallel reservation", Capabilities: []wire.Capability{wire.Screen}}, nil)
		})
	}
	wg.Wait()
	close(results)
	count := 0
	for err := range results {
		if err == nil {
			count++
			continue
		}
		if wire.Public(err).Code != "DEVICE_BUSY" {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatalf("reserved %d controllers", count)
	}
}

type authTransport struct{ token string }

func (a authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c := req.Clone(req.Context())
	c.Header.Set("Authorization", "Bearer "+a.token)
	return http.DefaultTransport.RoundTrip(c)
}

func phonePost(t *testing.T, origin, token, path, contentType string, body []byte, want int) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), "POST", origin+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out bytes.Buffer
	_, _ = out.ReadFrom(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("phone %s = %d, want %d: %s", path, resp.StatusCode, want, out.String())
	}
	return out.Bytes()
}

func TestCompanionPairingUploadAndRevocation(t *testing.T) {
	f := setup(t)
	code := request[map[string]any](t, f.owner, "POST", "/v1/companion/pairing", nil)["code"].(string)
	opening := []byte(wire.Marshal(map[string]string{"code": code, "label": "Companion fixture"}))
	var enrolled map[string]string
	if err := json.Unmarshal(phonePost(t, f.origin, "", "/companion/enroll", "application/json", opening, 201), &enrolled); err != nil {
		t.Fatal(err)
	}
	phonePost(t, f.origin, "", "/companion/enroll", "application/json", opening, 403)
	token, id := enrolled["token"], enrolled["device_id"]
	phonePost(t, f.origin, token, "/companion/heartbeat", "application/json", []byte(`{"present_session":""}`), 200)
	caps := []wire.Capability{wire.Screen, wire.Camera, wire.Microphone}
	enroll(t, f, id, wire.Personal, caps)
	session := request[wire.Session](t, f.agent, "POST", "/v1/sessions", wire.StartSession{DeviceID: id, Purpose: "Share synthetic pixels", Capabilities: caps})
	phonePost(t, f.origin, token, "/companion/sessions/"+session.ID+"/approve", "application/json", []byte(`{}`), 200)
	var picture bytes.Buffer
	if err := jpeg.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 80, 120)), nil); err != nil {
		t.Fatal(err)
	}
	phonePost(t, f.origin, token, "/companion/sessions/"+session.ID+"/frame", "image/jpeg", picture.Bytes(), 204)
	opURL := "/v1/sessions/" + session.ID + "/operations"
	result := request[wire.Result](t, f.agent, "POST", opURL, wire.Operation{Kind: "observe", Observe: &wire.ObserveRequest{Screen: true}})
	if result.Observation == nil || result.Observation.Width != 80 {
		t.Fatalf("missing companion pixels: %+v", result)
	}
	sensor := request[wire.Result](t, f.agent, "POST", opURL, wire.Operation{ID: "approved-photo", Kind: "sensor_request", Sensor: &wire.SensorRequest{Type: wire.Camera}})
	if sensor.Sensor == nil {
		t.Fatal("missing sensor request")
	}
	phonePost(t, f.origin, token, "/companion/sensors/"+sensor.Sensor.ID+"/artifact", "image/jpeg", picture.Bytes(), 200)
	photo := request[wire.Result](t, f.agent, "POST", opURL, wire.Operation{Kind: "artifact_get", Sensor: &wire.SensorRequest{Type: wire.Camera, RequestID: sensor.Sensor.ID}})
	if photo.Artifact == nil || photo.Artifact.MIME != "image/jpeg" {
		t.Fatal("missing camera artifact")
	}
	request[map[string]bool](t, f.owner, "DELETE", "/v1/companion/"+id, nil)
	phonePost(t, f.origin, token, "/companion/heartbeat", "application/json", []byte(`{"present_session":""}`), 403)
}
