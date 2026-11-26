package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"mobile-use/internal/relay"
	"mobile-use/internal/store"
	"mobile-use/internal/wire"
)

const h264Profile = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"

var videoCodec = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: h264Profile}
var microphoneCodec = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: "minptime=10;useinbandfec=1"}

type VideoService struct {
	store     *store.Store
	hub       *relay.Hub
	root      context.Context
	api       *webrtc.API
	urls      []string
	secret    []byte
	relayOnly bool
	mu        sync.Mutex
	peers     map[string]viewerPeer
}

type viewerPeer struct {
	actor  wire.Actor
	target store.Target
	cancel context.CancelFunc
}

type iceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
}

type viewerTrack struct {
	source wire.Capability
	track  *webrtc.TrackLocalStaticSample
}

func NewVideo(ctx context.Context, db *store.Store, hub *relay.Hub, urls string, secret []byte, relayOnly bool) (*VideoService, error) {
	var entries []string
	for _, entry := range strings.Split(urls, ",") {
		if entry == "" {
			continue
		}
		scheme, address, valid := strings.Cut(entry, ":")
		if !valid || !slices.Contains([]string{"turn", "turns"}, scheme) || address == "" || strings.ContainsAny(address, " /#@\r\n\t") {
			return nil, fmt.Errorf("TURN URLs must use turn: or turns: with an administrator-configured host")
		}
		entries = append(entries, entry)
	}
	if len(entries) > 0 && len(secret) < 24 {
		return nil, fmt.Errorf("the TURN shared secret must have at least 24 bytes")
	}
	if relayOnly && len(entries) == 0 {
		return nil, fmt.Errorf("relay-only video requires a TURN URL")
	}
	engine := &webrtc.MediaEngine{}
	if err := engine.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: videoCodec, PayloadType: 102}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	if err := engine.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: microphoneCodec, PayloadType: 111}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}
	setting := webrtc.SettingEngine{}
	setting.SetICETimeouts(3*time.Second, 6*time.Second, time.Second)
	return &VideoService{store: db, hub: hub, root: ctx, api: webrtc.NewAPI(webrtc.WithMediaEngine(engine), webrtc.WithSettingEngine(setting)),
		urls: entries, secret: secret, relayOnly: relayOnly, peers: make(map[string]viewerPeer)}, nil
}

func (v *VideoService) ice() []iceServer {
	if len(v.urls) == 0 {
		return []iceServer{}
	}
	name := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + ":" + wire.NewID()
	mac := hmac.New(sha1.New, v.secret)
	_, _ = mac.Write([]byte(name))
	return []iceServer{{URLs: v.urls, Username: name, Credential: base64.StdEncoding.EncodeToString(mac.Sum(nil))}}
}

func (v *VideoService) Routes(router *http.ServeMux) {
	router.HandleFunc("GET /v1/sessions/{id}/video", withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
		_, sources, err := v.store.VideoTarget(r.Context(), actor, r.PathValue("id"), time.Now())
		if err != nil {
			writeError(w, err)
			return
		}
		policy := "all"
		if v.relayOnly {
			policy = "relay"
		}
		writeJSON(w, 200, map[string]any{"ice_servers": v.ice(), "ice_transport_policy": policy, "sources": sources})
	}))
	router.HandleFunc("POST /v1/sessions/{id}/video", withActor(v.offer))
	router.HandleFunc("DELETE /v1/sessions/{id}/video/{viewer}", withActor(func(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
		v.mu.Lock()
		peer, ok := v.peers[r.PathValue("viewer")]
		v.mu.Unlock()
		if !ok || peer.actor.Subject != actor.Subject || peer.actor.Workspace != actor.Workspace || peer.target.Session != r.PathValue("id") {
			writeError(w, wire.ErrNotFound)
			return
		}
		peer.cancel()
		w.WriteHeader(http.StatusNoContent)
	}))
}

