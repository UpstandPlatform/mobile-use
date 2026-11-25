package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"mobile-use/internal/connector"
	"mobile-use/internal/store"
	"mobile-use/internal/wire"
)

const sensorLifetime = 2 * time.Minute
const artifactLifetime = 5 * time.Minute

type Companion struct {
	Store    *store.Store
	Origin   string
	mu       sync.Mutex
	frames   map[string]phoneFrame
	requests map[string]sensorEntry
}

type phoneFrame struct {
	observation wire.Observation
	session     string
	generation  int64
}
type sensorEntry struct {
	status            wire.SensorStatus
	workspace, device string
	artifact          *wire.Image
}

func NewCompanion(db *store.Store, origin string) *Companion {
	return &Companion{Store: db, Origin: origin, frames: make(map[string]phoneFrame), requests: make(map[string]sensorEntry)}
}

func hashToken(token string) string {
	data := sha256.Sum256([]byte(token))
	return hex.EncodeToString(data[:])
}

func (c *Companion) ConsoleRoutes(router *http.ServeMux, auth *Auth, origin string) {
	router.Handle("POST /v1/companion/pairing", auth.Require(withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
		code := wire.NewID()
		expiry := time.Now().Add(5 * time.Minute)
		if err := c.Store.PairCompanion(r.Context(), actor, hashToken(code), expiry); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 201, map[string]any{"code": code, "origin": c.Origin, "expires_at": expiry})
	}), origin))
	router.Handle("DELETE /v1/companion/{id}", auth.Require(withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
		id := r.PathValue("id")
		if err := c.Store.RevokeCompanion(r.Context(), actor, id); err != nil {
			writeError(w, err)
			return
		}
		c.Stop(store.Target{Workspace: actor.Workspace, Device: id})
		writeJSON(w, 200, map[string]bool{"revoked": true})
	}), origin))
}

func (c *Companion) PhoneRoutes(router *http.ServeMux) {
	router.HandleFunc("POST /companion/enroll", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code  string `json:"code"`
			Label string `json:"label"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		if len(req.Code) != 32 || len(req.Label) < 1 || len(req.Label) > 80 {
			writeError(w, wire.Error("INVALID_PAIRING", "Provide a valid pairing code and device name.", 400))
			return
		}
		token := wire.NewID() + wire.NewID()
		phone, err := c.Store.EnrollCompanion(r.Context(), hashToken(req.Code), hashToken(token), req.Label, time.Now())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 201, map[string]string{"token": token, "device_id": phone.ID})
	})
	router.HandleFunc("POST /companion/heartbeat", c.phone(func(w http.ResponseWriter, r *http.Request, phone store.Companion) {
		var req struct {
			Present string `json:"present_session"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		actor := ownerActor(phone)
		ctx := r.Context()
		now := time.Now()
		caps := wire.Capabilities{wire.Screen: true, wire.Camera: true, wire.Microphone: true}
		id := phoneConnector(phone.ID)
		if err := c.Store.Register(ctx, phone.Workspace, id, []wire.Inventory{{ID: phone.ID, Label: phone.Label, Platform: "android", Mode: "companion", Capabilities: caps}}, now); err != nil {
			writeError(w, err)
			return
		}
		if req.Present != "" {
			if _, err := c.phoneSession(ctx, phone, req.Present); err == nil {
				_, _, _ = c.Store.ChangeSession(ctx, actor, req.Present, "presence", now)
			}
		}
		sessions, err := c.Store.Sessions(ctx, actor)
		if err != nil {
			writeError(w, err)
			return
		}
		visible := []wire.Session{}
		for _, session := range sessions {
			if session.DeviceID == phone.ID {
				visible = append(visible, session)
			}
		}
		requests := []wire.SensorStatus{}
		c.mu.Lock()
		c.sweepLocked(now)
		for _, entry := range c.requests {
			if entry.workspace == phone.Workspace && entry.device == phone.ID && entry.status.State == wire.Pending {
				requests = append(requests, entry.status)
			}
		}
		c.mu.Unlock()
		writeJSON(w, 200, map[string]any{"sessions": visible, "sensors": requests})
	}))
	router.HandleFunc("POST /companion/unpair", c.phone(func(w http.ResponseWriter, r *http.Request, phone store.Companion) {
		if err := c.Store.RevokeCompanion(r.Context(), ownerActor(phone), phone.ID); err != nil {
			writeError(w, err)
			return
		}
		c.Stop(store.Target{Workspace: phone.Workspace, Device: phone.ID})
		writeJSON(w, 200, map[string]bool{"revoked": true})
	}))
	router.HandleFunc("POST /companion/sessions/{id}/{verb}", c.phone(func(w http.ResponseWriter, r *http.Request, phone store.Companion) {
		verb := r.PathValue("verb")
		if verb != "approve" && verb != "deny" && verb != "end" {
			writeError(w, wire.ErrNotFound)
			return
		}
		id := r.PathValue("id")
		if _, err := c.phoneSession(r.Context(), phone, id); err != nil {
			writeError(w, err)
			return
		}
		session, target, err := c.Store.ChangeSession(r.Context(), ownerActor(phone), id, verb, time.Now())
		if err == nil && verb == "end" {
			c.Stop(target)
		}
		respond(w, 200, session, err)
	}))
	router.HandleFunc("POST /companion/sessions/{id}/frame", c.phone(c.uploadFrame))
	router.HandleFunc("POST /companion/sensors/{id}/deny", c.phone(func(w http.ResponseWriter, r *http.Request, phone store.Companion) {
		entry, err := c.sensorFor(phone, r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		entry.status.State = wire.Denied
		if err := c.saveSensor(entry); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, entry.status)
	}))
	router.HandleFunc("POST /companion/sensors/{id}/artifact", c.phone(c.uploadArtifact))
}

