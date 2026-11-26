package connector

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"mobile-use/internal/wire"
)

type IOSDevice struct {
	UDID        string `json:"udid"`
	Label       string `json:"label"`
	Virtual     bool   `json:"virtual"`
	WDABundleID string `json:"wda_bundle_id,omitempty"`
	WDAURL      string `json:"wda_url,omitempty"`
	WDAPort     int    `json:"wda_port,omitempty"`
	WebKitPort  int    `json:"webkit_port,omitempty"`
	MJPEGURL    string `json:"mjpeg_url,omitempty"`
	MJPEGPort   int    `json:"mjpeg_port,omitempty"`
}

type IOS struct {
	endpoint string
	devices  map[string]IOSDevice
	client   *http.Client
	ffmpeg   string
}

func NewIOS(endpoint string, configured []IOSDevice, ffmpeg string) (*IOS, error) {
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("the iOS connector requires a macOS host")
	}
	if len(configured) == 0 {
		return nil, fmt.Errorf("configure at least one iOS device")
	}
	if !localHTTP(endpoint) {
		return nil, fmt.Errorf("Appium must use a loopback HTTP endpoint")
	}
	backend := &IOS{endpoint: strings.TrimRight(endpoint, "/"), devices: make(map[string]IOSDevice), client: &http.Client{
		Transport:     &http.Transport{MaxIdleConns: 16, IdleConnTimeout: 30 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, ffmpeg: ffmpeg}
	ports := make(map[int]bool)
	for _, device := range configured {
		if !wire.ValidID(device.UDID) || device.Label == "" || len(device.Label) > 100 || device.WDAURL != "" && !localHTTP(device.WDAURL) {
			return nil, fmt.Errorf("invalid iOS device configuration")
		}
		if _, exists := backend.devices[device.UDID]; exists {
			return nil, fmt.Errorf("iOS device UDIDs must be unique")
		}
		if device.MJPEGURL != "" && (ffmpeg == "" || !localHTTP(device.MJPEGURL)) {
			return nil, fmt.Errorf("iOS MJPEG needs a loopback URL and the FFmpeg executable")
		}
		if device.MJPEGPort < 0 || device.MJPEGPort > 65535 || device.MJPEGPort > 0 && device.MJPEGPort < 1024 {
			return nil, fmt.Errorf("invalid WDA MJPEG port")
		}
		for _, port := range []int{device.WDAPort, device.WebKitPort} {
			if port == 0 && len(configured) == 1 {
				continue
			}
			if port < 1024 || port > 65535 || ports[port] {
				return nil, fmt.Errorf("configure unique WDA and WebKit ports per device")
			}
			ports[port] = true
		}
		backend.devices[device.UDID] = device
	}
	return backend, nil
}

func localHTTP(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback()
}

func (i *IOS) Discover(ctx context.Context) ([]Found, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := webdriver(ctx, i.client, i.endpoint, "GET", "/status", nil, nil); err != nil {
		return nil, err
	}
	// Enrollment is explicit. Appium verifies device availability when creating the runner session.
	result := make([]Found, 0, len(i.devices))
	for _, device := range i.devices {
		result = append(result, Found{NativeID: device.UDID, Inventory: wire.Inventory{
			Label: device.Label, Platform: "ios", Mode: "xcuitest", Virtual: device.Virtual,
			Capabilities: wire.Capabilities{wire.Screen: true, wire.Tree: true, wire.Touch: true, wire.Text: true, wire.Apps: true, wire.Browser: true, wire.MultiTouch: true}, LiveVideo: device.MJPEGURL != "",
		}})
	}
	return result, nil
}

func (i *IOS) Open(ctx context.Context, found Found) (Driver, error) {
	d := i.devices[found.NativeID]
	caps := map[string]any{
		"platformName": "iOS", "appium:automationName": "XCUITest", "appium:udid": d.UDID,
		"appium:noReset": true, "appium:autoAcceptAlerts": false, "appium:autoDismissAlerts": false,
		"appium:newCommandTimeout": 300, "appium:showXcodeLog": false, "appium:wdaLaunchTimeout": 8000, "appium:wdaStartupRetries": 0,
	}
	if d.WDAURL != "" {
		caps["appium:webDriverAgentUrl"] = d.WDAURL
	}
	if d.WDAURL == "" && !d.Virtual {
		caps["appium:usePreinstalledWDA"] = true
	}
	if d.WDABundleID != "" {
		caps["appium:updatedWDABundleId"] = d.WDABundleID
	}
	if d.WDAPort > 0 {
		caps["appium:wdaLocalPort"] = d.WDAPort
	}
	if d.WebKitPort > 0 {
		caps["appium:webkitDebugProxyPort"] = d.WebKitPort
	}
	if d.MJPEGPort > 0 {
		caps["appium:mjpegServerPort"] = d.MJPEGPort
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	err := webdriver(ctx, i.client, i.endpoint, "POST", "/session", map[string]any{"capabilities": map[string]any{"alwaysMatch": caps}}, &session)
	if err != nil || !wire.ValidID(session.SessionID) {
		return nil, wire.Error("IOS_SESSION_FAILED", "Appium could not open the iOS session. Check Xcode, device trust, and WebDriverAgent provisioning.", 503)
	}
	backend := &iosDevice{endpoint: i.endpoint + "/session/" + session.SessionID, client: i.client, mjpeg: d.MJPEGURL, ffmpeg: i.ffmpeg}
	if d.Virtual {
		backend.simulator = d.UDID
	}
	return backend, nil
}

type iosDevice struct {
	endpoint  string
	client    *http.Client
	simulator string
	mjpeg     string
	ffmpeg    string
}

func webdriver(ctx context.Context, client *http.Client, endpoint, method, path string, request, response any) error {
	var body io.Reader
	if request != nil {
		body = strings.NewReader(wire.Marshal(request))
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint+path, body)
	if err != nil {
		return err
	}
	if request != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	var envelope struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 12<<20)).Decode(&envelope); err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return wire.Error("WEBDRIVER_REJECTED", "Appium rejected the operation on this device.", 422)
	}
	if response == nil {
		return nil
	}
	return json.Unmarshal(envelope.Value, response)
}

