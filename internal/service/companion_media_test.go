package service_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"mobile-use/internal/client"
	"mobile-use/internal/relay"
	"mobile-use/internal/service"
	"mobile-use/internal/store"
	"mobile-use/internal/wire"
)

func TestCompanionMediaSharesAuthorizedTracksAndRevokesOnPause(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	db, err := store.Open(ctx, t.TempDir()+"/media.sqlite", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	companion := service.NewCompanion(db, origin)
	hub := &relay.Hub{Store: db, Companion: companion}
	video, err := service.NewVideo(ctx, db, hub, "", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	auth := &service.Auth{Static: []service.Principal{{Token: tokenOwner, Actor: wire.Actor{Subject: "owner", Role: wire.Admin, Workspace: "media-test"}}}}
	svc := &service.Service{Store: db, Hub: hub, Video: video}
	server.Config.Handler = svc.HTTP(service.HTTPOptions{Origin: origin, Auth: auth, Video: video, Extra: func(mux *http.ServeMux) {
		companion.ConsoleRoutes(mux, auth, origin)
		companion.PhoneRoutes(mux)
	}})
	server.Start()
	defer server.Close()
	viewer, err := client.New(origin, tokenOwner)
	if err != nil {
		t.Fatal(err)
	}
	pair := request[struct{ Code string }](t, viewer, "POST", "/v1/companion/pairing", map[string]bool{"auto_enroll": true})
	var phone struct {
		Device string `json:"device_id"`
		Token  string `json:"token"`
	}
	if err := json.Unmarshal(phonePost(t, origin, "", "/companion/enroll", "application/json",
		[]byte(wire.Marshal(map[string]string{"code": pair.Code, "label": "Media test phone"})), 201), &phone); err != nil {
		t.Fatal(err)
	}
	hb, err := http.NewRequestWithContext(ctx, "POST", origin+"/companion/heartbeat", strings.NewReader(`{"present_session":""}`))
	if err != nil {
		t.Fatal(err)
	}
	hb.Header.Set("Authorization", "Bearer "+phone.Token)
	hb.Header.Set("Content-Type", "application/json")
	hb.Header.Set("X-Mobile-Use-Media", "1")
	resp, err := http.DefaultClient.Do(hb)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat returned %d", resp.StatusCode)
	}

	dial := func(session string, source wire.Capability) (*websocket.Conn, int) {
		t.Helper()
		url := "ws" + strings.TrimPrefix(origin, "http") + "/companion/sessions/" + session + "/media/" + string(source)
		conn, response, err := websocket.DefaultDialer.DialContext(ctx, url, http.Header{"Authorization": {"Bearer " + phone.Token}})
		if conn != nil {
			t.Cleanup(func() { conn.Close() })
		}
		if response == nil {
			t.Fatalf("media handshake for %s failed: %v", source, err)
		}
		response.Body.Close()
		return conn, response.StatusCode
	}
	start := func(sources []wire.Capability) wire.Session {
		t.Helper()
		session := request[wire.Session](t, viewer, "POST", "/v1/sessions", wire.StartSession{
			DeviceID: phone.Device, Purpose: "Verify attended streaming", Capabilities: sources,
		})
		phonePost(t, origin, phone.Token, "/companion/sessions/"+session.ID+"/approve", "application/json", []byte(`{}`), 200)
		return session
	}
	screenOnly := start([]wire.Capability{wire.Screen})
	if _, status := dial(screenOnly.ID, wire.Camera); status != http.StatusForbidden {
		t.Fatalf("camera ingress without capability returned %d", status)
	}
	request[wire.Session](t, viewer, "POST", "/v1/sessions/"+screenOnly.ID+"/end", nil)

	sources := []wire.Capability{wire.Screen, wire.Camera, wire.Microphone}
	session := start(sources)
	path := "/v1/sessions/" + session.ID + "/video"
	config := request[struct{ Sources []wire.Capability }](t, viewer, "GET", path, nil)
	if !slices.Equal(config.Sources, sources) {
		t.Fatalf("viewer advertised %v, want %v", config.Sources, sources)
	}
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	for _, kind := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeAudio} {
		if _, err := peer.AddTransceiverFromKind(kind, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			t.Fatal(err)
		}
	}
	received := make(chan *webrtc.TrackRemote, len(sources))
	peer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if _, _, err := track.ReadRTP(); err == nil {
			received <- track
		}
	})
	offer, err := peer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(peer)
	if err := peer.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gathered:
	case <-ctx.Done():
		t.Fatal("viewer ICE gathering timed out")
	}
	answer := request[struct{ Answer webrtc.SessionDescription }](t, viewer, "POST", path, struct {
		webrtc.SessionDescription
		Sources []wire.Capability `json:"sources"`
	}{*peer.LocalDescription(), sources})
	if err := peer.SetRemoteDescription(answer.Answer); err != nil {
		t.Fatal(err)
	}
	publishers := make(map[wire.Capability]*websocket.Conn)
	for _, source := range sources {
		conn, status := dial(session.ID, source)
		if status != http.StatusSwitchingProtocols {
			t.Fatalf("%s ingress returned %d", source, status)
		}
		publishers[source] = conn
	}
	avc := []byte{0, 0, 0, 1, 0x67, 0x42, 0xe0, 0x1f, 0, 0, 1, 0x68, 0xce, 0x06, 0xe2, 0, 0, 1, 0x65, 0x88, 0x84, 0x20}
	opus := []byte{0xf8, 0xff, 0xfe}
	wanted := map[string]string{"screen": webrtc.MimeTypeH264, "camera": webrtc.MimeTypeH264, "microphone": webrtc.MimeTypeOpus}
	for pts := uint64(0); len(wanted) > 0 && ctx.Err() == nil; pts += 40_000 {
		for source, publisher := range publishers {
			data := avc
			if source == wire.Microphone {
				data = opus
			}
			packet := append(binary.BigEndian.AppendUint64(nil, pts), data...)
			_ = publisher.SetWriteDeadline(time.Now().Add(time.Second))
			if err := publisher.WriteMessage(websocket.BinaryMessage, packet); err != nil {
				t.Fatal(err)
			}
			_ = publisher.SetReadDeadline(time.Now().Add(time.Second))
			kind, ack, err := publisher.ReadMessage()
			if err != nil || kind != websocket.BinaryMessage || len(ack) != 9 || binary.BigEndian.Uint64(ack) != pts {
				t.Fatalf("%s sample acknowledgement = %v (%v)", source, ack, err)
			}
		}
		select {
		case track := <-received:
			if got := track.Codec().MimeType; !strings.EqualFold(got, wanted[track.ID()]) {
				t.Fatalf("track %s codec = %s, expected %s", track.ID(), got, wanted[track.ID()])
			}
			delete(wanted, track.ID())
		case <-time.After(40 * time.Millisecond):
		}
	}
	if len(wanted) != 0 {
		t.Fatalf("tracks did not receive RTP: %v", wanted)
	}
	request[wire.Session](t, viewer, "POST", "/v1/sessions/"+session.ID+"/pause", nil)
	for source, publisher := range publishers {
		_ = publisher.SetReadDeadline(time.Now().Add(time.Second))
		if _, _, err := publisher.ReadMessage(); err == nil {
			t.Fatalf("old %s publisher survived pause", source)
		}
		if _, status := dial(session.ID, source); status != http.StatusConflict {
			t.Fatalf("paused %s ingress returned %d", source, status)
		}
	}
}
