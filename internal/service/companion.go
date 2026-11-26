package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"mobile-use/internal/connector"
	"mobile-use/internal/store"
	"mobile-use/internal/wire"
)

const sensorLifetime = 2 * time.Minute
const artifactLifetime = 5 * time.Minute
const pairingAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

type CompanionEndpoint struct {
	Origin    string `json:"origin"`
	Interface string `json:"interface,omitempty"`
	Primary   bool   `json:"primary,omitempty"`
}

type Companion struct {
	Store           *store.Store
	Origin          string
	Origins         []CompanionEndpoint
	AllowedOrigins  []string
	CertFingerprint string
	Mode            string
	HostName        string

	mu        sync.Mutex
	frames    map[string]phoneFrame
	frameSubs map[string]map[chan frameEvent]struct{}
	requests  map[string]sensorEntry
	pairings  map[string]*pairingRecord
	byCode    map[string]string
	attempts  map[string][]time.Time
}

type pairingRecord struct {
	ID           string
	Workspace    string
	Owner        string
	Creator      wire.Actor
	CodeHash     string
	ExpiresAt    time.Time
	AutoEnroll   bool
	GrantAgents  []string
	State        string
	DeviceID     string
	Label        string
	AutoEnrolled bool
}

type phoneFrame struct {
	observation wire.Observation
	session     string
	generation  int64
}

type frameEvent struct {
	kind       string
	data       []byte
	session    string
	generation int64
}
type sensorEntry struct {
	status            wire.SensorStatus
	workspace, device string
	artifact          *wire.Image
}

func NewCompanion(db *store.Store, origin string) *Companion {
	return &Companion{
		Store:          db,
		Origin:         origin,
		Origins:        []CompanionEndpoint{{Origin: origin, Primary: true}},
		AllowedOrigins: []string{origin},
		Mode:           "remote",
		frames:         make(map[string]phoneFrame),
		frameSubs:      make(map[string]map[chan frameEvent]struct{}),
		requests:       make(map[string]sensorEntry),
		pairings:       make(map[string]*pairingRecord),
		byCode:         make(map[string]string),
		attempts:       make(map[string][]time.Time),
	}
}

func hashToken(token string) string {
	data := sha256.Sum256([]byte(token))
	return hex.EncodeToString(data[:])
}

func generatePairingCode() string {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	var out [8]byte
	for i := range out {
		out[i] = pairingAlphabet[int(raw[i])&31]
	}
	return string(out[:4]) + "-" + string(out[4:])
}

// NormalizePairingCode validates and canonicalizes an 8-character pairing code or legacy 32-char hex code.
func NormalizePairingCode(raw string) (string, bool) {
	clean := strings.TrimSpace(raw)
	clean = strings.ReplaceAll(clean, "-", "")
	clean = strings.ReplaceAll(clean, " ", "")
	if len(clean) == 8 {
		upper := strings.ToUpper(clean)
		for i := 0; i < len(upper); i++ {
			if !strings.ContainsRune(pairingAlphabet, rune(upper[i])) {
				return "", false
			}
		}
		return upper, true
	}
	if len(clean) == 32 {
		lower := strings.ToLower(clean)
		for i := 0; i < len(lower); i++ {
			c := lower[i]
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return "", false
			}
		}
		return lower, true
	}
	return "", false
}

// BuildPairingURI encodes a mobile-use://pair QR/deep-link payload.
func BuildPairingURI(origin, code, fingerprint, hostName, mode string) string {
	norm, ok := NormalizePairingCode(code)
	if !ok {
		norm = strings.TrimSpace(code)
	}
	q := url.Values{}
	q.Set("o", origin)
	q.Set("c", norm)
	if hostName != "" {
		q.Set("n", hostName)
	}
	if mode != "" {
		q.Set("m", mode)
	}
	if fingerprint != "" {
		q.Set("p", strings.ToLower(fingerprint))
	}
	return "mobile-use://pair?" + q.Encode()
}

func (c *Companion) selectOrigin(requested string) (string, bool) {
	if strings.TrimSpace(requested) == "" {
		return c.Origin, true
	}
	trimmed := strings.TrimRight(strings.TrimSpace(requested), "/")
	for _, allowed := range c.AllowedOrigins {
		if strings.EqualFold(trimmed, allowed) {
			return allowed, true
		}
	}
	for _, ep := range c.Origins {
		if strings.EqualFold(trimmed, ep.Origin) {
			return ep.Origin, true
		}
	}
	if strings.EqualFold(trimmed, c.Origin) {
		return c.Origin, true
	}
	return "", false
}