func (i *iosDevice) command(ctx context.Context, method, path string, request, response any) error {
	return webdriver(ctx, i.client, i.endpoint, method, path, request, response)
}

func (i *iosDevice) script(ctx context.Context, source string, args []any, response any) error {
	if args == nil {
		args = []any{}
	}
	return i.command(ctx, "POST", "/execute/sync", map[string]any{"script": source, "args": args}, response)
}

func (i *iosDevice) mobile(ctx context.Context, name string, args map[string]any, response any) error {
	if args == nil {
		return i.script(ctx, "mobile: "+name, nil, response)
	}
	return i.script(ctx, "mobile: "+name, []any{args}, response)
}

func (i *iosDevice) Display(ctx context.Context) (Display, error) {
	if err := i.requireUnlocked(ctx); err != nil {
		return Display{}, err
	}
	var size wire.Rect
	if err := i.command(ctx, "GET", "/window/rect", nil, &size); err != nil {
		return Display{}, err
	}
	var foreground struct {
		BundleID string `json:"bundleId"`
	}
	if err := i.mobile(ctx, "activeAppInfo", nil, &foreground); err != nil {
		return Display{}, err
	}
	if size.Width < 1 || size.Height < 1 || foreground.BundleID == "" {
		return Display{}, wire.ErrUnsupported
	}
	orientation := "portrait"
	if size.Width > size.Height {
		orientation = "landscape"
	}
	return Display{Width: size.Width, Height: size.Height, Orientation: orientation, AppID: foreground.BundleID}, nil
}

