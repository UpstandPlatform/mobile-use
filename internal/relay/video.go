package relay

import (
	"context"
	"errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"io"
	"mobile-use/internal/connector"
	"mobile-use/internal/store"
	"mobile-use/internal/wire"
	"net"
	"sync"
	"time"
)

const videoLease = 10 * time.Second

type videoWatch struct {
	ctx        context.Context
	cancel     context.CancelFunc
	frames     chan wire.VideoFrame
	publishing bool
}

func (c *connection) video(id string) *videoWatch {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.viewers[id]
}

func (h *Hub) Watch(ctx context.Context, target store.Target, publish func(wire.VideoFrame) error) error {
	if target.Workspace == h.LocalWorkspace && target.Connector == "local" && h.Local != nil {
		return h.Local.Video(ctx, target.Device, target.Session, target.Generation, publish)
	}
	if conn := h.conn(target); conn != nil {
		return conn.watch(ctx, target, publish)
	}
	if h.PeerTLS == nil {
		return wire.ErrOffline
	}
	address, err := h.Store.Route(ctx, target.Workspace, target.Connector, time.Now())
	if err != nil {
		return err
	}
	if address == "" || address == h.RelayAddress {
		return wire.ErrOffline
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		return wire.ErrOffline
	}
	cc, err := grpc.NewClient(address, grpc.WithTransportCredentials(credentials.NewTLS(h.PeerTLS.Clone())), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxMessage)))
	if err != nil {
		return err
	}
	defer cc.Close()
	stream, err := cc.NewStream(ctx, &relayService.Streams[2], watchMethod)
	if err != nil {
		return err
	}
	if err := send(stream, Message{Type: "watch", Target: &target}); err != nil {
		return err
	}
	if err := stream.CloseSend(); err != nil {
		return err
	}
	for {
		message, err := receive(stream)
		if err != nil {
			return err
		}
		if !validVideo(message) {
			return wire.ErrUnsupported
		}
		if err := publish(*message.Frame); err != nil {
			return err
		}
	}
}

func (c *connection) watch(parent context.Context, target store.Target, publish func(wire.VideoFrame) error) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	id := wire.NewID()
	watch := &videoWatch{ctx: ctx, cancel: cancel, frames: make(chan wire.VideoFrame, 2)}
	c.mu.Lock()
	if len(c.viewers) >= 64 {
		c.mu.Unlock()
		return wire.Error("VIDEO_LIMIT", "The connector has reached its video viewer limit.", 429)
	}
	c.viewers[id] = watch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.viewers, id)
		c.mu.Unlock()
		// Slot expiry at the connector still terminates capture if the control stream is gone.
		_ = c.send(Message{Type: "video_stop", VideoID: id})
	}()
	if err := c.send(Message{Type: "video_start", VideoID: id, Target: &target}); err != nil {
		return err
	}
	ticker := time.NewTicker(2500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.closed:
			return wire.ErrOffline
		case frame := <-watch.frames:
			if err := publish(frame); err != nil {
				return err
			}
		case <-ticker.C:
			if err := c.send(Message{Type: "video_keepalive", VideoID: id}); err != nil {
				return err
			}
		}
	}
}

func (h *Hub) PublishVideo(stream grpc.ServerStream) error {
	name := certificateName(stream.Context())
	workspace, ok := h.ConnectorNames[name]
	if !ok {
		return status.Error(codes.PermissionDenied, "Connector certificate is not enrolled")
	}
	opening, err := receive(stream)
	if err != nil {
		return err
	}
	if opening.Type != "publish" || !wire.ValidID(opening.VideoID) {
		return status.Error(codes.InvalidArgument, "Invalid video publication")
	}
	conn := h.conn(store.Target{Workspace: workspace, Connector: name})
	if conn == nil {
		return status.Error(codes.Unavailable, "Connector is disconnected")
	}
	conn.mu.Lock()
	watch := conn.viewers[opening.VideoID]
	if watch == nil || watch.publishing {
		conn.mu.Unlock()
		return status.Error(codes.PermissionDenied, "There is no matching viewer")
	}
	watch.publishing = true
	conn.mu.Unlock()
	defer watch.cancel()
	for {
		message, err := receive(stream)
		if errors.Is(err, io.EOF) {
			return send(stream, Message{Type: "ready"})
		}
		if err != nil {
			return err
		}
		if !validVideo(message) {
			return status.Error(codes.InvalidArgument, "Invalid video frame")
		}
		select {
		case watch.frames <- *message.Frame:
		case <-watch.ctx.Done():
			return status.Error(codes.Canceled, "Viewer closed")
		case <-conn.closed:
			return status.Error(codes.Canceled, "Connector closed")
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

func (h *Hub) WatchVideo(stream grpc.ServerStream) error {
	if !h.PeerNames[certificateName(stream.Context())] {
		return status.Error(codes.PermissionDenied, "Untrusted relay peer")
	}
	message, err := receive(stream)
	if err != nil {
		return err
	}
	if message.Type != "watch" || message.Target == nil {
		return status.Error(codes.InvalidArgument, "Missing video target")
	}
	ctx := stream.Context()
	target := *message.Target
	if err := h.Store.Confirm(ctx, target, time.Now()); err != nil {
		return status.Error(codes.FailedPrecondition, "Session is inactive")
	}
	conn := h.conn(target)
	if conn == nil {
		return status.Error(codes.Unavailable, "Connector is disconnected")
	}
	return conn.watch(ctx, target, func(frame wire.VideoFrame) error { return send(stream, Message{Type: "frame", Frame: &frame}) })
}

func validVideo(message Message) bool {
	frame := message.Frame
	return message.Type == "frame" && frame != nil && len(frame.Data) > 0 && len(frame.Data) <= (2<<20)+(64<<10) && frame.DurationMicros > 0 && frame.DurationMicros <= 250000
}

type connectorVideo struct {
	cancel   context.CancelFunc
	mu       sync.Mutex
	deadline *time.Timer
	target   store.Target
}

func (v *connectorVideo) keepalive() { v.mu.Lock(); defer v.mu.Unlock(); v.deadline.Reset(videoLease) }
func (v *connectorVideo) stop()      { v.mu.Lock(); defer v.mu.Unlock(); v.deadline.Stop(); v.cancel() }

func startConnectorVideo(parent context.Context, conn *grpc.ClientConn, manager *connector.Manager, message Message, outgoing chan<- Message) *connectorVideo {
	ctx, cancel := context.WithCancel(parent)
	video := &connectorVideo{cancel: cancel, deadline: time.AfterFunc(videoLease, cancel), target: *message.Target}
	go func() {
		defer video.stop()
		defer func() {
			select {
			case outgoing <- Message{Type: "video_finished", VideoID: message.VideoID}:
			case <-parent.Done():
			}
		}()
		stream, err := conn.NewStream(ctx, &relayService.Streams[1], publishMethod)
		if err != nil {
			return
		}
		defer stream.CloseSend()
		if err := send(stream, Message{Type: "publish", VideoID: message.VideoID}); err != nil {
			return
		}
		_ = manager.Video(ctx, message.Target.Device, message.Target.Session, message.Target.Generation, func(frame wire.VideoFrame) error { return send(stream, Message{Type: "frame", Frame: &frame}) })
	}()
	return video
}