func (c *Companion) ConsoleRoutes(router *http.ServeMux, auth *Auth, origin string) {
	router.Handle("GET /v1/companion/sessions/{id}/frames", auth.Require(withActor(c.streamFrames), origin))
	router.Handle("POST /v1/companion/pairing", auth.Require(withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
		var req struct {
			Origin      string   `json:"origin"`
			AutoEnroll  bool     `json:"auto_enroll"`
			GrantAgents []string `json:"grant_agents"`
		}
		if r.Body != nil {
			defer r.Body.Close()
			raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
			if err != nil {
				writeError(w, wire.Error("INVALID_JSON", "Send one valid JSON object for this operation.", 400))
				return
			}
			if len(bytes.TrimSpace(raw)) > 0 {
				dec := json.NewDecoder(bytes.NewReader(raw))
				dec.DisallowUnknownFields()
				if err := dec.Decode(&req); err != nil || dec.Decode(new(json.RawMessage)) != io.EOF {
					writeError(w, wire.Error("INVALID_JSON", "Send one valid JSON object for this operation.", 400))
					return
				}
			}
		}
		selectedOrigin, ok := c.selectOrigin(req.Origin)
		if !ok {
			writeError(w, wire.Error("INVALID_ORIGIN", "The requested pairing origin is not active on this server.", 400))
			return
		}
		for _, subject := range req.GrantAgents {
			if !wire.ValidID(subject) {
				writeError(w, wire.Error("INVALID_GRANT", "Each grant agent identity must be valid.", 400))
				return
			}
		}
		formattedCode := generatePairingCode()
		normalizedCode, _ := NormalizePairingCode(formattedCode)
		codeHash := hashToken(normalizedCode)
		now := time.Now()
		expiry := now.Add(5 * time.Minute)
		if err := c.Store.PairCompanion(r.Context(), actor, codeHash, expiry); err != nil {
			writeError(w, err)
			return
		}
		id := wire.NewID()
		c.mu.Lock()
		c.ensureMapsLocked()
		c.sweepLocked(now)
		c.pairings[id] = &pairingRecord{
			ID:          id,
			Workspace:   actor.Workspace,
			Owner:       actor.Subject,
			Creator:     actor,
			CodeHash:    codeHash,
			ExpiresAt:   expiry,
			AutoEnroll:  req.AutoEnroll,
			GrantAgents: req.GrantAgents,
			State:       "waiting",
		}
		c.byCode[codeHash] = id
		c.mu.Unlock()

		mode := c.Mode
		if mode == "" {
			mode = "remote"
		}
		origins := c.Origins
		if len(origins) == 0 && c.Origin != "" {
			origins = []CompanionEndpoint{{Origin: c.Origin, Primary: true}}
		}
		writeJSON(w, 201, map[string]any{
			"id":          id,
			"code":        formattedCode,
			"origin":      selectedOrigin,
			"origins":     origins,
			"fingerprint": c.CertFingerprint,
			"mode":        mode,
			"host_name":   c.HostName,
			"qr_uri":      BuildPairingURI(selectedOrigin, formattedCode, c.CertFingerprint, c.HostName, mode),
			"expires_at":  expiry,
			"auto_enroll": req.AutoEnroll && actor.CanManage(),
		})
	}), origin))
	router.Handle("GET /v1/companion/pairing/{id}", auth.Require(withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
		id := r.PathValue("id")
		now := time.Now()
		c.mu.Lock()
		c.ensureMapsLocked()
		c.sweepLocked(now)
		record, ok := c.pairings[id]
		if !ok || record.Workspace != actor.Workspace || (!actor.CanManage() && actor.Subject != record.Owner) {
			c.mu.Unlock()
			writeError(w, wire.ErrNotFound)
			return
		}
		state := record.State
		if state == "waiting" && !now.Before(record.ExpiresAt) {
			state = "expired"
		}
		resp := map[string]any{
			"id":            record.ID,
			"state":         state,
			"device_id":     record.DeviceID,
			"label":         record.Label,
			"auto_enrolled": record.AutoEnrolled,
			"expires_at":    record.ExpiresAt,
		}
		c.mu.Unlock()
		writeJSON(w, 200, resp)
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

func (c *Companion) allowEnrollAttempt(remoteAddr string, now time.Time) bool {
	ip := remoteAddr
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		ip = host
	}
	cutoff := now.Add(-time.Minute)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureMapsLocked()
	total := 0
	for k, list := range c.attempts {
		kept := list[:0]
		for _, ts := range list {
			if ts.After(cutoff) {
				kept = append(kept, ts)
			}
		}
		if len(kept) == 0 {
			delete(c.attempts, k)
		} else {
			c.attempts[k] = kept
			total += len(kept)
		}
	}
	if total >= 60 || len(c.attempts[ip]) >= 15 || (len(c.attempts) >= 1000 && len(c.attempts[ip]) == 0) {
		return false
	}
	c.attempts[ip] = append(c.attempts[ip], now)
	return true
}

func (c *Companion) PhoneRoutes(router *http.ServeMux) {
	router.HandleFunc("POST /companion/enroll", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		if !c.allowEnrollAttempt(r.RemoteAddr, now) {
			writeError(w, wire.Error("RATE_LIMITED", "Too many pairing attempts. Wait a minute and try again.", 429))
			return
		}
		var req struct {
			Code  string `json:"code"`
			Label string `json:"label"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		normalizedCode, ok := NormalizePairingCode(req.Code)
		if !ok || len(req.Label) < 1 || len(req.Label) > 80 {
			writeError(w, wire.Error("INVALID_PAIRING", "Provide a valid pairing code and device name.", 400))
			return
		}
		codeHash := hashToken(normalizedCode)
		token := wire.NewID() + wire.NewID()
		phone, err := c.Store.EnrollCompanion(r.Context(), codeHash, hashToken(token), req.Label, now)
		if err != nil {
			writeError(w, err)
			return
		}
		caps := wire.Capabilities{wire.Screen: true, wire.Camera: true, wire.Microphone: true}
		_ = c.Store.Register(r.Context(), phone.Workspace, phoneConnector(phone.ID), []wire.Inventory{{
			ID:           phone.ID,
			Label:        phone.Label,
			Platform:     "android",
			Mode:         "companion",
			Capabilities: caps,
		}}, now)

		var autoEnroll bool
		var creator wire.Actor
		var grantAgents []string
		var recordID string
		c.mu.Lock()
		c.ensureMapsLocked()
		if id, exists := c.byCode[codeHash]; exists {
			recordID = id
			if rec := c.pairings[id]; rec != nil {
				autoEnroll = rec.AutoEnroll
				creator = rec.Creator
				grantAgents = append([]string(nil), rec.GrantAgents...)
				rec.State = "paired"
				rec.DeviceID = phone.ID
				rec.Label = phone.Label
			}
			delete(c.byCode, codeHash)
		}
		c.mu.Unlock()

		autoEnrolled := false
		if autoEnroll && creator.CanManage() {
			subjects := grantAgents
			if len(subjects) == 0 && phone.Workspace == "local" {
				subjects = []string{"local-agent"}
			}
			var grants []wire.Grant
			for _, subj := range subjects {
				if wire.ValidID(subj) {
					grants = append(grants, wire.Grant{
						Subject:      subj,
						Capabilities: []wire.Capability{wire.Screen, wire.Camera, wire.Microphone},
					})
				}
			}
			enabled := true
			if _, _, err := c.Store.Enroll(r.Context(), creator, phone.ID, wire.Enrollment{
				Label:   phone.Label,
				Owner:   phone.Owner,
				Profile: wire.Personal,
				Grants:  grants,
				Enabled: &enabled,
			}, now); err == nil {
				autoEnrolled = true
			}
		}
		if recordID != "" && autoEnrolled {
			c.mu.Lock()
			if rec := c.pairings[recordID]; rec != nil {
				rec.AutoEnrolled = true
			}
			c.mu.Unlock()
		}
		resp := map[string]any{"token": token, "device_id": phone.ID}
		if autoEnrolled {
			resp["auto_enrolled"] = true
		}
		writeJSON(w, 201, resp)
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
	origins := c.AllowedOrigins
	if len(origins) == 0 && c.Origin != "" {
		origins = []string{c.Origin}
	}
	return perimeterHosts(router, origins, "")
}

func (c *Companion) ensureMapsLocked() {
	if c.frames == nil {
		c.frames = make(map[string]phoneFrame)
	}
	if c.frameSubs == nil {
		c.frameSubs = make(map[string]map[chan frameEvent]struct{})
	}
	if c.requests == nil {
		c.requests = make(map[string]sensorEntry)
	}
	if c.pairings == nil {
		c.pairings = make(map[string]*pairingRecord)
	}
	if c.byCode == nil {
		c.byCode = make(map[string]string)
	}
	if c.attempts == nil {
		c.attempts = make(map[string][]time.Time)
	}
}

func (c *Companion) streamFrames(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
	session, err := c.Store.Session(r.Context(), actor, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	canObserve := actor.CanManage() || actor.Role == wire.Owner && actor.Subject == session.Owner || actor.Role == wire.Agent && actor.Subject == session.Controller
	if !canObserve || session.State != wire.Active || !containsCap(session.Capabilities, wire.Screen) {
		writeError(w, wire.ErrForbidden)
		return
	}
	devices, err := c.Store.Devices(r.Context(), actor, time.Now())
	if err != nil {
		writeError(w, err)
		return
	}
	companion := false
	for _, device := range devices {
		if device.ID == session.DeviceID && device.Mode == "companion" {
			companion = true
			break
		}
	}
	if !companion {
		writeError(w, wire.ErrNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, wire.Error("STREAM_UNAVAILABLE", "This server cannot stream screen updates.", 500))
		return
	}
	channel, initial := c.subscribeFrames(session.DeviceID)
	defer c.unsubscribeFrames(session.DeviceID, channel)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	if initial != nil && initial.session == session.ID && initial.generation == session.Generation && time.Since(initial.observation.CapturedAt) < 3*time.Second {
		if data, marshalErr := json.Marshal(initial.observation); marshalErr == nil {
			writeFrameEvent(w, flusher, frameEvent{kind: "frame", data: data})
		}
	} else {
		writeFrameEvent(w, flusher, frameEvent{kind: "status", data: []byte(`{"state":"waiting"}`)})
	}
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-channel:
			if event.kind == "frame" && (event.session != session.ID || event.generation != session.Generation) {
				continue
			}
			if !writeFrameEvent(w, flusher, event) {
				return
			}
		case <-keepalive.C:
			setStreamWriteDeadline(w)
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeFrameEvent(w http.ResponseWriter, flusher http.Flusher, event frameEvent) bool {
	setStreamWriteDeadline(w)
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.kind, event.data); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

func setStreamWriteDeadline(w http.ResponseWriter) {
	// The server has a short write timeout for ordinary requests. Renew it on each
	// stream write so an active screen feed can remain connected indefinitely.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second))
}

func (c *Companion) subscribeFrames(device string) (chan frameEvent, *phoneFrame) {
	channel := make(chan frameEvent, 1)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureMapsLocked()
	if c.frameSubs[device] == nil {
		c.frameSubs[device] = make(map[chan frameEvent]struct{})
	}
	c.frameSubs[device][channel] = struct{}{}
	if frame, ok := c.frames[device]; ok {
		return channel, &frame
	}
	return channel, nil
}

func (c *Companion) unsubscribeFrames(device string, channel chan frameEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.frameSubs[device], channel)
	if len(c.frameSubs[device]) == 0 {
		delete(c.frameSubs, device)
	}
}

func (c *Companion) publishFrameLocked(device string, frame phoneFrame) {
	if len(c.frameSubs[device]) == 0 {
		return
	}
	data, err := json.Marshal(frame.observation)
	if err != nil {
		return
	}
	c.publishEventLocked(device, frameEvent{kind: "frame", data: data, session: frame.session, generation: frame.generation})
}

func (c *Companion) publishEventLocked(device string, event frameEvent) {
	for channel := range c.frameSubs[device] {
		select {
		case channel <- event:
		default:
			select {
			case <-channel:
			default:
			}
			select {
			case channel <- event:
			default:
			}
		}
	}
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
	c.publishFrameLocked(phone.ID, c.frames[phone.ID])
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
			return wire.Result{}, wire.Error("SHARING_INACTIVE", "Start screen sharing in the companion app and approve Android’s screen-capture prompt.", 409)
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
		c.publishEventLocked(target.Device, frameEvent{kind: "status", data: []byte(`{"state":"waiting"}`)})
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
			c.publishEventLocked(id, frameEvent{kind: "status", data: []byte(`{"state":"waiting"}`)})
		}
	}
	for id, entry := range c.requests {
		if !now.Before(entry.status.ExpiresAt) {
			delete(c.requests, id)
		}
	}
	retention := now.Add(-15 * time.Minute)
	for id, rec := range c.pairings {
		if rec.State == "waiting" && !now.Before(rec.ExpiresAt) {
			rec.State = "expired"
			delete(c.byCode, rec.CodeHash)
		}
		if rec.ExpiresAt.Before(retention) {
			delete(c.byCode, rec.CodeHash)
			delete(c.pairings, id)
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
