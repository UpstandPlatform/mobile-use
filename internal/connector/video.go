package connector

import (
	"context"
	"mobile-use/internal/wire"
)

type VideoBackend interface {
	Video(context.Context, Found, func(wire.VideoFrame) error) error
}

type VideoDriver interface {
	Video(context.Context, func(wire.VideoFrame) error) error
}

func (m *Manager) Video(ctx context.Context, device, session string, generation int64, publish func(wire.VideoFrame) error) error {
	worker := m.get(device)
	if worker == nil {
		return wire.ErrOffline
	}
	worker.mu.Lock()
	if !worker.online {
		worker.mu.Unlock()
		return wire.ErrOffline
	}
	if generation < worker.version || generation == worker.version && (worker.revoked || session != worker.session) {
		worker.mu.Unlock()
		return wire.ErrExpired
	}
	if !worker.found.LiveVideo {
		worker.mu.Unlock()
		return wire.ErrUnsupported
	}
	if generation > worker.version {
		if worker.busy {
			worker.mu.Unlock()
			return wire.Error("VIDEO_BACKEND_BUSY", "Retry video after the device operation completes.", 409)
		}
		worker.version, worker.session, worker.revoked = generation, session, false
		worker.frames = make(map[string]frame)
	}
	if video, ok := worker.backend.(VideoBackend); ok {
		found := worker.found
		worker.mu.Unlock()
		return video.Video(ctx, found, publish)
	}
	if driver, ok := worker.driver.(VideoDriver); ok {
		worker.mu.Unlock()
		return driver.Video(ctx, publish)
	}
	if worker.busy || worker.driver != nil {
		worker.mu.Unlock()
		return wire.Error("VIDEO_BACKEND_BUSY", "Retry video after the device operation completes.", 409)
	}
	found, backend := worker.found, worker.backend
	open, cancel := context.WithCancel(ctx)
	worker.busy, worker.cancel = true, cancel
	worker.mu.Unlock()
	driver, err := backend.Open(open, found)
	cancel()
	worker.mu.Lock()
	worker.busy, worker.cancel = false, nil
	if err != nil {
		worker.mu.Unlock()
		return err
	}
	if ctx.Err() != nil || !worker.online || generation < worker.version || generation == worker.version && worker.revoked {
		worker.mu.Unlock()
		closeDriver(driver)
		return wire.ErrExpired
	}
	worker.driver = driver
	worker.mu.Unlock()
	video, ok := driver.(VideoDriver)
	if !ok {
		return wire.ErrUnsupported
	}
	return video.Video(ctx, publish)
}
