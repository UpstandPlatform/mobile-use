package connector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"sync"
	"time"

	"mobile-use/internal/wire"
)

type Found struct {
	NativeID string
	wire.Inventory
}

type Backend interface {
	Discover(context.Context) ([]Found, error)
	Open(context.Context, Found) (Driver, error)
}

type Display struct {
	Width, Height      int
	Orientation, AppID string
	Locked             bool
}

type Driver interface {
	Display(context.Context) (Display, error)
	Screenshot(context.Context) (*wire.Image, error)
	Tree(context.Context) ([]wire.Node, error)
	Action(context.Context, wire.Action, Display, *wire.Rect) error
	Apps(context.Context) ([]string, error)
	App(context.Context, string, bool) error
	Browser(context.Context, wire.Operation) (wire.Result, error)
	Close(context.Context)
}

type MaintenanceDriver interface {
	Maintain(context.Context, string, string) error
}

type Manager struct {
	id       string
	backends []Backend
	mu       sync.Mutex
	devices  map[string]*worker
}

type worker struct {
	mu      sync.Mutex
	backend Backend
	found   Found
	online  bool
	busy    bool
	version int64
	session string
	revoked bool
	driver  Driver
	cancel  context.CancelFunc
	frames  map[string]frame
}

type frame struct {
	at      time.Time
	display Display
	nodes   map[string]wire.Rect
}

func New(workspace, id string, backends ...Backend) *Manager {
	return &Manager{id: workspace + "/" + id, backends: backends, devices: make(map[string]*worker)}
}

func (m *Manager) Inventory(ctx context.Context) []wire.Inventory {
	type detected struct {
		found   Found
		backend Backend
	}
	devices := make(map[string]detected)
	for _, backend := range m.backends {
		found, err := backend.Discover(ctx)
		if err != nil {
			slog.Warn("Device discovery failed", "error", err)
			continue
		}
		for _, device := range found {
			hash := sha256.Sum256([]byte(m.id + "/" + device.Platform + "/" + device.NativeID))
			device.ID = hex.EncodeToString(hash[:16])
			devices[device.ID] = detected{device, backend}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, node := range m.devices {
		if _, ok := devices[id]; ok {
			continue
		}
		node.mu.Lock()
		node.online = false
		node.revokeLocked()
		node.mu.Unlock()
	}
	inventory := make([]wire.Inventory, 0, len(devices))
	for id, item := range devices {
		node := m.devices[id]
		if node == nil {
			node = &worker{backend: item.backend, found: item.found}
			m.devices[id] = node
		}
		node.mu.Lock()
		node.online = true
		node.found = item.found
		node.mu.Unlock()
		inventory = append(inventory, item.found.Inventory)
	}
	return inventory
}

func (m *Manager) get(id string) *worker {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.devices[id]
}

func (m *Manager) Stop(device, session string, generation int64) {
	node := m.get(device)
	if node == nil {
		return
	}
	node.mu.Lock()
	defer node.mu.Unlock()
	if generation < node.version {
		return
	}
	node.version, node.session = generation, session
	node.revokeLocked()
}

func (m *Manager) Disconnect() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, node := range m.devices {
		node.mu.Lock()
		node.revokeLocked()
		node.mu.Unlock()
	}
}

func (w *worker) revokeLocked() {
	w.revoked = true
	w.frames = nil
	if w.cancel != nil {
		w.cancel()
	}
	if !w.busy && w.driver != nil {
		closeDriver(w.driver)
		w.driver = nil
	}
}

func closeDriver(driver Driver) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		driver.Close(ctx)
	}()
}