func (c *Companion) PhoneHandler() http.Handler {
	router := http.NewServeMux()
	c.PhoneRoutes(router)
	return perimeter(router, c.Origin, "")
}

func phoneConnector(id string) string { return "companion." + id }

func ownerActor(phone store.Companion) wire.Actor {
	return wire.Actor{Workspace: phone.Workspace, Subject: phone.Owner, Role: wire.Owner}
}

func (c *Companion) phone(handler func(http.ResponseWriter, *http.Request, store.Companion)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || len(token) != 64 {
			writeError(w, errUnauthorized)
			return
		}
		phone, err := c.Store.Companion(r.Context(), hashToken(token))
		if err != nil {
			writeError(w, err)
			return
		}
		handler(w, r, phone)
	}
}

func (c *Companion) phoneSession(ctx context.Context, phone store.Companion, id string) (wire.Session, error) {
	session, err := c.Store.Session(ctx, ownerActor(phone), id)
	if err != nil {
		return session, err
	}
	if session.DeviceID != phone.ID || session.Owner != phone.Owner {
		return wire.Session{}, wire.ErrNotFound
	}
	return session, nil
}

func (c *Companion) uploadFrame(w http.ResponseWriter, r *http.Request, phone store.Companion) {
	ctx := r.Context()
	now := time.Now()
	session, err := c.phoneSession(ctx, phone, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if !containsCap(session.Capabilities, wire.Screen) {
		writeError(w, wire.ErrForbidden)
		return
	}
	target := store.Target{Workspace: phone.Workspace, Connector: phoneConnector(phone.ID), Device: phone.ID, Session: session.ID, Generation: session.Generation}
	if err := c.Store.Confirm(ctx, target, now); err != nil {
		writeError(w, err)
		return
	}
	c.mu.Lock()
	previous := c.frames[phone.ID]
	c.mu.Unlock()
	if previous.session == session.ID && now.Sub(previous.observation.CapturedAt) < 200*time.Millisecond {
		writeError(w, wire.Error("FRAME_RATE_LIMIT", "Send at most five frames per second.", 429))
		return
	}
	defer r.Body.Close()
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 650<<10))
	if err != nil || http.DetectContentType(data) != "image/jpeg" {
		writeError(w, wire.Error("INVALID_FRAME", "Send a JPEG frame of up to 650 KiB.", 400))
		return
	}
	width, height, err := connector.ImageDimensions(data)
	if err != nil || width > 1920 || height > 1920 {
		writeError(w, wire.Error("INVALID_FRAME", "The display frame dimensions are unsupported.", 400))
		return
	}
	if _, _, err := c.Store.ChangeSession(ctx, ownerActor(phone), session.ID, "presence", now); err != nil {
		writeError(w, err)
		return
	}
	orientation := "portrait"
	if width > height {
		orientation = "landscape"
	}
	frame := wire.Observation{CapturedAt: now, FrameID: wire.NewID(), Width: width, Height: height, Orientation: orientation, Image: &wire.Image{MIME: "image/jpeg", Data: data}}
	c.mu.Lock()
	c.sweepLocked(now)
	if len(c.frames) >= 100 && c.frames[phone.ID].session == "" {
		c.mu.Unlock()
		writeError(w, wire.Error("MEDIA_LIMIT", "The server is at its screen-sharing capacity.", 429))
		return
	}
	c.frames[phone.ID] = phoneFrame{frame, session.ID, session.Generation}
	c.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func containsCap(caps []wire.Capability, expected wire.Capability) bool {
	for _, cap := range caps {
		if cap == expected {
			return true
		}
	}
	return false
}

func (c *Companion) sensorFor(phone store.Companion, id string) (sensorEntry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked(time.Now())
	entry, ok := c.requests[phone.Workspace+"/"+id]
	if !ok || entry.workspace != phone.Workspace || entry.device != phone.ID || entry.status.State != wire.Pending {
		return sensorEntry{}, wire.ErrNotFound
	}
	return entry, nil
}