func (v *VideoService) offer(w http.ResponseWriter, r *http.Request, actor wire.Actor) {
	var req struct {
		Type    string            `json:"type"`
		SDP     string            `json:"sdp"`
		Sources []wire.Capability `json:"sources,omitempty"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Type != "offer" || len(req.SDP) > 65536 || !strings.HasPrefix(req.SDP, "v=0") {
		writeError(w, wire.Error("INVALID_VIDEO_OFFER", "Send a WebRTC video offer.", 400))
		return
	}
	target, allowed, id, err := v.store.OpenViewer(r.Context(), actor, r.PathValue("id"), time.Now())
	if err != nil {
		writeError(w, err)
		return
	}
	opened := false
	defer func() {
		if !opened {
			v.closeViewer(target, id)
		}
	}()
	sources := req.Sources
	if len(sources) == 0 {
		sources = []wire.Capability{wire.Screen}
	}
	for i, source := range sources {
		if !slices.Contains(allowed, source) || slices.Contains(sources[:i], source) {
			writeError(w, wire.ErrForbidden)
			return
		}
	}
	var serverConfig []webrtc.ICEServer
	for _, entry := range v.ice() {
		serverConfig = append(serverConfig, webrtc.ICEServer{URLs: entry.URLs, Username: entry.Username, Credential: entry.Credential})
	}
	policy := webrtc.ICETransportPolicyAll
	if v.relayOnly {
		policy = webrtc.ICETransportPolicyRelay
	}
	peer, err := v.api.NewPeerConnection(webrtc.Configuration{ICEServers: serverConfig, ICETransportPolicy: policy})
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() {
		if !opened {
			_ = peer.Close()
		}
	}()
	tracks := make([]viewerTrack, 0, len(sources))
	for _, source := range sources {
		codec := videoCodec
		if source == wire.Microphone {
			codec = microphoneCodec
		}
		track, err := webrtc.NewTrackLocalStaticSample(codec, string(source), "mobile-use-"+string(source))
		if err != nil {
			writeError(w, err)
			return
		}
		sender, err := peer.AddTrack(track)
		if err != nil {
			writeError(w, err)
			return
		}
		tracks = append(tracks, viewerTrack{source, track})
		go drainRTCP(sender)
	}
	if err := peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: req.SDP}); err != nil {
		writeError(w, wire.Error("INVALID_VIDEO_OFFER", "The browser video offer could not be negotiated.", 422))
		return
	}
	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		writeError(w, wire.ErrUnsupported)
		return
	}
	for _, track := range tracks {
		format := "h264/90000"
		if track.source == wire.Microphone {
			format = "opus/48000"
		}
		if !strings.Contains(strings.ToLower(answer.SDP), format) {
			writeError(w, wire.Error("VIDEO_CODEC_UNAVAILABLE", "The browser did not offer the required media codec.", 422))
			return
		}
	}
	gathered := webrtc.GatheringCompletePromise(peer)
	if err := peer.SetLocalDescription(answer); err != nil {
		writeError(w, err)
		return
	}
	select {
	case <-gathered:
	case <-time.After(5 * time.Second):
		writeError(w, wire.Error("ICE_TIMEOUT", "Video network candidates did not become available.", 504))
		return
	case <-r.Context().Done():
		return
	}
	if peer.LocalDescription() == nil {
		writeError(w, wire.ErrUnsupported)
		return
	}
	ctx, cancel := context.WithCancel(v.root)
	if !actor.ExpiresAt.IsZero() {
		ctx, cancel = withViewerExpiry(ctx, cancel, actor.ExpiresAt)
	}
	connected := make(chan struct{})
	var signal sync.Once
	onState := func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			signal.Do(func() { close(connected) })
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			cancel()
		}
	}
	peer.OnConnectionStateChange(onState)
	onState(peer.ConnectionState())
	v.mu.Lock()
	v.peers[id] = viewerPeer{actor: actor, target: target, cancel: cancel}
	v.mu.Unlock()
	opened = true
	go v.run(ctx, cancel, peer, tracks, target, id, connected)
	writeJSON(w, 201, map[string]any{"viewer_id": id, "answer": peer.LocalDescription()})
}

func drainRTCP(sender *webrtc.RTPSender) {
	buffer := make([]byte, 1500)
	for {
		if _, _, err := sender.Read(buffer); err != nil {
			return
		}
	}
}

func withViewerExpiry(parent context.Context, stopParent context.CancelFunc, expiry time.Time) (context.Context, context.CancelFunc) {
	ctx, stop := context.WithDeadline(parent, expiry)
	return ctx, func() { stop(); stopParent() }
}

func (v *VideoService) run(ctx context.Context, cancel context.CancelFunc, peer *webrtc.PeerConnection, tracks []viewerTrack, target store.Target, id string, connected <-chan struct{}) {
	closeOnCancel := context.AfterFunc(ctx, func() { _ = peer.Close() })
	defer func() {
		closeOnCancel()
		cancel()
		_ = peer.Close()
		v.closeViewer(target, id)
		v.mu.Lock()
		delete(v.peers, id)
		v.mu.Unlock()
	}()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				check, stop := context.WithTimeout(ctx, time.Second)
				err := v.store.RenewViewer(check, target, id, now)
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	select {
	case <-ctx.Done():
		return
	case <-time.After(12 * time.Second):
		return
	case <-connected:
	}
	if strings.HasPrefix(target.Connector, "companion.") {
		companion, ok := v.hub.Companion.(*Companion)
		if !ok {
			return
		}
		var watching sync.WaitGroup
		for _, track := range tracks {
			watching.Go(func() { companion.watchMedia(ctx, companionMediaKey{target, track.source}, track.write) })
		}
		watching.Wait()
		return
	}
	_ = v.hub.Watch(ctx, target, func(frame wire.VideoFrame) error {
		return tracks[0].write(frame.Data, time.Duration(frame.DurationMicros)*time.Microsecond)
	})
}

func (track viewerTrack) write(data []byte, duration time.Duration) error {
	return track.track.WriteSample(media.Sample{Data: data, Duration: duration})
}

func (v *VideoService) closeViewer(target store.Target, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	v.store.CloseViewer(ctx, target, id)
}

func (v *VideoService) Stop(target store.Target) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, peer := range v.peers {
		if peer.target.Workspace == target.Workspace && peer.target.Session == target.Session && peer.target.Generation <= target.Generation {
			peer.cancel()
		}
	}
}