func (m *Manager) Execute(ctx context.Context, command wire.Dispatch) wire.Result {
	result := wire.Result{ID: command.Operation.ID, State: wire.Failed}
	node := m.get(command.DeviceID)
	if node == nil {
		result.Fault = wire.ErrOffline
		return result
	}
	if _, err := command.Operation.Required(); err != nil {
		result.Fault = wire.Public(err)
		return result
	}
	// Sessions start at generation 1. A generation-0 command would match a fresh worker's zero
	// state without initializing its frame map, so observe would write to a nil map.
	if command.Generation < 1 || command.SessionID == "" {
		result.Fault = wire.ErrExpired
		return result
	}
	now := time.Now()
	maxLifetime := 11 * time.Second
	if command.Operation.IsAppMaintenance() && !command.Personal {
		maxLifetime = wire.AppMaintenanceTimeout + time.Second
	}
	if !now.Before(command.Deadline) || command.Deadline.After(now.Add(maxLifetime)) {
		result.Fault = wire.ErrExpired
		return result
	}
	node.mu.Lock()
	if !node.online {
		node.mu.Unlock()
		result.Fault = wire.ErrOffline
		return result
	}
	if command.Generation < node.version || command.Generation == node.version && (node.revoked || command.SessionID != node.session) {
		node.mu.Unlock()
		result.Fault = wire.ErrExpired
		return result
	}
	if node.busy {
		node.mu.Unlock()
		result.Fault = wire.Error("OPERATION_IN_PROGRESS", "Wait for the current device operation to finish.", 409)
		return result
	}
	if command.Generation > node.version {
		node.frames = make(map[string]frame)
		node.version, node.session, node.revoked = command.Generation, command.SessionID, false
	}
	ctx, cancel := context.WithDeadline(ctx, command.Deadline)
	node.cancel, node.busy = cancel, true
	driver, found := node.driver, node.found
	node.mu.Unlock()
	defer func() {
		cancel()
		node.mu.Lock()
		defer node.mu.Unlock()
		node.busy, node.cancel = false, nil
		if node.revoked && node.driver != nil {
			closeDriver(node.driver)
			node.driver = nil
		}
	}()
	if driver == nil {
		var err error
		driver, err = node.backend.Open(ctx, found)
		if err != nil {
			result.Fault = wire.Public(err)
			return result
		}
		node.mu.Lock()
		node.driver = driver
		node.mu.Unlock()
	}
	if ctx.Err() != nil {
		result.Fault = wire.ErrExpired
		return result
	}
	outcome, err := node.run(ctx, driver, command)
	if err == nil && ctx.Err() == nil {
		outcome.ID, outcome.State = command.Operation.ID, wire.Succeeded
		return outcome
	}
	if err == nil || errors.Is(err, wire.ErrUnknown) || ctx.Err() != nil && mutating(command.Operation.Kind) {
		result.State, result.Fault = wire.Unknown, wire.ErrUnknown
		return result
	}
	if ctx.Err() != nil {
		result.Fault = wire.Error("DEADLINE_EXCEEDED", "The device did not respond before the deadline.", 504)
		return result
	}
	result.Fault = wire.Public(err)
	if result.Fault.Code == "INTERNAL" {
		slog.Warn("Device operation failed", "kind", command.Operation.Kind, "error", err)
	}
	return result
}

func mutating(kind string) bool {
	return kind == "act" || kind == "app_launch" || kind == "app_terminate" || kind == "browser_act" || kind == "app_install" || kind == "app_reset"
}

func (w *worker) run(ctx context.Context, driver Driver, command wire.Dispatch) (wire.Result, error) {
	op := command.Operation
	switch op.Kind {
	case "observe":
		observation, err := w.observe(ctx, driver, *op.Observe)
		return wire.Result{Observation: observation}, err
	case "act":
		return wire.Result{}, w.action(ctx, driver, *op.Action)
	case "apps_list":
		if _, err := unlocked(ctx, driver); err != nil {
			return wire.Result{}, err
		}
		apps, err := driver.Apps(ctx)
		return wire.Result{Apps: apps}, err
	case "app_launch", "app_terminate":
		if _, err := unlocked(ctx, driver); err != nil {
			return wire.Result{}, err
		}
		w.clearFrames()
		return wire.Result{}, driver.App(ctx, op.AppID, op.Kind == "app_launch")
	case "app_install", "app_reset":
		if _, err := unlocked(ctx, driver); err != nil {
			return wire.Result{}, err
		}
		maintenance, ok := driver.(MaintenanceDriver)
		if command.Personal || !ok {
			return wire.Result{}, wire.ErrUnsupported
		}
		w.clearFrames()
		return wire.Result{}, maintenance.Maintain(ctx, op.Kind, op.Maintenance.ArtifactID)
	case "browser_contexts", "browser_observe", "browser_act":
		if _, err := unlocked(ctx, driver); err != nil {
			return wire.Result{}, err
		}
		if op.Kind == "browser_act" {
			w.clearFrames()
		}
		return driver.Browser(ctx, op)
	default:
		return wire.Result{}, wire.ErrUnsupported
	}
}

