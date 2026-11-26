package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"mobile-use/internal/store"
	"mobile-use/internal/wire"
)

type companionMediaKey struct {
	target store.Target
	source wire.Capability
}

type companionMediaFeed struct {
	publisher *websocket.Conn
	viewers   map[*companionMediaViewer]struct{}
}

type companionMediaViewer struct {
	frames  chan companionSample
	needIDR bool
}

type companionSample struct {
	data      []byte
	pts       uint64
	received  time.Time
	publisher *websocket.Conn
}

const mediaHeaderSize = 8 // Presentation timestamp in microseconds, followed by one encoded sample.

func (c *Companion) streamPhoneMedia(w http.ResponseWriter, r *http.Request, phone store.Companion) {
	source := wire.Capability(r.PathValue("source"))
	if source != wire.Screen && source != wire.Camera && source != wire.Microphone {
		writeError(w, wire.ErrNotFound)
		return
	}
	ctx := r.Context()
	session, err := c.phoneSession(ctx, phone, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if !containsCap(session.Capabilities, source) {
		writeError(w, wire.ErrForbidden)
		return
	}
	key := companionMediaKey{store.Target{Workspace: phone.Workspace, Connector: phoneConnector(phone.ID), Device: phone.ID, Session: session.ID, Generation: session.Generation}, source}
	if session.Paused {
		writeError(w, wire.ErrExpired)
		return
	}
	if err = c.Store.Confirm(ctx, key.target, time.Now()); err != nil {
		writeError(w, err)
		return
	}
	upgrader := websocket.Upgrader{}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	c.mu.Lock()
	feed := c.mediaFeedLocked(key)
	if feed.publisher != nil {
		_ = feed.publisher.Close()
	}
	feed.publisher = conn
	for viewer := range feed.viewers {
		viewer.reset(source != wire.Microphone)
	}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if feed.publisher == conn {
			feed.publisher = nil
			for viewer := range feed.viewers {
				viewer.reset(source != wire.Microphone)
			}
			c.releaseMediaFeedLocked(key, feed)
		}
	}()

	// Confirm after registration as well so Stop can fence a publisher racing with revocation.
	var nextCheck time.Time
	checkLease := func(now time.Time) error {
		if now.Before(nextCheck) {
			return nil
		}
		check, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if err := c.Store.Confirm(check, key.target, now); err != nil {
			return err
		}
		nextCheck = now.Add(time.Second)
		return nil
	}
	if checkLease(time.Now()) != nil {
		return
	}
	limit := 650 << 10
	if source == wire.Microphone {
		limit = 4 << 10
	}
	conn.SetReadLimit(int64(mediaHeaderSize + limit))
	_ = conn.SetReadDeadline(time.Now().Add(12 * time.Second))
	conn.SetPingHandler(func(data string) error {
		now := time.Now()
		if err := checkLease(now); err != nil {
			return err
		}
		_ = conn.SetReadDeadline(now.Add(12 * time.Second))
		return conn.WriteControl(websocket.PongMessage, []byte(data), now.Add(time.Second))
	})
	for {
		kind, packet, err := conn.ReadMessage()
		if err != nil {
			return
		}
		now := time.Now()
		if kind != websocket.BinaryMessage || len(packet) <= mediaHeaderSize || checkLease(now) != nil {
			return
		}
		_ = conn.SetReadDeadline(now.Add(12 * time.Second))
		data := packet[mediaHeaderSize:]
		idr := false
		if source != wire.Microphone {
			if !bytes.HasPrefix(data, avcStartCode) && !bytes.HasPrefix(data, []byte{0, 0, 0, 1}) {
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseUnsupportedData, "Send Annex B H.264 samples."), now.Add(time.Second))
				return
			}
			idr = isAVCRandomAccessPoint(data)
		}
		c.mu.Lock()
		if feed.publisher != conn {
			c.mu.Unlock()
			return
		}
		sample := companionSample{data: data, pts: binary.BigEndian.Uint64(packet), received: now, publisher: conn}
		for viewer := range feed.viewers {
			viewer.offer(sample, source != wire.Microphone, idr)
		}
		preview := source == wire.Screen && len(c.frameSubs[phone.ID]) != 0
		c.mu.Unlock()

		// Echo the capture timestamp so the phone detects backlog even after TCP accepts the write.
		var ack [mediaHeaderSize + 1]byte
		copy(ack[:], packet[:mediaHeaderSize])
		if preview {
			ack[mediaHeaderSize] = 1
		}
		_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
		if err := conn.WriteMessage(websocket.BinaryMessage, ack[:]); err != nil {
			return
		}
	}
}

