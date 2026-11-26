package connector

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mobile-use/internal/wire"
)

type Android struct {
	ADB          string
	UseHelper    bool
	ScrcpyServer string
	Catalog      *AndroidCatalog
}

type helperProbeResult struct {
	checkedAt  time.Time
	multiTouch bool
}

var helperProbeCache = struct {
	sync.Mutex
	results map[string]helperProbeResult
}{results: make(map[string]helperProbeResult)}

func (a Android) path() string {
	if a.ADB != "" {
		return a.ADB
	}
	return "adb"
}

func (a Android) Discover(ctx context.Context) ([]Found, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	data, err := runProcess(ctx, a.path(), "devices", "-l")
	if err != nil {
		return nil, err
	}
	var devices []Found
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 || parts[1] != "device" || len(parts[0]) > 192 {
			continue
		}
		label := "Android device"
		for _, field := range parts[2:] {
			if model, ok := strings.CutPrefix(field, "model:"); ok && model != "" {
				label = strings.ReplaceAll(model, "_", " ")
				break
			}
		}
		virtual := strings.HasPrefix(parts[0], "emulator-")
		capabilities := wire.Capabilities{wire.Screen: true, wire.Tree: true, wire.Touch: true, wire.Text: true, wire.Apps: true, wire.Browser: true}
		catalog := a.Catalog.items()
		if len(catalog) > 0 {
			capabilities[wire.AppManagement] = true
		}
		if a.UseHelper {
			capabilities[wire.MultiTouch] = a.helperSupportsTouchPaths(ctx, parts[0])
		}
		devices = append(devices, Found{NativeID: parts[0], Inventory: wire.Inventory{
			Label: label, Platform: "android", Mode: "adb", Virtual: virtual,
			Capabilities: capabilities, LiveVideo: a.ScrcpyServer != "", Catalog: catalog,
		}})
	}
	return devices, nil
}

func (a Android) helperSupportsTouchPaths(ctx context.Context, id string) bool {
	key := a.path() + "\x00" + id
	now := time.Now()
	helperProbeCache.Lock()
	if cached, ok := helperProbeCache.results[key]; ok && now.Sub(cached.checkedAt) < time.Minute {
		helperProbeCache.Unlock()
		return cached.multiTouch
	}
	helperProbeCache.Unlock()

	multiTouch := false
	installed, err := runProcess(ctx, a.path(), "-s", id, "shell", "pm path org.mobileuse.control")
	if err == nil && bytes.HasPrefix(installed, []byte("package:")) && ctx.Err() == nil {
		probe := base64.RawURLEncoding.EncodeToString([]byte(wire.Marshal(map[string]string{"type": "capabilities"})))
		version, probeErr := runProcess(ctx, a.path(), "-s", id, "shell", "am instrument -w -e request "+probe+" org.mobileuse.control/.ControlInstrumentation")
		multiTouch = probeErr == nil && bytes.Contains(version, []byte("mobile_use_result=touch_paths_v1"))
	}
	if ctx.Err() != nil {
		return false
	}
	helperProbeCache.Lock()
	if len(helperProbeCache.results) >= 256 {
		for cachedKey, cached := range helperProbeCache.results {
			if now.Sub(cached.checkedAt) >= time.Minute {
				delete(helperProbeCache.results, cachedKey)
			}
		}
		if len(helperProbeCache.results) >= 256 {
			for cachedKey := range helperProbeCache.results {
				delete(helperProbeCache.results, cachedKey)
				break
			}
		}
	}
	helperProbeCache.results[key] = helperProbeResult{checkedAt: now, multiTouch: multiTouch}
	helperProbeCache.Unlock()
	return multiTouch
}

func (a Android) Open(_ context.Context, found Found) (Driver, error) {
	return &androidDevice{adb: a.path(), id: found.NativeID, dumpPath: "/data/local/tmp/mobile-use-" + wire.NewID() + ".xml", helper: found.Capabilities[wire.MultiTouch], catalog: a.Catalog}, nil
}