func unlocked(ctx context.Context, driver Driver) (Display, error) {
	display, err := driver.Display(ctx)
	if err != nil {
		return display, err
	}
	if display.Locked {
		return display, wire.ErrLocked
	}
	return display, nil
}

func (w *worker) observe(ctx context.Context, driver Driver, request wire.ObserveRequest) (*wire.Observation, error) {
	if request.WaitFor != "" {
		if err := waitFor(ctx, driver, request); err != nil {
			return nil, err
		}
	}
	display, err := unlocked(ctx, driver)
	if err != nil {
		return nil, err
	}
	at := time.Now()
	obs := &wire.Observation{FrameID: wire.NewID(), CapturedAt: at, Width: display.Width, Height: display.Height, Orientation: display.Orientation, AppID: display.AppID}
	nodes := make(map[string]wire.Rect)
	if request.Tree {
		obs.Nodes, err = driver.Tree(ctx)
		if err != nil {
			obs.Partial = append(obs.Partial, "UI hierarchy unavailable")
		}
		for _, node := range obs.Nodes {
			if node.Enabled && node.Bounds.Width > 0 && node.Bounds.Height > 0 {
				nodes[node.ID] = node.Bounds
			}
		}
	}
	if request.Screen {
		shot, err := driver.Screenshot(ctx)
		if err != nil || shot == nil || len(shot.Data) > 8<<20 {
			obs.Partial = append(obs.Partial, "Screenshot unavailable")
		} else {
			obs.Image = shot
		}
	}
	if len(obs.Partial) > 0 && obs.Image == nil && len(obs.Nodes) == 0 {
		return nil, wire.ErrUnsupported
	}
	current, err := unlocked(ctx, driver)
	if err != nil {
		return nil, err
	}
	if current != display {
		return nil, wire.ErrStale
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.revoked {
		return nil, wire.ErrExpired
	}
	for id, old := range w.frames {
		if at.Sub(old.at) > 15*time.Second || len(w.frames) >= 8 {
			delete(w.frames, id)
		}
	}
	w.frames[obs.FrameID] = frame{at: at, display: display, nodes: nodes}
	return obs, nil
}

func waitFor(ctx context.Context, driver Driver, request wire.ObserveRequest) error {
	timeout := time.Duration(request.TimeoutMS) * time.Millisecond
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		if _, err := unlocked(ctx, driver); err != nil {
			return err
		}
		nodes, err := driver.Tree(ctx)
		if err == nil {
			for _, node := range nodes {
				if node.Label == request.WaitFor || node.ResourceID == request.WaitFor {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return wire.Error("CONDITION_TIMEOUT", "The element did not appear before the deadline.", 408)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (w *worker) clearFrames() {
	w.mu.Lock()
	defer w.mu.Unlock()
	clear(w.frames)
}

func (w *worker) action(ctx context.Context, driver Driver, action wire.Action) error {
	w.mu.Lock()
	old, ok := w.frames[action.FrameID]
	w.mu.Unlock()
	if !ok || time.Since(old.at) > 15*time.Second {
		return wire.ErrStale
	}
	display, err := unlocked(ctx, driver)
	if err != nil {
		return err
	}
	if display != old.display {
		return wire.ErrStale
	}
	var bounds *wire.Rect
	if action.NodeID != "" {
		rect, ok := old.nodes[action.NodeID]
		if !ok {
			return wire.ErrStale
		}
		// Tree IDs are stable only for equivalent snapshots. Recheck the target before dispatch.
		nodes, err := driver.Tree(ctx)
		if err != nil {
			return wire.ErrStale
		}
		for _, node := range nodes {
			if node.ID == action.NodeID && node.Bounds == rect && node.Enabled {
				bounds = &rect
				break
			}
		}
		if bounds == nil {
			return wire.ErrStale
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	w.clearFrames()
	return driver.Action(ctx, action, display, bounds)
}

func Pixel(point wire.Point, display Display) (int, int) {
	return int(point.X * float64(display.Width)), int(point.Y * float64(display.Height))
}

func ImageDimensions(data []byte) (int, int, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 10000 || config.Height > 10000 {
		return 0, 0, fmt.Errorf("invalid screenshot dimensions")
	}
	return config.Width, config.Height, nil
}
