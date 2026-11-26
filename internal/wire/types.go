package wire

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

type Fault struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"`
}

func (e *Fault) Error() string { return e.Code + ": " + e.Message }

func Error(code, message string, status int) *Fault {
	return &Fault{Code: code, Message: message, Status: status}
}

func Public(err error) *Fault {
	var fault *Fault
	if errors.As(err, &fault) {
		return fault
	}
	return Error("INTERNAL", "An internal error occurred. See server diagnostics.", http.StatusInternalServerError)
}

var (
	ErrNotFound    = Error("NOT_FOUND", "The resource is unavailable.", 404)
	ErrForbidden   = Error("FORBIDDEN", "This identity does not have permission for the operation.", 403)
	ErrOffline     = Error("DEVICE_OFFLINE", "The device connector is offline or the device is disconnected.", 503)
	ErrBusy        = Error("DEVICE_BUSY", "The device already has a controller.", 409)
	ErrExpired     = Error("SESSION_INACTIVE", "The session is inactive or its lease has expired.", 409)
	ErrUnsupported = Error("UNSUPPORTED", "This operation is unavailable in the current device context.", 422)
	ErrUnknown     = Error("OUTCOME_UNKNOWN", "The connector could not confirm the outcome. Observe the device before issuing another action.", 409)
	ErrStale       = Error("STALE_REFERENCE", "Observe the current screen or browser document before attempting this action.", 409)
	ErrConflict    = Error("IDEMPOTENCY_CONFLICT", "That operation ID was used with a different request.", 409)
	ErrLocked      = Error("OWNER_ACTION_REQUIRED", "The owner must unlock the phone or complete the protected prompt.", 409)
)

type Capability string

const (
	Screen        Capability = "screen"
	Tree          Capability = "ui_tree"
	Touch         Capability = "touch"
	Text          Capability = "text"
	Apps          Capability = "apps"
	Browser       Capability = "browser"
	Camera        Capability = "camera"
	Microphone    Capability = "microphone"
	MultiTouch    Capability = "multi_touch"
	AppManagement Capability = "app_management"
)

type Capabilities map[Capability]bool

func (caps Capabilities) Allows(required []Capability) bool {
	for _, item := range required {
		if !caps[item] {
			return false
		}
	}
	return true
}

type Actor struct {
	Workspace string    `json:"workspace"`
	Subject   string    `json:"subject"`
	Role      string    `json:"role"`
	ExpiresAt time.Time `json:"-"`
}

const (
	Admin    = "admin"
	Operator = "operator"
	Owner    = "owner"
	Agent    = "agent"
	Auditor  = "auditor"
)

func (a Actor) CanManage() bool { return a.Role == Admin || a.Role == Operator }

type Grant struct {
	Subject      string       `json:"subject"`
	Capabilities []Capability `json:"capabilities"`
}

type Inventory struct {
	ID           string        `json:"id"`
	Label        string        `json:"label"`
	Platform     string        `json:"platform"`
	Mode         string        `json:"mode"`
	Virtual      bool          `json:"virtual"`
	Capabilities Capabilities  `json:"capabilities"`
	LiveVideo    bool          `json:"live_video"`
	Catalog      []CatalogItem `json:"catalog,omitempty"`
}

type CatalogItem struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	AppID string `json:"app_id"`
}

type VideoFrame struct {
	Data           []byte `json:"data"`
	DurationMicros int64  `json:"duration_micros"`
}

type Device struct {
	Inventory
	Connector string  `json:"connector,omitempty"`
	Owner     string  `json:"owner,omitempty"`
	Profile   string  `json:"profile"`
	Grants    []Grant `json:"grants,omitempty"`
	Online    bool    `json:"online"`
	Enabled   bool    `json:"enabled"`
}

const (
	Personal  = "personal"
	Dedicated = "dedicated"
)