func (i *iosDevice) requireUnlocked(ctx context.Context) error {
	var locked *bool
	if err := i.mobile(ctx, "isLocked", nil, &locked); err != nil {
		if err := i.command(ctx, "POST", "/appium/device/is_locked", map[string]any{}, &locked); err != nil {
			return wire.ErrLocked
		}
	}
	if locked == nil || *locked {
		return wire.ErrLocked
	}
	return nil
}

func (i *iosDevice) Screenshot(ctx context.Context) (*wire.Image, error) {
	var encoded string
	if err := i.command(ctx, "GET", "/screenshot", nil, &encoded); err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, wire.ErrUnsupported
	}
	mime := http.DetectContentType(data)
	if mime != "image/png" && mime != "image/jpeg" {
		return nil, wire.ErrUnsupported
	}
	return &wire.Image{MIME: mime, Data: data}, nil
}

func (i *iosDevice) Tree(ctx context.Context) ([]wire.Node, error) {
	var data string
	if err := i.command(ctx, "GET", "/source", nil, &data); err != nil {
		return nil, err
	}
	return parseNodes(xml.NewDecoder(bytes.NewBufferString(data)), func(element xml.StartElement) (wire.Node, bool) {
		if !strings.HasPrefix(element.Name.Local, "XCUIElementType") {
			return wire.Node{}, false
		}
		values := attrs(element)
		label := values["label"]
		if label == "" {
			label = values["value"]
		}
		if element.Name.Local == "XCUIElementTypeSecureTextField" {
			label = ""
		}
		toInt := func(value string) int { result, _ := strconv.ParseFloat(value, 64); return int(result) }
		role := element.Name.Local
		checkable := strings.Contains(role, "Switch") || strings.Contains(role, "Checkbox") || strings.Contains(role, "RadioButton")
		editable := strings.Contains(role, "TextField") || strings.Contains(role, "TextView") || strings.Contains(role, "SearchField")
		scrollable := strings.Contains(role, "ScrollView") || strings.Contains(role, "Table") || strings.Contains(role, "CollectionView") || values["scrollable"] == "true"
		return wire.Node{
			Role: role, Label: label, Hint: values["placeholderValue"], ResourceID: values["name"],
			Enabled: values["enabled"] != "false", Visible: values["visible"] != "false", VisibilityKnown: true,
			Clickable: values["accessible"] == "true", Focusable: values["focused"] == "true" || editable,
			Editable: editable, Checkable: checkable, Checked: checkable && values["value"] == "1",
			Selected: values["selected"] == "true", Scrollable: scrollable, Focused: values["focused"] == "true",
			Bounds: wire.Rect{X: toInt(values["x"]), Y: toInt(values["y"]), Width: toInt(values["width"]), Height: toInt(values["height"])},
		}, true
	})
}

func (i *iosDevice) Action(ctx context.Context, action wire.Action, display Display, rect *wire.Rect) error {
	var err error
	switch action.Type {
	case "type":
		values := make([]string, 0, len(action.Text))
		for _, character := range action.Text {
			values = append(values, string(character))
		}
		return i.sendKeys(ctx, action.Text, values)
	case "key":
		switch action.Key {
		case "home", "volume_up", "volume_down":
			button := map[string]string{"home": "home", "volume_up": "volumeUp", "volume_down": "volumeDown"}[action.Key]
			err = i.mobile(ctx, "pressButton", map[string]any{"name": button}, nil)
		case "enter", "backspace":
			value := "\uE007"
			if action.Key == "backspace" {
				value = "\uE003"
			}
			return i.sendKeys(ctx, value, []string{value})
		case "tab", "escape":
			value := "\uE004"
			if action.Key == "escape" {
				value = "\uE00C"
			}
			return i.sendKeys(ctx, value, []string{value})
		default:
			return wire.ErrUnsupported
		}
	default:
		sources, err := touchSources(action, display, rect)
		if err != nil {
			return err
		}
		if err := i.command(ctx, "POST", "/actions", map[string]any{"actions": sources}, nil); err != nil {
			return wire.ErrUnknown
		}
	}
	if err != nil {
		return wire.ErrUnknown
	}
	return nil
}

