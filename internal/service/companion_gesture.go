package service

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"mobile-use/internal/store"
	"mobile-use/internal/wire"
)

type gestureSubscriber struct {
	channel    chan gestureEvent
	session    string
	generation int64
}

type gestureEvent struct {
	data       []byte
	session    string
	generation int64
}

type pointerGesturePayload struct {
	Type       string           `json:"type"`
	Gesture    string           `json:"gesture"`
	DurationMS int              `json:"duration_ms"`
	Paths      []wire.TouchPath `json:"paths"`
}

// GestureConsoleRoutes exposes an owner-managed link between the screen-sharing
// companion and the ADB control entry for the same Android phone.
func (c *Companion) GestureConsoleRoutes(router *http.ServeMux, auth *Auth, origin string) {
	router.Handle("GET /v1/companion/gesture-target/{id}", auth.Require(withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
		target, err := c.Store.CompanionControlDevice(r.Context(), actor, r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"target_device_id": target})
	}), origin))
	router.Handle("PUT /v1/companion/gesture-target/{id}", auth.Require(withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
		var request struct {
			TargetDeviceID string `json:"target_device_id"`
		}
		if !readJSON(w, r, &request) {
			return
		}
		if request.TargetDeviceID != "" && !wire.ValidID(request.TargetDeviceID) {
			writeError(w, wire.Error("INVALID_GESTURE_TARGET", "Choose a valid enrolled Android ADB device.", http.StatusBadRequest))
			return
		}
		if err := c.Store.SetCompanionControlDevice(r.Context(), actor, r.PathValue("id"), request.TargetDeviceID, time.Now()); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"target_device_id": request.TargetDeviceID})
	}), origin))
}

// GesturePhoneRoutes installs the authenticated phone-side event stream. It is
// separate from media transport so video upgrades cannot delay or drop gestures.
func (c *Companion) GesturePhoneRoutes(router *http.ServeMux) {
	router.HandleFunc("GET /companion/sessions/{id}/gestures", c.phone(c.streamPhoneGestures))
}

func (c *Companion) streamPhoneGestures(w http.ResponseWriter, r *http.Request, phone store.Companion) {
	session, err := c.frameSession(r.Context(), phone, r.PathValue("id"), time.Now())
	if err != nil {
		writeError(w, err)
		return
	}
	subscriber := c.subscribeGestures(phone.ID, session.ID, session.Generation)
	defer c.unsubscribeGestures(phone.ID, subscriber.channel)
	conn, err := (&websocket.Upgrader{ReadBufferSize: 1024, WriteBufferSize: 1024}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(1024)
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	conn.SetPingHandler(func(data string) error {
		_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(3*time.Second))
	})
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(20 * time.Second)) })
	type incoming struct {
		kind int
		err  error
	}
	reads := make(chan incoming, 1)
	go func() {
		for {
			kind, _, readErr := conn.ReadMessage()
			select {
			case reads <- incoming{kind: kind, err: readErr}:
			case <-r.Context().Done():
				return
			}
			if readErr != nil {
				return
			}
		}
	}()
	keepalive := time.NewTicker(10 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case message := <-reads:
			if message.err != nil {
				return
			}
			if message.kind != websocket.PongMessage && message.kind != websocket.PingMessage {
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "read-only gesture stream"), time.Now().Add(time.Second))
				return
			}
		case event := <-subscriber.channel:
			if event.session != session.ID || event.generation != session.Generation {
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
			if conn.WriteMessage(websocket.TextMessage, event.data) != nil {
				return
			}
		case <-keepalive.C:
			if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(3*time.Second)) != nil {
				return
			}
		}
	}
}