type Enrollment struct {
	Label   string  `json:"label"`
	Owner   string  `json:"owner"`
	Profile string  `json:"profile"`
	Grants  []Grant `json:"grants,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
}

type Session struct {
	ID                string       `json:"id"`
	DeviceID          string       `json:"device_id"`
	Requester         string       `json:"requester"`
	Controller        string       `json:"controller"`
	Owner             string       `json:"owner"`
	Purpose           string       `json:"purpose"`
	Capabilities      []Capability `json:"capabilities"`
	Profile           string       `json:"profile"`
	State             string       `json:"state"`
	Generation        int64        `json:"generation"`
	CreatedAt         time.Time    `json:"created_at"`
	ExpiresAt         time.Time    `json:"expires_at"`
	LastActivity      time.Time    `json:"last_activity"`
	OwnerPresentUntil time.Time    `json:"owner_present_until,omitempty"`
	Reason            string       `json:"reason,omitempty"`
	Paused            bool         `json:"paused,omitempty"`
}

const (
	Pending = "pending_approval"
	Active  = "active"
	Ended   = "ended"
	Denied  = "denied"
	Expired = "expired"
)

type StartSession struct {
	DeviceID     string       `json:"device_id"`
	Purpose      string       `json:"purpose"`
	Capabilities []Capability `json:"capabilities"`
}

type Operation struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Observe     *ObserveRequest `json:"observe,omitempty"`
	Action      *Action         `json:"action,omitempty"`
	Browser     *BrowserRequest `json:"browser,omitempty"`
	AppID       string          `json:"app_id,omitempty"`
	Sensor      *SensorRequest  `json:"sensor,omitempty"`
	Maintenance *Maintenance    `json:"maintenance,omitempty"`
}

type Maintenance struct {
	ArtifactID string `json:"artifact_id"`
	Confirm    string `json:"confirm"`
}

type SensorRequest struct {
	Type            Capability `json:"type"`
	RequestID       string     `json:"request_id,omitempty"`
	DurationSeconds int        `json:"duration_seconds,omitempty"`
}

type SensorStatus struct {
	ID              string     `json:"id"`
	Type            Capability `json:"type"`
	SessionID       string     `json:"session_id"`
	State           string     `json:"state"`
	DurationSeconds int        `json:"duration_seconds,omitempty"`
	ExpiresAt       time.Time  `json:"expires_at"`
}

type ObserveRequest struct {
	Screen    bool   `json:"screen"`
	Tree      bool   `json:"tree"`
	WaitFor   string `json:"wait_for,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Action struct {
	Type        string `json:"type"`
	FrameID     string `json:"frame_id,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	Point       *Point `json:"point,omitempty"`
	End         *Point `json:"end,omitempty"`
	SecondPoint *Point `json:"second_point,omitempty"`
	SecondEnd   *Point `json:"second_end,omitempty"`
	DurationMS  int    `json:"duration_ms,omitempty"`
	Text        string `json:"text,omitempty"`
	Key         string `json:"key,omitempty"`
}

type BrowserRequest struct {
	ContextID  string `json:"context_id,omitempty"`
	DocumentID string `json:"document_id,omitempty"`
	Type       string `json:"type,omitempty"`
	URL        string `json:"url,omitempty"`
	Selector   string `json:"selector,omitempty"`
	Text       string `json:"text,omitempty"`
	DeltaY     int    `json:"delta_y,omitempty"`
}

type Observation struct {
	FrameID     string    `json:"frame_id"`
	CapturedAt  time.Time `json:"captured_at"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	Orientation string    `json:"orientation"`
	AppID       string    `json:"app_id,omitempty"`
	Image       *Image    `json:"image,omitempty"`
	Nodes       []Node    `json:"nodes,omitempty"`
	Partial     []string  `json:"partial,omitempty"`
}

type Image struct {
	MIME string `json:"mime"`
	Data []byte `json:"data"`
}

