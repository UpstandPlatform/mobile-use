package service

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"mobile-use/internal/store"
	"mobile-use/internal/wire"
)

const maxPhoneControlRequests = 8

type phoneControlCommand struct {
	Type     string        `json:"type"`
	ID       string        `json:"id"`
	Dispatch wire.Dispatch `json:"dispatch"`
}

type phoneControlResponse struct {
	Type   string      `json:"type"`
	ID     string      `json:"id"`
	Result wire.Result `json:"result"`
}

type phoneControlOutcome struct {
	result wire.Result
	err    error
}

type controlSubscriber struct {
	device, session string
	generation      int64
	commands        chan phoneControlCommand
	done            chan struct{}
	closeOnce       sync.Once
	mu              sync.Mutex
	pending         map[string]chan phoneControlOutcome
}

func (c *Companion) ControlPhoneRoutes(router *http.ServeMux) {
	router.HandleFunc("GET /companion/sessions/{id}/control", c.phone(c.streamPhoneControl))
}

func (c *Companion) streamPhoneControl(w http.ResponseWriter, r *http.Request, phone store.Companion) {
	session, err := c.phoneSession(r.Context(), phone, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	target := store.Target{Workspace: phone.Workspace, Connector: phoneConnector(phone.ID), Device: phone.ID, Session: session.ID, Generation: session.Generation}
	if err := c.Store.Confirm(r.Context(), target, time.Now()); err != nil {
		writeError(w, err)
		return
	}
	if session.State != wire.Active || !hasPhoneControlCapability(session.Capabilities) {
		writeError(w, wire.ErrForbidden)
		return
	}
	conn, err := (&websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 8192}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	subscriber := c.subscribeControl(phone.ID, session.ID, session.Generation)
	defer c.unsubscribeControl(phone.ID, subscriber)

	conn.SetReadLimit(2 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	conn.SetPingHandler(func(data string) error {
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(5*time.Second))
	})
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(30 * time.Second)) })
	type incoming struct {
		kind int
		data []byte
		err  error
	}
	reads := make(chan incoming, 1)
	go func() {
		defer close(reads)
		for {
			kind, data, readErr := conn.ReadMessage()
			select {
			case reads <- incoming{kind: kind, data: data, err: readErr}:
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
		case <-subscriber.done:
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "session ended"), time.Now().Add(time.Second))
			return
		case message, ok := <-reads:
			if !ok || message.err != nil {
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
			if message.kind != websocket.TextMessage {
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "text results required"), time.Now().Add(time.Second))
				return
			}
			var response phoneControlResponse
			if json.Unmarshal(message.data, &response) != nil || response.Type != "result" || !wire.ValidID(response.ID) || response.ID != response.Result.ID {
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "invalid command result"), time.Now().Add(time.Second))
				return
			}
			subscriber.resolve(response.ID, response.Result)
		case command := <-subscriber.commands:
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if conn.WriteJSON(command) != nil {
				return
			}
		case <-keepalive.C:
			if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(3*time.Second)) != nil {
				return
			}
		}
	}
}

func hasPhoneControlCapability(capabilities []wire.Capability) bool {
	for _, capability := range capabilities {
		switch capability {
		case wire.Tree, wire.Touch, wire.Text, wire.Apps, wire.Browser:
			return true
		}
	}
	return false
}

func (c *Companion) subscribeControl(device, session string, generation int64) *controlSubscriber {
	subscriber := &controlSubscriber{
		device: device, session: session, generation: generation,
		commands: make(chan phoneControlCommand, maxPhoneControlRequests),
		done:     make(chan struct{}), pending: make(map[string]chan phoneControlOutcome),
	}
	c.mu.Lock()
	c.ensureMapsLocked()
	previous := c.controlSubs[device]
	c.controlSubs[device] = subscriber
	c.mu.Unlock()
	if previous != nil {
		previous.close(wire.ErrOffline)
	}
	return subscriber
}

func (c *Companion) unsubscribeControl(device string, subscriber *controlSubscriber) {
	c.mu.Lock()
	if c.controlSubs[device] == subscriber {
		delete(c.controlSubs, device)
	}
	c.mu.Unlock()
	subscriber.close(wire.ErrOffline)
}

func (s *controlSubscriber) request(ctx context.Context, dispatch wire.Dispatch) (wire.Result, error) {
	id := dispatch.Operation.ID
	response := make(chan phoneControlOutcome, 1)
	s.mu.Lock()
	if s.isClosedLocked() {
		s.mu.Unlock()
		return wire.Result{}, wire.ErrOffline
	}
	if len(s.pending) >= maxPhoneControlRequests {
		s.mu.Unlock()
		return wire.Result{}, wire.Error("CONTROL_QUEUE_FULL", "The phone control queue is busy. Retry after the current action completes.", http.StatusTooManyRequests)
	}
	if _, exists := s.pending[id]; exists {
		s.mu.Unlock()
		return wire.Result{}, wire.ErrConflict
	}
	s.pending[id] = response
	s.mu.Unlock()
	defer s.remove(id)
	command := phoneControlCommand{Type: "command", ID: id, Dispatch: dispatch}
	select {
	case s.commands <- command:
	case <-s.done:
		return wire.Result{}, wire.ErrOffline
	case <-ctx.Done():
		return wire.Result{}, ctx.Err()
	}
	select {
	case outcome := <-response:
		return outcome.result, outcome.err
	case <-s.done:
		return wire.Result{}, wire.ErrOffline
	case <-ctx.Done():
		return wire.Result{}, ctx.Err()
	}
}

func (s *controlSubscriber) resolve(id string, result wire.Result) {
	s.mu.Lock()
	response := s.pending[id]
	s.mu.Unlock()
	if response == nil {
		return
	}
	select {
	case response <- phoneControlOutcome{result: result}:
	default:
	}
}

func (s *controlSubscriber) remove(id string) {
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
}

func (s *controlSubscriber) close(err error) {
	s.closeOnce.Do(func() {
		close(s.done)
		s.mu.Lock()
		pending := make([]chan phoneControlOutcome, 0, len(s.pending))
		for _, response := range s.pending {
			pending = append(pending, response)
		}
		s.mu.Unlock()
		for _, response := range pending {
			select {
			case response <- phoneControlOutcome{err: err}:
			default:
			}
		}
	})
}

func (s *controlSubscriber) isClosedLocked() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}