func (c *Companion) saveSensor(entry sensorEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked(time.Now())
	key := entry.workspace + "/" + entry.status.ID
	current, ok := c.requests[key]
	if !ok || current.status.State != wire.Pending {
		return wire.ErrNotFound
	}
	c.requests[key] = entry
	return nil
}

func (c *Companion) uploadArtifact(w http.ResponseWriter, r *http.Request, phone store.Companion) {
	entry, err := c.sensorFor(phone, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	session, err := c.phoneSession(r.Context(), phone, entry.status.SessionID)
	if err != nil {
		writeError(w, err)
		return
	}
	target := store.Target{Workspace: phone.Workspace, Connector: phoneConnector(phone.ID), Device: phone.ID, Session: session.ID, Generation: session.Generation}
	if err := c.Store.Confirm(r.Context(), target, time.Now()); err != nil {
		writeError(w, err)
		return
	}
	defer r.Body.Close()
	limit := int64(3 << 20)
	if entry.status.Type == wire.Microphone {
		limit = int64(44 + 16000*2*entry.status.DurationSeconds)
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	mime := http.DetectContentType(data)
	validPhoto := entry.status.Type == wire.Camera && mime == "image/jpeg"
	validAudio := entry.status.Type == wire.Microphone && len(data) > 44 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE"
	if err != nil || !validPhoto && !validAudio {
		writeError(w, wire.Error("INVALID_ARTIFACT", "The companion did not return a valid bounded photo or WAV clip.", 400))
		return
	}
	if validAudio {
		mime = "audio/wav"
	}
	entry.status.State = "ready"
	entry.status.ExpiresAt = time.Now().Add(artifactLifetime)
	entry.artifact = &wire.Image{MIME: mime, Data: data}
	if err := c.saveSensor(entry); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, entry.status)
}

func (c *Companion) Dispatch(_ context.Context, target store.Target, command wire.Dispatch) (wire.Result, error) {
	result := wire.Result{ID: command.Operation.ID, State: wire.Succeeded}
	now := time.Now()
	op := command.Operation
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked(now)
	switch op.Kind {
	case "observe":
		frame := c.frames[target.Device]
		if frame.session != target.Session || frame.generation != target.Generation {
			return wire.Result{}, wire.Error("SHARING_INACTIVE", "The owner must start screen sharing on the phone.", 409)
		}
		result.Observation = &frame.observation
	case "sensor_request":
		pending := 0
		for _, entry := range c.requests {
			if entry.device == target.Device && entry.status.State == wire.Pending {
				pending++
			}
		}
		if pending >= 2 || len(c.requests) >= 100 {
			return wire.Result{}, wire.Error("SENSOR_QUEUE_FULL", "The companion sensor queue is full.", 429)
		}
		duration := op.Sensor.DurationSeconds
		if op.Sensor.Type == wire.Microphone && duration == 0 {
			duration = 5
		}
		status := wire.SensorStatus{ID: op.ID, Type: op.Sensor.Type, SessionID: target.Session, State: wire.Pending, DurationSeconds: duration, ExpiresAt: now.Add(sensorLifetime)}
		c.requests[target.Workspace+"/"+op.ID] = sensorEntry{status: status, workspace: target.Workspace, device: target.Device}
		result.Sensor = &status
	case "sensor_status", "artifact_get":
		entry, ok := c.requests[target.Workspace+"/"+op.Sensor.RequestID]
		if !ok || entry.workspace != target.Workspace || entry.device != target.Device || entry.status.SessionID != target.Session || entry.status.Type != op.Sensor.Type {
			return wire.Result{}, wire.ErrNotFound
		}
		result.Sensor = &entry.status
		if op.Kind == "artifact_get" {
			if entry.artifact == nil {
				return wire.Result{}, wire.Error("ARTIFACT_UNAVAILABLE", "The approved artifact is not ready.", 409)
			}
			result.Artifact = entry.artifact
		}
	default:
		return wire.Result{}, wire.ErrUnsupported
	}
	return result, nil
}

func (c *Companion) Stop(target store.Target) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if frame := c.frames[target.Device]; target.Session == "" || frame.session == target.Session {
		delete(c.frames, target.Device)
	}
	for id, entry := range c.requests {
		if entry.workspace == target.Workspace && entry.device == target.Device && (entry.status.SessionID == target.Session || target.Session == "") {
			delete(c.requests, id)
		}
	}
}

func (c *Companion) sweepLocked(now time.Time) {
	for id, frame := range c.frames {
		if now.Sub(frame.observation.CapturedAt) > 3*time.Second {
			delete(c.frames, id)
		}
	}
	for id, entry := range c.requests {
		if !now.Before(entry.status.ExpiresAt) {
			delete(c.requests, id)
		}
	}
}

func (c *Companion) Maintain(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			c.mu.Lock()
			c.sweepLocked(now)
			c.mu.Unlock()
		}
	}
}