type androidDevice struct {
	adb      string
	id       string
	dumpPath string
	browser  *cdpBrowser
	helper   bool
	catalog  *AndroidCatalog
}

func (a *androidDevice) run(ctx context.Context, args ...string) ([]byte, error) {
	return runProcess(ctx, a.adb, append([]string{"-s", a.id}, args...)...)
}

func (a *androidDevice) shell(ctx context.Context, command string) (string, error) {
	data, err := a.run(ctx, "shell", command)
	return string(data), err
}

func runProcess(ctx context.Context, path string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = time.Second
	var output limitedBuffer
	cmd.Stdout = &output
	// Device output and command arguments can contain private data.
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var missing *exec.Error
		if errors.As(err, &missing) {
			return nil, wire.Error("TOOL_MISSING", "A required platform executable is missing.", 503)
		}
		return nil, wire.Error("DEVICE_COMMAND_FAILED", "The platform command failed.", 502)
	}
	return output.Bytes(), nil
}

type limitedBuffer struct{ buffer bytes.Buffer }

func (b *limitedBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 12<<20 {
		return 0, fmt.Errorf("platform output limit exceeded")
	}
	return b.buffer.Write(p)
}

var (
	rotationMatch = regexp.MustCompile(`(?i)(?:SurfaceOrientation:\s*|mCurrentOrientation=|mRotation=ROTATION_)([0-9]+)`)
	displayMatch  = regexp.MustCompile(`(?:Physical|Override) size:\s*(\d+)x(\d+)`)
	focusMatch    = regexp.MustCompile(`mCurrentFocus=.*?\s([a-zA-Z][a-zA-Z0-9_.]+)/[^\s}]+`)
	lockMatch     = regexp.MustCompile(`(?i)\b(?:showingAndNotOccluded|mShowingLockscreen|mShowingDream|isStatusBarKeyguard|mKeyguardShowing|isKeyguardShowing|isKeyguardShowingAndNotOccluded)\s*=\s*true\b`)
	boundsMatch   = regexp.MustCompile(`^\[(-?\d+),(-?\d+)\]\[(-?\d+),(-?\d+)\]$`)
	packageMatch  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z0-9_]+)+$`)
	activityMatch = regexp.MustCompile(`^[A-Za-z_.$][A-Za-z0-9_.$]+$`)
)

func (a *androidDevice) Display(ctx context.Context) (Display, error) {
	data, err := a.shell(ctx, "wm size; dumpsys input; dumpsys window")
	if err != nil {
		return Display{}, err
	}
	dimensions := displayMatch.FindAllStringSubmatch(data, -1)
	if len(dimensions) == 0 {
		return Display{}, wire.ErrUnsupported
	}
	size := dimensions[len(dimensions)-1]
	width, _ := strconv.Atoi(size[1])
	height, _ := strconv.Atoi(size[2])
	rotation := "0"
	if value := rotationMatch.FindStringSubmatch(data); len(value) == 2 {
		rotation = value[1]
	}
	if rotation == "1" || rotation == "3" || rotation == "90" || rotation == "270" {
		width, height = height, width
	}
	app := ""
	if value := focusMatch.FindStringSubmatch(data); len(value) == 2 {
		app = value[1]
	}
	locked := lockMatch.MatchString(data) || strings.Contains(data, "mCurrentFocus=Window{") && strings.Contains(strings.SplitN(strings.SplitN(data, "mCurrentFocus=Window{", 2)[1], "\n", 2)[0], "NotificationShade")
	if app == "" && !locked {
		return Display{}, wire.Error("DEVICE_TRANSITION", "Wait for the foreground app to settle and observe again.", 409)
	}
	return Display{Width: width, Height: height, AppID: app, Orientation: "rotation_" + rotation, Locked: locked}, nil
}

func (a *androidDevice) Screenshot(ctx context.Context) (*wire.Image, error) {
	data, err := a.run(ctx, "exec-out", "screencap", "-p")
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		return nil, wire.ErrUnsupported
	}
	return &wire.Image{MIME: "image/png", Data: data}, nil
}

func (a *androidDevice) Tree(ctx context.Context) ([]wire.Node, error) {
	if _, err := a.shell(ctx, "uiautomator dump "+a.dumpPath+" >/dev/null"); err != nil {
		return nil, err
	}
	data, err := a.run(ctx, "exec-out", "cat", a.dumpPath)
	_, _ = a.shell(ctx, "rm -f "+a.dumpPath)
	if err != nil {
		return nil, err
	}
	return androidTree(data)
}

func androidTree(data []byte) ([]wire.Node, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	return parseNodes(decoder, func(element xml.StartElement) (wire.Node, bool) {
		if element.Name.Local != "node" {
			return wire.Node{}, false
		}
		values := attrs(element)
		password := values["password"] == "true"
		label := values["text"]
		if label == "" {
			label = values["content-desc"]
		}
		if password {
			label = ""
		}
		return wire.Node{
			Role: values["class"], Label: label, Hint: values["hint"], ResourceID: values["resource-id"],
			Bounds: androidBounds(values["bounds"]), Enabled: values["enabled"] == "true",
			Visible: values["visible-to-user"] != "false", VisibilityKnown: true, Clickable: values["clickable"] == "true",
			LongClickable: values["long-clickable"] == "true", Focusable: values["focusable"] == "true",
			Editable:  values["editable"] == "true" || strings.HasSuffix(values["class"], "EditText"),
			Checkable: values["checkable"] == "true", Checked: values["checked"] == "true",
			Selected: values["selected"] == "true", Scrollable: values["scrollable"] == "true",
			Protected: password, Focused: values["focused"] == "true",
		}, true
	})
}

func attrs(e xml.StartElement) map[string]string {
	values := make(map[string]string, len(e.Attr))
	for _, attribute := range e.Attr {
		values[attribute.Name.Local] = attribute.Value
	}
	return values
}

func parseNodes(decoder *xml.Decoder, parse func(xml.StartElement) (wire.Node, bool)) ([]wire.Node, error) {
	var parents []string
	result := make([]wire.Node, 0, 128)
	for len(result) < 1200 {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, wire.ErrUnsupported
		}
		switch e := token.(type) {
		case xml.StartElement:
			if len(parents) >= 128 {
				return nil, wire.ErrUnsupported
			}
			parent := ""
			if len(parents) > 0 {
				parent = parents[len(parents)-1]
			}
			node, ok := parse(e)
			if ok {
				node.Label = boundedText(node.Label, 256)
				node.Hint = boundedText(node.Hint, 256)
				node.Role = boundedText(node.Role, 128)
				node.ResourceID = boundedText(node.ResourceID, 256)
				node.ParentID = parent
				hash := sha256.Sum256([]byte(fmt.Sprintf("%d/%s/%s/%s/%s/%+v", len(result), parent, node.Role, node.Label, node.ResourceID, node.Bounds)))
				node.ID = hex.EncodeToString(hash[:10])
				result = append(result, node)
				parent = node.ID
			}
			parents = append(parents, parent)
		case xml.EndElement:
			if len(parents) > 0 {
				parents = parents[:len(parents)-1]
			}
		}
	}
	return result, nil
}

func boundedText(value string, limit int) string {
	chars := []rune(value)
	if len(chars) > limit {
		return string(chars[:limit])
	}
	return value
}

func androidBounds(raw string) wire.Rect {
	parts := boundsMatch.FindStringSubmatch(raw)
	if len(parts) != 5 {
		return wire.Rect{}
	}
	x, _ := strconv.Atoi(parts[1])
	y, _ := strconv.Atoi(parts[2])
	x2, _ := strconv.Atoi(parts[3])
	y2, _ := strconv.Atoi(parts[4])
	return wire.Rect{X: x, Y: y, Width: x2 - x, Height: y2 - y}
}

func shellQuoted(input string) string { return "'" + strings.ReplaceAll(input, "'", "'\"'\"'") + "'" }

func (a *androidDevice) Action(ctx context.Context, action wire.Action, display Display, rect *wire.Rect) error {
	var command string
	switch action.Type {
	case "tap", "double_tap", "long_press":
		x, y, err := androidActionStart(action, display, rect)
		if err != nil {
			return err
		}
		if action.Type == "tap" {
			command = fmt.Sprintf("input tap %d %d", x, y)
		} else if action.Type == "double_tap" {
			command = fmt.Sprintf("input tap %d %d && sleep 0.08 && input tap %d %d", x, y, x, y)
		} else {
			duration := action.DurationMS
			if duration == 0 {
				duration = 650
			}
			command = fmt.Sprintf("input swipe %d %d %d %d %d", x, y, x, y, duration)
		}
	case "swipe", "drag", "scroll", "fling":
		paths := action.GesturePaths()
		if len(paths) == 0 && action.Type == "scroll" && rect != nil {
			path, err := nodeScrollPath(*rect, display, action.Direction)
			if err != nil {
				return err
			}
			paths = []wire.TouchPath{path}
		}
		if len(paths) != 1 {
			return wire.ErrUnsupported
		}
		x, y := Pixel(paths[0].Start, display)
		x2, y2 := Pixel(paths[0].End, display)
		duration := action.DurationMS
		if duration == 0 {
			duration = 350
			if action.Type == "fling" {
				duration = 120
			}
		}
		gesture := "swipe"
		if action.Type == "drag" {
			gesture = "draganddrop"
		}
		command = fmt.Sprintf("input %s %d %d %d %d %d", gesture, x, y, x2, y2, duration)
	case "type":
		if a.helper {
			return a.instrument(ctx, map[string]any{"type": "type", "text": action.Text, "app_id": display.AppID})
		}
		nodes, err := a.Tree(ctx)
		if err != nil {
			return wire.ErrUnsupported
		}
		for _, node := range nodes {
			if node.Focused && node.Protected {
				return wire.ErrLocked
			}
		}
		for _, char := range action.Text {
			if char < 0x20 || char > 0x7e || char == '%' {
				return wire.Error("TEXT_UNAVAILABLE", "This Android input path supports printable ASCII without percent escapes.", 422)
			}
		}
		command = "input text " + shellQuoted(strings.ReplaceAll(action.Text, " ", "%s"))
	case "pinch", "rotate", "multi_touch", "two_finger_tap":
		if !a.helper {
			return wire.ErrUnsupported
		}
		paths := action.GesturePaths()
		pixels := make([][][2]int, 0, len(paths))
		for _, path := range paths {
			x1, y1 := Pixel(path.Start, display)
			x2, y2 := Pixel(path.End, display)
			pixels = append(pixels, [][2]int{{x1, y1}, {x2, y2}})
		}
		duration := action.DurationMS
		if duration == 0 {
			duration = 350
			if action.Type == "two_finger_tap" {
				duration = 100
			}
		}
		return a.instrument(ctx, map[string]any{"type": "touches", "paths": pixels, "duration_ms": duration})
	case "key":
		switch action.Key {
		case "notification_shade":
			command = "cmd statusbar expand-notifications"
		case "quick_settings":
			command = "cmd statusbar expand-settings"
		default:
			key := map[string]string{
				"home": "HOME", "back": "BACK", "overview": "APP_SWITCH", "enter": "ENTER", "backspace": "DEL",
				"tab": "TAB", "escape": "ESCAPE", "menu": "MENU", "dpad_up": "DPAD_UP", "dpad_down": "DPAD_DOWN",
				"dpad_left": "DPAD_LEFT", "dpad_right": "DPAD_RIGHT", "volume_up": "VOLUME_UP", "volume_down": "VOLUME_DOWN",
				"volume_mute": "VOLUME_MUTE",
			}[action.Key]
			if key == "" {
				return wire.ErrUnsupported
			}
			command = "input keyevent KEYCODE_" + key
		}
	default:
		return wire.ErrUnsupported
	}
	_, err := a.shell(ctx, command)
	if err != nil {
		return wire.ErrUnknown
	}
	return nil
}

func androidActionStart(action wire.Action, display Display, rect *wire.Rect) (int, int, error) {
	if rect != nil {
		x, y := rect.X+rect.Width/2, rect.Y+rect.Height/2
		if x >= 0 && x < display.Width && y >= 0 && y < display.Height {
			return x, y, nil
		}
		return 0, 0, wire.ErrStale
	}
	if len(action.Paths) > 0 {
		x, y := Pixel(action.Paths[0].Start, display)
		if x >= 0 && x < display.Width && y >= 0 && y < display.Height {
			return x, y, nil
		}
		return 0, 0, wire.ErrStale
	}
	if action.Point != nil {
		x, y := Pixel(*action.Point, display)
		if x >= 0 && x < display.Width && y >= 0 && y < display.Height {
			return x, y, nil
		}
	}
	return 0, 0, wire.ErrStale
}

func (a *androidDevice) instrument(ctx context.Context, payload map[string]any) error {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(wire.Marshal(payload)))
	output, err := a.shell(ctx, "am instrument -w -e request "+encoded+" org.mobileuse.control/.ControlInstrumentation")
	if err != nil {
		return wire.ErrUnknown
	}
	switch {
	case strings.Contains(output, "INSTRUMENTATION_RESULT: mobile_use_result=ok"):
		return nil
	case strings.Contains(output, "INSTRUMENTATION_RESULT: mobile_use_result=protected"):
		return wire.ErrLocked
	case strings.Contains(output, "INSTRUMENTATION_RESULT: mobile_use_result=unsupported"):
		return wire.ErrUnsupported
	case strings.Contains(output, "INSTRUMENTATION_RESULT: mobile_use_result=no_editor"):
		return wire.Error("FOCUS_NOT_EDITABLE", "Focus a supported text field before typing.", 422)
	case strings.Contains(output, "INSTRUMENTATION_RESULT: mobile_use_result=text_rejected"):
		return wire.Error("TEXT_REJECTED", "The focused field did not accept text insertion.", 422)
	default:
		return wire.ErrUnknown
	}
}

func (a *androidDevice) Apps(ctx context.Context) ([]string, error) {
	data, err := a.shell(ctx, "pm list packages --user current")
	if err != nil {
		return nil, err
	}
	apps := []string{}
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() && len(apps) < 1000 {
		id, ok := strings.CutPrefix(strings.TrimSpace(scanner.Text()), "package:")
		if ok && packageMatch.MatchString(id) {
			apps = append(apps, id)
		}
	}
	sort.Strings(apps)
	return apps, scanner.Err()
}

func (a *androidDevice) App(ctx context.Context, id string, launch bool) error {
	if !packageMatch.MatchString(id) {
		return wire.ErrUnsupported
	}
	command := "am force-stop --user current " + id
	if launch {
		resolved, err := a.shell(ctx, "cmd package resolve-activity --brief --user current -a android.intent.action.MAIN -c android.intent.category.LAUNCHER "+id)
		if err != nil {
			return wire.ErrUnsupported
		}
		component := ""
		for _, item := range strings.Fields(resolved) {
			pkg, class, ok := strings.Cut(item, "/")
			if ok && pkg == id && activityMatch.MatchString(class) {
				component = item
			}
		}
		if component == "" {
			return wire.ErrUnsupported
		}
		command = "am start --user current -n " + shellQuoted(component)
	}
	output, err := a.shell(ctx, command)
	if err != nil {
		return wire.ErrUnknown
	}
	if strings.Contains(output, "Error:") {
		return wire.ErrUnsupported
	}
	return nil
}

func (a *androidDevice) Close(ctx context.Context) {
	if a.browser != nil {
		a.browser.close(ctx)
		a.browser = nil
	}
	_, _ = a.shell(ctx, "rm -f "+a.dumpPath)
}
