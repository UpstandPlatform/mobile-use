package relay

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"mobile-use/internal/connector"
	"mobile-use/internal/store"
	"mobile-use/internal/wire"
)

type Hub struct {
	Store          *store.Store
	RelayAddress   string
	ConnectorNames map[string]string
	PeerNames      map[string]bool
	PeerTLS        *tls.Config
	LocalWorkspace string
	Local          *connector.Manager
	Companion      Companion
	mu             sync.Mutex
	connections    map[string]*connection
}

type Companion interface {
	Dispatch(context.Context, store.Target, wire.Dispatch) (wire.Result, error)
	Stop(store.Target)
}

type connection struct {
	stream    Stream
	sendMutex sync.Mutex
	mu        sync.Mutex
	pending   map[string]chan wire.Result
	viewers   map[string]*videoWatch
	closed    chan struct{}
}

func (c *connection) send(message Message) error {
	c.sendMutex.Lock()
	defer c.sendMutex.Unlock()
	return send(c.stream, message)
}

func (c *connection) execute(ctx context.Context, command wire.Dispatch) (wire.Result, error) {
	if ctx.Err() != nil {
		return wire.Result{}, wire.ErrExpired
	}
	id := command.Operation.ID
	result := make(chan wire.Result, 1)
	c.mu.Lock()
	if len(c.pending) >= 64 {
		c.mu.Unlock()
		return wire.Result{}, wire.Error("CONNECTOR_BUSY", "The connector queue is full.", 429)
	}
	c.pending[id] = result
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err := c.send(Message{Type: "execute", Command: &command}); err != nil {
		return wire.Result{}, wire.ErrUnknown
	}
	select {
	case value := <-result:
		return value, nil
	case <-ctx.Done():
		return wire.Result{}, wire.ErrUnknown
	case <-c.closed:
		return wire.Result{}, wire.ErrUnknown
	}
}

func certificateName(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return ""
	}
	info, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(info.State.VerifiedChains) == 0 || len(info.State.PeerCertificates) == 0 {
		return ""
	}
	return info.State.PeerCertificates[0].Subject.CommonName
}

func (h *Hub) Connect(stream grpc.ServerStream) error {
	name := certificateName(stream.Context())
	workspace, ok := h.ConnectorNames[name]
	if !ok {
		return status.Error(codes.PermissionDenied, "Connector certificate is not enrolled")
	}
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	messages := make(chan Message)
	failures := make(chan error, 1)
	go func() {
		for {
			message, err := receive(stream)
			if err != nil {
				failures <- err
				return
			}
			select {
			case messages <- message:
			case <-ctx.Done():
				return
			}
		}
	}()
	var opening Message
	select {
	case opening = <-messages:
	case err := <-failures:
		return err
	case <-time.After(5 * time.Second):
		return status.Error(codes.DeadlineExceeded, "Connector hello not received")
	case <-ctx.Done():
		return ctx.Err()
	}
	if opening.Type != "hello" || opening.Version != 1 {
		return status.Error(codes.FailedPrecondition, "Connector protocol version is unsupported")
	}
	connectionID := wire.NewID()
	if err := h.Store.ClaimRoute(ctx, workspace, name, connectionID, h.RelayAddress, time.Now()); err != nil {
		return status.Error(codes.AlreadyExists, "Connector already connected or route unavailable")
	}
	conn := &connection{stream: stream, pending: make(map[string]chan wire.Result), viewers: make(map[string]*videoWatch), closed: make(chan struct{})}
	key := workspace + "/" + name
	h.mu.Lock()
	if h.connections == nil {
		h.connections = make(map[string]*connection)
	}
	h.connections[key] = conn
	h.mu.Unlock()
	defer func() {
		close(conn.closed)
		h.mu.Lock()
		if h.connections[key] == conn {
			delete(h.connections, key)
		}
		h.mu.Unlock()
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := h.Store.DropRoute(cleanup, workspace, name, connectionID, time.Now()); err != nil {
			slog.Warn("Connector route cleanup failed", "connector", name)
		}
	}()
	if err := conn.send(Message{Type: "ready", Version: 1}); err != nil {
		return err
	}
	watchdog := time.NewTimer(store.ConnectorTTL)
	defer watchdog.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-failures:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case <-watchdog.C:
			return status.Error(codes.DeadlineExceeded, "Connector heartbeat expired")
		case message := <-messages:
			switch message.Type {
			case "inventory":
				now := time.Now()
				if err := h.Store.RenewRoute(ctx, workspace, name, connectionID, now); err != nil {
					return status.Error(codes.FailedPrecondition, "Connector lease expired")
				}
				if err := h.Store.Register(ctx, workspace, name, message.Inventory, now); err != nil {
					return status.Error(codes.InvalidArgument, "Connector inventory could not be accepted")
				}
				watchdog.Reset(store.ConnectorTTL)
			case "result":
				if message.Result == nil {
					return status.Error(codes.InvalidArgument, "Missing operation result")
				}
				conn.mu.Lock()
				waiter := conn.pending[message.Result.ID]
				if waiter != nil {
					delete(conn.pending, message.Result.ID)
				}
				conn.mu.Unlock()
				if waiter != nil {
					waiter <- *message.Result
				}
			case "video_finished":
				if watch := conn.video(message.VideoID); watch != nil {
					watch.cancel()
				}
			default:
				return status.Error(codes.InvalidArgument, "Unsupported connector message")
			}
		}
	}
}