func (c *Companion) subscribeGestures(device, session string, generation int64) gestureSubscriber {
	subscriber := gestureSubscriber{channel: make(chan gestureEvent, 4), session: session, generation: generation}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureMapsLocked()
	if c.gestureSubs[device] == nil {
		c.gestureSubs[device] = make(map[chan gestureEvent]gestureSubscriber)
	}
	c.gestureSubs[device][subscriber.channel] = subscriber
	// Opening this authenticated channel is tied to the companion's local
	// opt-in. Reassert the live preference after server restarts as well.
	c.gestureEnabled[device] = true
	return subscriber
}

func (c *Companion) unsubscribeGestures(device string, channel chan gestureEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.gestureSubs[device], channel)
	if len(c.gestureSubs[device]) == 0 {
		delete(c.gestureSubs, device)
		delete(c.gestureEnabled, device)
	}
}

func (c *Companion) PublishGesture(ctx context.Context, target store.Target, action wire.Action) {
	paths := action.GesturePaths()
	companionID := ""
	if target.Connector == phoneConnector(target.Device) {
		companionID = target.Device
	} else {
		var err error
		companionID, err = c.Store.CompanionForControlDevice(ctx, target.Workspace, target.Device)
		if err != nil || companionID == "" {
			return
		}
	}
	if target.Connector == phoneConnector(target.Device) {
		c.mu.Lock()
		if frame, ok := c.controlFrames[target.Device][action.FrameID]; ok {
			if action.NodeID != "" && frame.width > 0 && frame.height > 0 {
				if node, exists := frame.nodes[action.NodeID]; exists {
					paths = []wire.TouchPath{{
						Start: wire.Point{X: (float64(node.Bounds.X) + float64(node.Bounds.Width)/2) / float64(frame.width), Y: (float64(node.Bounds.Y) + float64(node.Bounds.Height)/2) / float64(frame.height)},
						End:   wire.Point{X: (float64(node.Bounds.X) + float64(node.Bounds.Width)/2) / float64(frame.width), Y: (float64(node.Bounds.Y) + float64(node.Bounds.Height)/2) / float64(frame.height)},
					}}
				}
			}
			delete(c.controlFrames[target.Device], action.FrameID)
			if len(c.controlFrames[target.Device]) == 0 {
				delete(c.controlFrames, target.Device)
			}
		}
		c.mu.Unlock()
	}
	if len(paths) == 0 || len(paths) > 5 {
		return
	}
	duration := action.DurationMS
	if duration < 100 {
		duration = defaultGestureDuration(action.Type)
	}
	duration = min(duration, 2000)
	payload, err := json.Marshal(pointerGesturePayload{
		Type: "gesture", Gesture: action.Type, DurationMS: duration, Paths: paths,
	})
	if err != nil {
		return
	}
	companionTarget := store.Target{
		Workspace: target.Workspace,
		Connector: phoneConnector(companionID),
		Device:    companionID,
	}
	c.mu.Lock()
	c.ensureMapsLocked()
	if !c.gestureEnabled[companionID] {
		c.mu.Unlock()
		return
	}
	subscribers := make([]gestureSubscriber, 0, len(c.gestureSubs[companionID]))
	for _, subscriber := range c.gestureSubs[companionID] {
		subscribers = append(subscribers, subscriber)
	}
	c.mu.Unlock()
	for _, subscriber := range subscribers {
		companionTarget.Session = subscriber.session
		companionTarget.Generation = subscriber.generation
		if c.Store.Confirm(ctx, companionTarget, time.Now()) != nil {
			continue
		}
		event := gestureEvent{data: payload, session: subscriber.session, generation: subscriber.generation}
		select {
		case subscriber.channel <- event:
		default:
			select {
			case <-subscriber.channel:
			default:
			}
			select {
			case subscriber.channel <- event:
			default:
			}
		}
	}
}

func defaultGestureDuration(kind string) int {
	switch kind {
	case "tap":
		return 180
	case "double_tap", "two_finger_tap":
		return 320
	case "long_press":
		return 800
	case "drag":
		return 500
	case "pinch", "rotate", "multi_touch":
		return 450
	default:
		return 350
	}
}