type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type Node struct {
	ID         string `json:"id"`
	ParentID   string `json:"parent_id,omitempty"`
	Role       string `json:"role,omitempty"`
	Label      string `json:"label,omitempty"`
	ResourceID string `json:"resource_id,omitempty"`
	Bounds     Rect   `json:"bounds"`
	Enabled    bool   `json:"enabled"`
	Clickable  bool   `json:"clickable"`
	Protected  bool   `json:"protected,omitempty"`
	Focused    bool   `json:"focused,omitempty"`
}

type BrowserContext struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
}

type BrowserPage struct {
	ContextID  string           `json:"context_id"`
	DocumentID string           `json:"document_id"`
	Title      string           `json:"title"`
	URL        string           `json:"url"`
	Elements   []BrowserElement `json:"elements"`
}

type BrowserElement struct {
	Tag   string `json:"tag"`
	ID    string `json:"id,omitempty"`
	Role  string `json:"role,omitempty"`
	Text  string `json:"text,omitempty"`
	Label string `json:"label,omitempty"`
	Type  string `json:"type,omitempty"`
}

type Result struct {
	ID          string           `json:"id"`
	State       string           `json:"state"`
	Observation *Observation     `json:"observation,omitempty"`
	Apps        []string         `json:"apps,omitempty"`
	Contexts    []BrowserContext `json:"contexts,omitempty"`
	Page        *BrowserPage     `json:"page,omitempty"`
	Fault       *Fault           `json:"error,omitempty"`
	Sensor      *SensorStatus    `json:"sensor,omitempty"`
	Artifact    *Image           `json:"artifact,omitempty"`
}

const (
	Executing = "executing"
	Succeeded = "succeeded"
	Failed    = "failed"
	Unknown   = "outcome_unknown"
)

type AuditEvent struct {
	At        time.Time `json:"at"`
	Actor     string    `json:"actor"`
	Event     string    `json:"event"`
	Device    string    `json:"device,omitempty"`
	Session   string    `json:"session,omitempty"`
	Operation string    `json:"operation,omitempty"`
	Code      string    `json:"code,omitempty"`
}

type Dispatch struct {
	DeviceID   string    `json:"device_id"`
	SessionID  string    `json:"session_id"`
	Generation int64     `json:"generation"`
	Deadline   time.Time `json:"deadline"`
	Personal   bool      `json:"personal"`
	Operation  Operation `json:"operation"`
}

const AppMaintenanceTimeout = 20 * time.Second

func (op Operation) IsAppMaintenance() bool {
	return op.Kind == "app_install" || op.Kind == "app_reset"
}

func NewID() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