func (h *Hub) conn(target store.Target) *connection {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.connections[target.Workspace+"/"+target.Connector]
}

func (h *Hub) Dispatch(ctx context.Context, target store.Target, cmd wire.Dispatch) (wire.Result, error) {
	if h.Companion != nil && strings.HasPrefix(target.Connector, "companion.") {
		return h.Companion.Dispatch(ctx, target, cmd)
	}
	if target.Workspace == h.LocalWorkspace && target.Connector == "local" && h.Local != nil {
		return h.Local.Execute(ctx, cmd), nil
	}
	if conn := h.conn(target); conn != nil {
		return conn.execute(ctx, cmd)
	}
	response, err := h.forward(ctx, target, Message{Type: "execute", Target: &target, Command: &cmd})
	if err != nil {
		return wire.Result{}, err
	}
	if response.Result == nil {
		return wire.Result{}, wire.ErrUnknown
	}
	return *response.Result, nil
}

func (h *Hub) Stop(ctx context.Context, target store.Target) {
	if h.Companion != nil && strings.HasPrefix(target.Connector, "companion.") {
		h.Companion.Stop(target)
		return
	}
	if target.Workspace == h.LocalWorkspace && target.Connector == "local" && h.Local != nil {
		h.Local.Stop(target.Device, target.Session, target.Generation)
		return
	}
	if conn := h.conn(target); conn != nil {
		_ = conn.send(Message{Type: "stop", Stop: &target})
		return
	}
	_, _ = h.forward(ctx, target, Message{Type: "stop", Stop: &target, Target: &target})
}

func (h *Hub) Forward(ctx context.Context, request *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error) {
	if !h.PeerNames[certificateName(ctx)] {
		return nil, status.Error(codes.PermissionDenied, "Untrusted relay peer")
	}
	message, err := unpack(request)
	if err != nil {
		return nil, err
	}
	if message.Target == nil {
		return nil, status.Error(codes.InvalidArgument, "Missing relay target")
	}
	conn := h.conn(*message.Target)
	if conn == nil {
		return nil, status.Error(codes.Unavailable, "Connector is disconnected")
	}
	switch message.Type {
	case "execute":
		if message.Command == nil || message.Command.DeviceID != message.Target.Device || message.Command.SessionID != message.Target.Session || message.Command.Generation != message.Target.Generation {
			return nil, status.Error(codes.InvalidArgument, "Invalid dispatch")
		}
		if err := h.Store.Confirm(ctx, *message.Target, time.Now()); err != nil {
			return nil, status.Error(codes.FailedPrecondition, "Session is inactive")
		}
		result, err := conn.execute(ctx, *message.Command)
		if err != nil {
			return nil, status.Error(codes.Aborted, "Device outcome is unknown")
		}
		return packet(Message{Type: "result", Result: &result}), nil
	case "stop":
		if err := conn.send(Message{Type: "stop", Stop: message.Target}); err != nil {
			return nil, err
		}
		return packet(Message{Type: "ready"}), nil
	default:
		return nil, status.Error(codes.InvalidArgument, "Unsupported relay request")
	}
}

func (h *Hub) forward(ctx context.Context, target store.Target, message Message) (Message, error) {
	if h.PeerTLS == nil {
		return Message{}, wire.ErrOffline
	}
	address, err := h.Store.Route(ctx, target.Workspace, target.Connector, time.Now())
	if err != nil {
		return Message{}, err
	}
	if address == "" || address == h.RelayAddress {
		return Message{}, wire.ErrOffline
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		return Message{}, wire.ErrOffline
	}
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(credentials.NewTLS(h.PeerTLS.Clone())), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxMessage)))
	if err != nil {
		return Message{}, wire.ErrOffline
	}
	defer conn.Close()
	var response wrapperspb.BytesValue
	if err := conn.Invoke(ctx, forwardMethod, packet(message), &response); err != nil {
		if status.Code(err) == codes.FailedPrecondition {
			return Message{}, wire.ErrExpired
		}
		return Message{}, wire.ErrUnknown
	}
	return unpack(&response)
}

