package relay

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"os"
	"testing"
	"time"

	"mobile-use/internal/connector"
	"mobile-use/internal/store"
	"mobile-use/internal/wire"
)

type relayDevice struct{}

func (relayDevice) Discover(context.Context) ([]connector.Found, error) {
	return []connector.Found{{NativeID: "relay-fixture", Inventory: wire.Inventory{Label: "Remote fixture", Platform: "android", Mode: "adb", LiveVideo: true, Capabilities: wire.Capabilities{wire.Screen: true}}}}, nil
}
func (relayDevice) Open(context.Context, connector.Found) (connector.Driver, error) {
	return relayDriver{}, nil
}
func (relayDevice) Video(ctx context.Context, _ connector.Found, publish func(wire.VideoFrame) error) error {
	frame := wire.VideoFrame{Data: []byte{0, 0, 0, 1, 0x65, 0x90}, DurationMicros: 50000}
	for {
		if err := publish(frame); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

type relayDriver struct{}

func (relayDriver) Display(context.Context) (connector.Display, error) {
	return connector.Display{Width: 64, Height: 100, Orientation: "portrait", AppID: "org.mobileuse.relayfixture"}, nil
}
func (relayDriver) Screenshot(context.Context) (*wire.Image, error) {
	return &wire.Image{MIME: "image/png", Data: []byte{137, 80, 78, 71, 13, 10, 26, 10}}, nil
}
func (relayDriver) Tree(context.Context) ([]wire.Node, error) { return nil, wire.ErrUnsupported }
func (relayDriver) Action(context.Context, wire.Action, connector.Display, *wire.Rect) error {
	return wire.ErrUnsupported
}
func (relayDriver) Apps(context.Context) ([]string, error)  { return nil, wire.ErrUnsupported }
func (relayDriver) App(context.Context, string, bool) error { return wire.ErrUnsupported }
func (relayDriver) Browser(context.Context, wire.Operation) (wire.Result, error) {
	return wire.Result{}, wire.ErrUnsupported
}
func (relayDriver) Close(context.Context) {}

func TestTwoReplicaCommandAndVideoRouting(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 24*time.Second)
	defer cancel()
	db, err := store.Open(ctx, t.TempDir()+"/relay.sqlite", os.Getenv("MOBILE_USE_TEST_POSTGRES"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	workspace := "relay-" + wire.NewID()
	ca, certificates := testTLS(t, "replica-one", "replica-two", "relay-device")
	listen1, listen2 := testListener(t), testListener(t)
	serverTLS := func(cert tls.Certificate) *tls.Config {
		return &tls.Config{Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: ca, MinVersion: tls.VersionTLS13}
	}
	clientTLS := func(cert tls.Certificate) *tls.Config {
		return &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: ca, MinVersion: tls.VersionTLS13}
	}
	first := &Hub{Store: db, RelayAddress: listen1.Addr().String(), ConnectorNames: map[string]string{"relay-device": workspace}, PeerNames: map[string]bool{"replica-two": true}, PeerTLS: clientTLS(certificates[0])}
	second := &Hub{Store: db, RelayAddress: listen2.Addr().String(), ConnectorNames: first.ConnectorNames, PeerNames: map[string]bool{"replica-one": true}, PeerTLS: clientTLS(certificates[1])}
	primary, stopPrimary := context.WithCancel(ctx)
	defer stopPrimary()
	go func() { _ = first.serveListener(primary, listen1, serverTLS(certificates[0])) }()
	go func() { _ = second.serveListener(ctx, listen2, serverTLS(certificates[1])) }()
	manager := connector.New(workspace, "relay-device", relayDevice{})
	go func() { _ = Connect(ctx, listen1.Addr().String(), clientTLS(certificates[2]), manager) }()
	actor := wire.Actor{Workspace: workspace, Role: wire.Admin, Subject: "owner"}
	var device string
	for device == "" {
		inventory, err := db.Devices(ctx, actor, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if len(inventory) > 0 && inventory[0].Online {
			device = inventory[0].ID
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("mTLS connector did not register")
		case <-time.After(50 * time.Millisecond):
		}
	}
	if _, _, err := db.Enroll(ctx, actor, device, wire.Enrollment{Profile: wire.Dedicated, Owner: actor.Subject}, time.Now()); err != nil {
		t.Fatal(err)
	}
	session, err := db.Start(ctx, actor, wire.StartSession{DeviceID: device, Purpose: "Verify cross-replica routing", Capabilities: []wire.Capability{wire.Screen}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	op := wire.Operation{ID: wire.NewID(), Kind: "observe", Observe: &wire.ObserveRequest{Screen: true}}
	ticket, err := db.BeginOperation(ctx, actor, session.ID, op, "test-input", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	shot, err := second.Dispatch(ctx, ticket.Target, ticket.Dispatch)
	if err != nil || shot.Observation == nil || shot.Observation.AppID != "org.mobileuse.relayfixture" {
		t.Fatalf("cross-replica screenshot failed: %v, %+v", err, shot)
	}
	ready := errors.New("received remote video")
	err = second.Watch(ctx, ticket.Target, func(frame wire.VideoFrame) error {
		if len(frame.Data) != 6 || frame.DurationMicros != 50000 {
			t.Fatalf("wrong frame: %+v", frame)
		}
		return ready
	})
	if !errors.Is(err, ready) {
		t.Fatalf("cross-replica video failed: %v", err)
	}
	stopPrimary()
	reconnect, disconnect := context.WithCancel(ctx)
	defer disconnect()
	go func() { _ = Connect(reconnect, second.RelayAddress, clientTLS(certificates[2]), manager) }()
	awaitReplicaRoute(t, ctx, db, actor, second.RelayAddress, true)
	oldSession, err := db.Session(ctx, actor, session.ID)
	if err != nil || oldSession.State != wire.Expired || oldSession.Reason != "connector_replaced" {
		t.Fatalf("old session did not expire after failover: %+v, %v", oldSession, err)
	}
	if err := db.Confirm(ctx, ticket.Target, time.Now()); !errors.Is(err, wire.ErrExpired) {
		t.Fatalf("old generation remained valid: %v", err)
	}
	newSession, err := db.Start(ctx, actor, wire.StartSession{DeviceID: device, Purpose: "Verify recovery on the surviving replica", Capabilities: []wire.Capability{wire.Screen}}, time.Now())
	if err != nil || newSession.Generation <= session.Generation {
		t.Fatalf("could not reserve a fresh lease: %+v, %v", newSession, err)
	}
	op.ID = wire.NewID()
	newTicket, err := db.BeginOperation(ctx, actor, newSession.ID, op, "test-input", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	shot, err = second.Dispatch(ctx, newTicket.Target, newTicket.Dispatch)
	if err != nil || shot.Observation == nil {
		t.Fatalf("screenshot after failover failed: %v, %+v", err, shot)
	}
	if _, _, err := db.ChangeSession(ctx, actor, newSession.ID, "end", time.Now()); err != nil {
		t.Fatal(err)
	}
	second.Stop(ctx, newTicket.Target)
	shot, err = second.Dispatch(ctx, newTicket.Target, newTicket.Dispatch)
	if err != nil || shot.Fault == nil || shot.Fault.Code != wire.ErrExpired.Code {
		t.Fatalf("stopped lease accepted: %v, %+v", err, shot)
	}
	disconnect()
	awaitReplicaRoute(t, ctx, db, actor, "", false)
}

func awaitReplicaRoute(t *testing.T, ctx context.Context, db *store.Store, actor wire.Actor, address string, online bool) {
	t.Helper()
	for {
		route, err := db.Route(ctx, actor.Workspace, "relay-device", time.Now())
		devices, listErr := db.Devices(ctx, actor, time.Now())
		if route == address && (err == nil || !online && errors.Is(err, wire.ErrOffline)) && listErr == nil && len(devices) == 1 && devices[0].Online == online {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("connector did not reach expected route state: route=%q, online=%v, errors=%v/%v", route, online, err, listErr)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func testListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener
}

func testTLS(t *testing.T, names ...string) (*x509.CertPool, []tls.Certificate) {
	t.Helper()
	newKey := func() *ecdsa.PrivateKey {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	key := newKey()
	now := time.Now()
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	raw, err := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	var certificates []tls.Certificate
	for index, name := range names {
		leafKey := newKey()
		leaf := &x509.Certificate{SerialNumber: big.NewInt(int64(index + 2)), Subject: pkix.Name{CommonName: name}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: root.NotBefore, NotAfter: root.NotAfter, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
		der, err := x509.CreateCertificate(rand.Reader, leaf, parsed, &leafKey.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		certificates = append(certificates, tls.Certificate{Certificate: [][]byte{der}, PrivateKey: leafKey})
	}
	return pool, certificates
}