func (i *iosDevice) sendKeys(ctx context.Context, text string, values []string) error {
	const elementKey = "element-6066-11e4-a52e-4f735466cecf"
	var active map[string]string
	if err := i.command(ctx, "GET", "/element/active", nil, &active); err != nil {
		return wire.ErrUnsupported
	}
	id := active[elementKey]
	if !wire.ValidID(id) {
		return wire.ErrUnsupported
	}
	var elementType string
	if err := i.command(ctx, "GET", "/element/"+id+"/attribute/type", nil, &elementType); err != nil {
		return wire.ErrUnsupported
	}
	if elementType == "XCUIElementTypeSecureTextField" {
		return wire.ErrLocked
	}
	if err := i.command(ctx, "POST", "/element/"+id+"/value", map[string]any{"text": text, "value": values}, nil); err != nil {
		return wire.ErrUnknown
	}
	return nil
}

func touchSources(action wire.Action, display Display, rect *wire.Rect) ([]map[string]any, error) {
	paths := action.GesturePaths()
	if rect != nil {
		if action.Type == "scroll" {
			path, err := nodeScrollPath(*rect, display, action.Direction)
			if err != nil {
				return nil, err
			}
			paths = []wire.TouchPath{path}
		} else {
			x, y := rect.X+rect.Width/2, rect.Y+rect.Height/2
			point := wire.Point{X: float64(x) / float64(display.Width), Y: float64(y) / float64(display.Height)}
			paths = []wire.TouchPath{{Start: point, End: point}}
		}
	}
	if len(paths) == 0 {
		return nil, wire.ErrStale
	}
	duration := action.DurationMS
	if duration == 0 {
		duration = 350
		if action.Type == "long_press" {
			duration = 650
		} else if action.Type == "fling" {
			duration = 120
		} else if action.Type == "two_finger_tap" {
			duration = 100
		}
	}
	sequence := func(id string, path wire.TouchPath) map[string]any {
		px, py := Pixel(path.Start, display)
		actions := []map[string]any{{"type": "pointerMove", "duration": 0, "x": px, "y": py, "origin": "viewport"}, {"type": "pointerDown", "button": 0}}
		if action.Type == "long_press" || action.Type == "drag" {
			actions = append(actions, map[string]any{"type": "pause", "duration": duration})
		}
		if action.Type == "double_tap" {
			actions = append(actions,
				map[string]any{"type": "pointerUp", "button": 0},
				map[string]any{"type": "pause", "duration": 80},
				map[string]any{"type": "pointerMove", "duration": 0, "x": px, "y": py, "origin": "viewport"},
				map[string]any{"type": "pointerDown", "button": 0},
			)
		}
		if path.End != path.Start {
			nx, ny := Pixel(path.End, display)
			actions = append(actions, map[string]any{"type": "pointerMove", "duration": duration, "x": nx, "y": ny, "origin": "viewport"})
		}
		actions = append(actions, map[string]any{"type": "pointerUp", "button": 0})
		return map[string]any{"type": "pointer", "id": id, "parameters": map[string]any{"pointerType": "touch"}, "actions": actions}
	}
	result := make([]map[string]any, 0, len(paths))
	for index, path := range paths {
		result = append(result, sequence(fmt.Sprintf("finger%d", index+1), path))
	}
	return result, nil
}