func (h *Hub) Serve(ctx context.Context, listen string, config *tls.Config) error {
	if config.ClientAuth != tls.RequireAndVerifyClientCert {
		return fmt.Errorf("connector listener requires verified mTLS")
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	return h.serveListener(ctx, listener, config)
}

func (h *Hub) serveListener(ctx context.Context, listener net.Listener, config *tls.Config) error {
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(config)), grpc.MaxRecvMsgSize(maxMessage), grpc.MaxSendMsgSize(maxMessage), grpc.MaxConcurrentStreams(256))
	server.RegisterService(&relayService, h)
	stop := context.AfterFunc(ctx, server.Stop)
	defer stop()
	err := server.Serve(listener)
	if errors.Is(err, grpc.ErrServerStopped) {
		return nil
	}
	return err
}

func loadCA(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("invalid certificate authority file")
	}
	return pool, nil
}

func TLS(cert, key, ca, name string, server bool) (*tls.Config, error) {
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return nil, err
	}
	pool, err := loadCA(ca)
	if err != nil {
		return nil, err
	}
	config := &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}
	if server {
		config.ClientAuth = tls.RequireAndVerifyClientCert
		config.ClientCAs = pool
	} else {
		config.RootCAs = pool
		config.ServerName = name
	}
	return config, nil
}

func Connect(ctx context.Context, address string, config *tls.Config, manager *connector.Manager) error {
	if _, _, err := net.SplitHostPort(address); err != nil {
		return fmt.Errorf("connector endpoint must be host:port")
	}
	if strings.TrimSpace(address) == "" {
		return fmt.Errorf("connector endpoint required")
	}
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(credentials.NewTLS(config)), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxMessage), grpc.MaxCallSendMsgSize(maxMessage)))
	if err != nil {
		return err
	}
	defer conn.Close()
	for ctx.Err() == nil {
		err = connectOnce(ctx, conn, manager)
		manager.Disconnect()
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			slog.Warn("Connector link interrupted", "error", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}

func connectOnce(parent context.Context, conn *grpc.ClientConn, manager *connector.Manager) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stream, err := conn.NewStream(ctx, &relayService.Streams[0], connectMethod)
	if err != nil {
		return err
	}
	defer stream.CloseSend()
	setup := time.AfterFunc(6*time.Second, cancel)
	defer setup.Stop()
	if err := send(stream, Message{Type: "hello", Version: 1}); err != nil {
		return err
	}
	ready, err := receive(stream)
	if err != nil {
		return err
	}
	if ready.Type != "ready" || ready.Version != 1 {
		return fmt.Errorf("relay rejected protocol version")
	}
	setup.Stop()
	outgoing := make(chan Message, 64)
	failed := make(chan error, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case message := <-outgoing:
				if err := send(stream, message); err != nil {
					failed <- err
					cancel()
					return
				}
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			discovery, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
			items := manager.Inventory(discovery)
			cancel()
			select {
			case outgoing <- Message{Type: "inventory", Inventory: items}:
			case <-ctx.Done():
				return
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
		}
	}()
	active := make(chan struct{}, 64)
	videos := make(map[string]*connectorVideo)
	defer func() {
		for _, video := range videos {
			video.stop()
		}
	}()
	for {
		message, err := receive(stream)
		if err != nil {
			select {
			case sendError := <-failed:
				return sendError
			default:
				return err
			}
		}
		switch message.Type {
		case "video_start":
			if message.Target == nil || !wire.ValidID(message.VideoID) || len(videos) >= 64 || videos[message.VideoID] != nil {
				return fmt.Errorf("invalid video lease")
			}
			videos[message.VideoID] = startConnectorVideo(ctx, conn, manager, message, outgoing)
		case "video_keepalive":
			if video := videos[message.VideoID]; video != nil {
				video.keepalive()
			}
		case "video_stop":
			if video := videos[message.VideoID]; video != nil {
				video.stop()
				delete(videos, message.VideoID)
			}
		case "stop":
			if message.Stop == nil {
				return fmt.Errorf("missing stop target")
			}
			manager.Stop(message.Stop.Device, message.Stop.Session, message.Stop.Generation)
			for id, video := range videos {
				if video.target.Device == message.Stop.Device && video.target.Session == message.Stop.Session && video.target.Generation <= message.Stop.Generation {
					video.stop()
					delete(videos, id)
				}
			}
		case "execute":
			if message.Command == nil {
				return fmt.Errorf("missing dispatch")
			}
			select {
			case active <- struct{}{}:
			default:
				return fmt.Errorf("dispatch limit exceeded")
			}
			go func(command wire.Dispatch) {
				defer func() { <-active }()
				result := manager.Execute(ctx, command)
				select {
				case outgoing <- Message{Type: "result", Result: &result}:
				case <-ctx.Done():
				}
			}(*message.Command)
		default:
			return fmt.Errorf("unexpected relay message")
		}
	}
}
