package connector

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mobile-use/internal/wire"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIOSContract(t *testing.T) {
	gesture := make(chan []map[string]any, 1)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var value any
		switch r.Method + " " + r.URL.Path {
		case "POST /session/example/execute/sync":
			var payload struct {
				Script string `json:"script"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			switch payload.Script {
			case "mobile: isLocked":
				value = false
			case "mobile: activeAppInfo":
				value = map[string]string{"bundleId": "org.example.app"}
			default:
				t.Error("unknown mobile command")
			}
		case "GET /session/example/window/rect":
			value = wire.Rect{Width: 390, Height: 844}
		case "GET /session/example/screenshot":
			value = base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n"))
		case "GET /session/example/source":
			value = `<AppiumAUT><XCUIElementTypeButton name="Open" label="Open" enabled="true" x="12" y="24" width="30" height="30"/><XCUIElementTypeSecureTextField name="Token" value="sensitive" /></AppiumAUT>`
		case "POST /session/example/actions":
			var payload struct {
				Actions []map[string]any `json:"actions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			gesture <- payload.Actions
		case "GET /session/example/element/active":
			value = map[string]string{"element-6066-11e4-a52e-4f735466cecf": "secret-field"}
		case "GET /session/example/element/secret-field/attribute/type":
			value = "XCUIElementTypeSecureTextField"
		default:
			t.Errorf("unexpected WebDriver request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 404)
			return
		}
		_, _ = fmt.Fprint(w, wire.Marshal(map[string]any{"value": value}))
	}))
	defer fixture.Close()
	device := iosDevice{endpoint: fixture.URL + "/session/example", client: fixture.Client()}
	ctx := t.Context()
	display, err := device.Display(ctx)
	if err != nil || display.Width != 390 || display.AppID != "org.example.app" {
		t.Fatalf("Display: %+v %v", display, err)
	}
	if shot, err := device.Screenshot(ctx); err != nil || shot.MIME != "image/png" {
		t.Fatalf("Screenshot: %+v %v", shot, err)
	}
	nodes, err := device.Tree(ctx)
	if err != nil || len(nodes) != 2 || nodes[1].Label != "" {
		t.Fatalf("Tree: %+v %v", nodes, err)
	}
	action := wire.Action{Type: "pinch", Point: &wire.Point{X: .2, Y: .3}, End: &wire.Point{X: .1, Y: .1}, SecondPoint: &wire.Point{X: .7, Y: .8}, SecondEnd: &wire.Point{X: .9, Y: .9}}
	if err := device.Action(ctx, action, display, nil); err != nil {
		t.Fatal(err)
	}
	if payload := <-gesture; len(payload) != 2 {
		t.Fatalf("expected two touch sources, got %d", len(payload))
	}
	if err := device.Action(ctx, wire.Action{Type: "type", Text: "protected"}, display, nil); err != wire.ErrLocked {
		t.Fatalf("secure field was not handed off: %v", err)
	}
}
