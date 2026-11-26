package relay

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"mobile-use/internal/store"
	"mobile-use/internal/wire"
)

// recordingStream flags any SendMsg that starts after the handler has exited.
type recordingStream struct {
	exited  atomic.Bool
	late    atomic.Int32
	sent    atomic.Int32
	blocked chan struct{}
}

func (s *recordingStream) Context() context.Context { return context.Background() }
func (s *recordingStream) RecvMsg(any) error        { return errors.New("unused") }
func (s *recordingStream) SendMsg(any) error {
	if s.exited.Load() {
		s.late.Add(1)
	}
	if s.blocked != nil {
		<-s.blocked
	}
	if s.exited.Load() {
		s.late.Add(1)
	}
	s.sent.Add(1)
	return nil
}

func newTestConnection(stream Stream) *connection {
	return &connection{stream: stream, pending: make(map[string]chan wire.Result), viewers: make(map[string]*videoWatch), closed: make(chan struct{})}
}

func TestConnectionRejectsSendsAfterShutdown(t *testing.T) {
	stream := &recordingStream{}
	conn := newTestConnection(stream)
	if err := conn.send(Message{Type: "ready"}); err != nil {
		t.Fatal(err)
	}
	conn.shutdown()
	stream.exited.Store(true)
	if err := conn.send(Message{Type: "video_stop", VideoID: wire.NewID()}); !errors.Is(err, errConnectionClosed) {
		t.Fatalf("send after shutdown: %v", err)
	}
	if stream.late.Load() != 0 || stream.sent.Load() != 1 {
		t.Fatalf("stream used after shutdown: late=%d sent=%d", stream.late.Load(), stream.sent.Load())
	}
	target := store.Target{Workspace: "w", Connector: "c", Device: "d", Session: "s", Generation: 1}
	if _, err := conn.execute(t.Context(), wire.Dispatch{Operation: wire.Operation{ID: wire.NewID()}}); !errors.Is(err, wire.ErrOffline) {
		t.Fatalf("execute on a closed connection should be offline, got %v", err)
	}
	if err := conn.watch(t.Context(), target, func(wire.VideoFrame) error { return nil }); !errors.Is(err, wire.ErrOffline) {
		t.Fatalf("watch on a closed connection should be offline, got %v", err)
	}
	if stream.late.Load() != 0 {
		t.Fatalf("stream used after shutdown by execute/watch: %d", stream.late.Load())
	}
}

func TestConnectionShutdownWaitsForInFlightSend(t *testing.T) {
	stream := &recordingStream{blocked: make(chan struct{})}
	conn := newTestConnection(stream)
	var senders sync.WaitGroup
	senders.Add(1)
	go func() { defer senders.Done(); _ = conn.send(Message{Type: "video_keepalive", VideoID: wire.NewID()}) }()
	for conn.sendMutex.TryLock() {
		conn.sendMutex.Unlock()
		time.Sleep(time.Millisecond)
	}
	done := make(chan struct{})
	go func() {
		conn.shutdown()
		stream.exited.Store(true)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("shutdown returned while SendMsg was still in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(stream.blocked)
	<-done
	for range 16 {
		senders.Add(1)
		go func() { defer senders.Done(); _ = conn.send(Message{Type: "video_stop", VideoID: wire.NewID()}) }()
	}
	senders.Wait()
	if stream.late.Load() != 0 || stream.sent.Load() != 1 {
		t.Fatalf("stream used after shutdown: late=%d sent=%d", stream.late.Load(), stream.sent.Load())
	}
}

func TestForwardErrorOnlyReportsOfflineWhenNothingRan(t *testing.T) {
	cases := []struct {
		err  error
		want *wire.Fault
	}{
		{status.Error(codes.Unavailable, disconnectedMessage), wire.ErrOffline},
		{status.Error(codes.Unavailable, "connection error: transport is closing"), wire.ErrUnknown},
		{status.Error(codes.FailedPrecondition, "Session is inactive"), wire.ErrExpired},
		{status.Error(codes.Aborted, "Device outcome is unknown"), wire.ErrUnknown},
		{context.DeadlineExceeded, wire.ErrUnknown},
	}
	for _, c := range cases {
		if got := forwardError(c.err); !errors.Is(got, c.want) {
			t.Errorf("forwardError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