var opaqueID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:@-]{0,127}$`)

func ValidID(value string) bool { return opaqueID.MatchString(value) }

func ValidCapability(value Capability) bool {
	return slices.Contains([]Capability{Screen, Tree, Touch, Text, Apps, Browser, Camera, Microphone, MultiTouch, AppManagement}, value)
}

func Marshal(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func (p *Point) valid() bool {
	return p != nil && p.X >= 0 && p.X < 1 && p.Y >= 0 && p.Y < 1
}

func (op Operation) Required() ([]Capability, error) {
	invalid := Error("INVALID_OPERATION", "The operation arguments are invalid.", 400)
	if !ValidID(op.ID) {
		return nil, invalid
	}
	switch op.Kind {
	case "observe":
		if op.Observe == nil || !op.Observe.Screen && !op.Observe.Tree || op.Observe.TimeoutMS < 0 || op.Observe.TimeoutMS > 8000 || len(op.Observe.WaitFor) > 128 {
			return nil, invalid
		}
		var caps []Capability
		if op.Observe.Screen {
			caps = append(caps, Screen)
		}
		if op.Observe.Tree || op.Observe.WaitFor != "" {
			caps = append(caps, Tree)
		}
		return caps, nil
	case "act":
		a := op.Action
		if a == nil || a.DurationMS < 0 || a.DurationMS > 3000 {
			return nil, invalid
		}
		switch a.Type {
		case "tap", "long_press":
			if !ValidID(a.FrameID) || (a.Point != nil) == (a.NodeID != "") || a.Point != nil && !a.Point.valid() || a.NodeID != "" && !ValidID(a.NodeID) {
				return nil, invalid
			}
			return []Capability{Touch}, nil
		case "swipe", "drag", "pinch":
			if !ValidID(a.FrameID) || !a.Point.valid() || !a.End.valid() {
				return nil, invalid
			}
			if a.Type == "pinch" {
				if !a.SecondPoint.valid() || !a.SecondEnd.valid() {
					return nil, invalid
				}
				return []Capability{Touch, MultiTouch}, nil
			}
			return []Capability{Touch}, nil
		case "type":
			if !ValidID(a.FrameID) || a.Text == "" || len(a.Text) > 4096 || strings.ContainsRune(a.Text, 0) {
				return nil, invalid
			}
			return []Capability{Text}, nil
		case "key":
			if !ValidID(a.FrameID) || !slices.Contains([]string{"home", "back", "overview", "enter", "backspace", "volume_up", "volume_down"}, a.Key) {
				return nil, invalid
			}
			return []Capability{Touch}, nil
		}
	case "apps_list":
		return []Capability{Apps}, nil
	case "app_launch", "app_terminate":
		if !ValidID(op.AppID) || !strings.Contains(op.AppID, ".") {
			return nil, invalid
		}
		return []Capability{Apps, Touch}, nil
	case "app_install", "app_reset":
		request := op.Maintenance
		verb := "install"
		if op.Kind == "app_reset" {
			verb = "reset"
		}
		if request == nil || !ValidID(request.ArtifactID) || request.Confirm != verb+" "+request.ArtifactID {
			return nil, Error("MAINTENANCE_CONFIRMATION", "Confirm the named maintenance operation before dispatch.", 400)
		}
		return []Capability{Apps, Touch, AppManagement}, nil
	case "browser_contexts":
		return []Capability{Browser}, nil
	case "sensor_request", "sensor_status", "artifact_get":
		sensor := op.Sensor
		if sensor == nil || sensor.Type != Camera && sensor.Type != Microphone || sensor.DurationSeconds < 0 || sensor.DurationSeconds > 15 ||
			op.Kind != "sensor_request" && !ValidID(sensor.RequestID) {
			return nil, invalid
		}
		return []Capability{sensor.Type}, nil
	case "browser_observe", "browser_act":
		b := op.Browser
		if b == nil || !ValidID(b.ContextID) || len(b.Selector) > 512 || len(b.Text) > 4096 {
			return nil, invalid
		}
		if op.Kind == "browser_observe" {
			return []Capability{Browser}, nil
		}
		if !ValidID(b.DocumentID) {
			return nil, invalid
		}
		switch b.Type {
		case "navigate":
			u, err := url.Parse(b.URL)
			if err != nil || len(b.URL) > 2048 || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
				return nil, invalid
			}
		case "click", "fill":
			if b.Selector == "" {
				return nil, invalid
			}
		case "scroll":
			if b.DeltaY < -2000 || b.DeltaY > 2000 {
				return nil, invalid
			}
		default:
			return nil, invalid
		}
		return []Capability{Browser, Touch}, nil
	}
	return nil, invalid
}

func ValidateCapabilities(caps []Capability) error {
	seen := make(Capabilities)
	if len(caps) == 0 {
		return Error("INVALID_CAPABILITIES", "Request at least one capability.", 400)
	}
	for _, item := range caps {
		if !ValidCapability(item) || seen[item] {
			return Error("INVALID_CAPABILITIES", fmt.Sprintf("Invalid or repeated capability %q.", item), 400)
		}
		seen[item] = true
	}
	return nil
}
