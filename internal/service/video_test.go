package service_test

import (
	"context"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
	"github.com/pion/webrtc/v4"
	"mobile-use/internal/client"
	"mobile-use/internal/connector"
	"mobile-use/internal/relay"
	"mobile-use/internal/service"
	"mobile-use/internal/store"
	"mobile-use/internal/wire"
)

type videoDevice struct{}

func (videoDevice) Discover(context.Context) ([]connector.Found, error) {
	return []connector.Found{{NativeID: "video-testing", Inventory: wire.Inventory{Label: "Synthetic video device", Platform: "android", Mode: "adb", Capabilities: wire.Capabilities{wire.Screen: true}, LiveVideo: true}}}, nil
}

func (videoDevice) Open(context.Context, connector.Found) (connector.Driver, error) {
	return nil, wire.ErrUnsupported
}

func (videoDevice) Video(ctx context.Context, _ connector.Found, publish func(wire.VideoFrame) error) error {
	tick := time.NewTicker(60 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			if err := publish(wire.VideoFrame{Data: []byte{0, 0, 0, 1, 0x65, 0x88, 0x84, 0x20}, DurationMicros: 60000}); err != nil {
				return err
			}
		}
	}
}

func TestVideoUsesTURNAndRevokesViewer(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	secret := "test-fixture-turn-secret-geared-to-loopback"
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logs := logging.NewDefaultLoggerFactory()
	turnServer, err := turn.NewServer(turn.ServerConfig{Realm: "mobile-use.test", LoggerFactory: logs, AuthHandler: turn.LongTermTURNRESTAuthHandler(secret, logs.NewLogger("turn-test")),
		PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: udp, RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{RelayAddress: net.ParseIP("127.0.0.1"), Address: "127.0.0.1"}}},
	})
	if err != nil {
		udp.Close()
		t.Fatal(err)
	}
	defer turnServer.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + listener.Addr().String()
	db, err := store.Open(ctx, t.TempDir()+"/video.sqlite", "")
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	defer db.Close()
	manager := connector.New("video-test", "local", videoDevice{})
	devices := manager.Inventory(ctx)
	if err := db.Register(ctx, "video-test", "local", devices, time.Now()); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	hub := &relay.Hub{Store: db, LocalWorkspace: "video-test", Local: manager}
	video, err := service.NewVideo(ctx, db, hub, "turn:"+udp.LocalAddr().String()+"?transport=udp", []byte(secret), true)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	auth := &service.Auth{Static: []service.Principal{{Token: tokenOwner, Actor: wire.Actor{Subject: "owner", Role: wire.Admin, Workspace: "video-test"}}}}
	svc := &service.Service{Store: db, Hub: hub, Video: video, DigestKey: []byte(secret)}
	server := httptest.NewUnstartedServer(svc.HTTP(service.HTTPOptions{Origin: origin, Auth: auth, Video: video}))
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	c, _ := client.New(origin, tokenOwner)
	request[wire.Device](t, c, "PUT", "/v1/devices/"+devices[0].ID, wire.Enrollment{Owner: "owner", Profile: wire.Dedicated})
	session := request[wire.Session](t, c, "POST", "/v1/sessions", wire.StartSession{DeviceID: devices[0].ID, Purpose: "Verify TURN video", Capabilities: []wire.Capability{wire.Screen}})
	path := "/v1/sessions/" + session.ID + "/video"
	type ice struct {
		URLs       []string `json:"urls"`
		Username   string   `json:"username"`
		Credential string   `json:"credential"`
	}
	config := request[struct {
		Servers []ice  `json:"ice_servers"`
		Policy  string `json:"ice_transport_policy"`
	}](t, c, "GET", path, nil)
	if config.Policy != "relay" || len(config.Servers) != 1 {
		t.Fatalf("TURN config was not issued: %+v", config)
	}
	credentials := config.Servers[0]
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{ICETransportPolicy: webrtc.ICETransportPolicyRelay,
		ICEServers: []webrtc.ICEServer{{URLs: credentials.URLs, Username: credentials.Username, Credential: credentials.Credential}}})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	transceiver, err := peer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan struct{}, 1)
	peer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if _, _, err := track.ReadRTP(); err == nil {
			received <- struct{}{}
		}
	})
	offer, err := peer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	done := webrtc.GatheringCompletePromise(peer)
	if err := peer.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("TURN candidate gathering timed out")
	}
	answer := request[struct {
		Answer webrtc.SessionDescription `json:"answer"`
		Viewer string                    `json:"viewer_id"`
	}](t, c, "POST", path, peer.LocalDescription())
	if err := peer.SetRemoteDescription(answer.Answer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
	case <-ctx.Done():
		t.Fatal("TURN-relayed RTP was not received")
	}
	pair, err := transceiver.Receiver().Transport().ICETransport().GetSelectedCandidatePair()
	if err != nil || pair == nil || pair.Local.Typ != webrtc.ICECandidateTypeRelay || pair.Remote.Typ != webrtc.ICECandidateTypeRelay {
		t.Fatalf("expected a relay pair, got %v, %v", pair, err)
	}
	request[wire.Session](t, c, "POST", "/v1/sessions/"+session.ID+"/pause", nil)
	if err := c.Request(ctx, "GET", path, nil, nil); wire.Public(err).Code != "SESSION_INACTIVE" {
		t.Fatalf("paused session still grants video: %v", err)
	}
}