func (i *iosDevice) Apps(ctx context.Context) ([]string, error) {
	if i.simulator != "" {
		output, err := runProcess(ctx, "xcrun", "simctl", "listapps", i.simulator)
		if err != nil {
			return nil, err
		}
		var apps []string
		for _, match := range simulatorApp.FindAllStringSubmatch(string(output), 1000) {
			apps = append(apps, match[1])
		}
		sort.Strings(apps)
		return apps, nil
	}
	var entries map[string]json.RawMessage
	if err := i.mobile(ctx, "listApps", nil, &entries); err != nil {
		return nil, err
	}
	apps := make([]string, 0, len(entries))
	for id := range entries {
		if packageMatch.MatchString(id) {
			apps = append(apps, id)
		}
	}
	sort.Strings(apps)
	return apps, nil
}

var simulatorApp = regexp.MustCompile(`(?m)^\s*"([A-Za-z][A-Za-z0-9_.-]+)"\s*=\s*\{`)

func (i *iosDevice) App(ctx context.Context, id string, launch bool) error {
	if !packageMatch.MatchString(id) {
		return wire.ErrUnsupported
	}
	name := "terminateApp"
	if launch {
		name = "activateApp"
	}
	if err := i.mobile(ctx, name, map[string]any{"bundleId": id}, nil); err != nil {
		return wire.ErrUnknown
	}
	return nil
}

func (i *iosDevice) Browser(ctx context.Context, op wire.Operation) (wire.Result, error) {
	var contexts []string
	if err := i.command(ctx, "GET", "/contexts", nil, &contexts); err != nil {
		return wire.Result{}, err
	}
	available := []wire.BrowserContext{}
	var selected string
	for _, name := range contexts {
		if !strings.HasPrefix(name, "WEBVIEW_") {
			continue
		}
		id := documentID("webkit", name)
		available = append(available, wire.BrowserContext{ID: id, Title: boundedText(name, 128)})
		if op.Browser != nil && id == op.Browser.ContextID {
			selected = name
		}
	}
	if op.Kind == "browser_contexts" {
		return wire.Result{Contexts: available}, nil
	}
	if selected == "" {
		return wire.Result{}, wire.ErrStale
	}
	if err := i.command(ctx, "POST", "/context", map[string]any{"name": selected}, nil); err != nil {
		return wire.Result{}, err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = i.command(ctx, "POST", "/context", map[string]any{"name": "NATIVE_APP"}, nil)
	}()
	var keys []string
	if err := i.script(ctx, "return "+documentExpression, nil, &keys); err != nil || len(keys) != 2 || publishedURL(keys[1]) == "" && keys[1] != "about:blank" {
		return wire.Result{}, wire.ErrUnsupported
	}
	generation := documentID(op.Browser.ContextID, wire.Marshal(keys))
	if op.Kind == "browser_observe" {
		var page wire.BrowserPage
		if err := i.script(ctx, "return "+pageExpression, nil, &page); err != nil {
			return wire.Result{}, err
		}
		var current []string
		if err := i.script(ctx, "return "+documentExpression, nil, &current); err != nil || documentID(op.Browser.ContextID, wire.Marshal(current)) != generation {
			return wire.Result{}, wire.ErrStale
		}
		page.ContextID, page.DocumentID, page.URL = op.Browser.ContextID, generation, publishedURL(page.URL)
		return wire.Result{Page: &page}, nil
	}
	if op.Browser.DocumentID != generation {
		return wire.Result{}, wire.ErrStale
	}
	if op.Browser.Type == "navigate" {
		if err := i.command(ctx, "POST", "/url", map[string]any{"url": op.Browser.URL}, nil); err != nil {
			return wire.Result{}, wire.ErrUnknown
		}
		return wire.Result{}, nil
	}
	var outcome string
	if err := i.script(ctx, "return "+actionExpression(*op.Browser), nil, &outcome); err != nil {
		return wire.Result{}, wire.ErrUnknown
	}
	return wire.Result{}, browserActionError(outcome)
}

func (i *iosDevice) Close(ctx context.Context) { _ = i.command(ctx, "DELETE", "", nil, nil) }