var avcStartCode = []byte{0, 0, 1}

func isAVCRandomAccessPoint(data []byte) bool {
	var sps, pps bool
	for len(data) > 0 {
		start := bytes.Index(data, avcStartCode)
		if start < 0 || start+len(avcStartCode) >= len(data) {
			return false
		}
		data = data[start+len(avcStartCode):]
		switch data[0] & 31 {
		case 7:
			sps = true
		case 8:
			pps = true
		case 5:
			return sps && pps
		}
		data = data[1:]
	}
	return false
}

func (v *companionMediaViewer) reset(needIDR bool) {
	for len(v.frames) > 0 {
		select {
		case <-v.frames:
		default:
		}
	}
	v.needIDR = needIDR
}

func (v *companionMediaViewer) offer(sample companionSample, video, idr bool) {
	if len(v.frames) == cap(v.frames) {
		v.reset(video)
	}
	if v.needIDR && !idr {
		return
	}
	v.needIDR = false
	v.frames <- sample
}

func (c *Companion) mediaFeedLocked(key companionMediaKey) *companionMediaFeed {
	if c.media == nil {
		c.media = make(map[companionMediaKey]*companionMediaFeed)
	}
	feed := c.media[key]
	if feed == nil {
		feed = &companionMediaFeed{viewers: make(map[*companionMediaViewer]struct{})}
		c.media[key] = feed
	}
	return feed
}

func (c *Companion) releaseMediaFeedLocked(key companionMediaKey, feed *companionMediaFeed) {
	if feed.publisher == nil && len(feed.viewers) == 0 {
		delete(c.media, key)
	}
}

func (c *Companion) stopMediaLocked(target store.Target) {
	for key, feed := range c.media {
		if key.target.Workspace != target.Workspace || key.target.Device != target.Device ||
			target.Session != "" && (key.target.Session != target.Session || key.target.Generation > target.Generation) {
			continue
		}
		if feed.publisher != nil {
			_ = feed.publisher.Close()
			feed.publisher = nil
		}
		for viewer := range feed.viewers {
			viewer.reset(key.source != wire.Microphone)
		}
		c.releaseMediaFeedLocked(key, feed)
	}
}

func (c *Companion) watchMedia(ctx context.Context, key companionMediaKey, publish func([]byte, time.Duration) error) {
	video := key.source != wire.Microphone
	queue, nominal := 3, int64(41667)
	if !video {
		queue, nominal = 5, 20000
	}
	viewer := &companionMediaViewer{frames: make(chan companionSample, queue), needIDR: video}
	c.mu.Lock()
	feed := c.mediaFeedLocked(key)
	feed.viewers[viewer] = struct{}{}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(feed.viewers, viewer)
		c.releaseMediaFeedLocked(key, feed)
	}()
	var lastPublisher *websocket.Conn
	var lastPTS uint64
	for {
		select {
		case <-ctx.Done():
			return
		case sample := <-viewer.frames:
			c.mu.Lock()
			current := feed.publisher == sample.publisher
			if current && time.Since(sample.received) > 250*time.Millisecond {
				viewer.reset(video)
				current = false
			}
			c.mu.Unlock()
			if !current || ctx.Err() != nil {
				continue
			}
			duration := nominal
			if sample.publisher == lastPublisher && sample.pts > lastPTS && sample.pts-lastPTS < 1_000_000 {
				duration = int64(sample.pts - lastPTS)
			}
			lastPublisher, lastPTS = sample.publisher, sample.pts
			if publish(sample.data, time.Duration(duration)*time.Microsecond) != nil {
				return
			}
		}
	}
}
