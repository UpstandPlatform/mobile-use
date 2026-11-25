package relay

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"mobile-use/internal/store"
	"mobile-use/internal/wire"
)

const (
	serviceName   = "mobileuse.relay.v1.Relay"
	connectMethod = "/" + serviceName + "/Connect"
	forwardMethod = "/" + serviceName + "/Forward"
	publishMethod = "/" + serviceName + "/PublishVideo"
	watchMethod   = "/" + serviceName + "/WatchVideo"
	maxMessage    = 13 << 20
)

type Message struct {
	Type      string           `json:"type"`
	Version   int              `json:"version,omitempty"`
	Inventory []wire.Inventory `json:"inventory,omitempty"`
	Command   *wire.Dispatch   `json:"command,omitempty"`
	Stop      *store.Target    `json:"stop,omitempty"`
	Result    *wire.Result     `json:"result,omitempty"`
	Target    *store.Target    `json:"target,omitempty"`
	VideoID   string           `json:"video_id,omitempty"`
	Frame     *wire.VideoFrame `json:"frame,omitempty"`
}

type Stream interface {
	Context() context.Context
	SendMsg(any) error
	RecvMsg(any) error
}

func send(stream Stream, message Message) error {
	return stream.SendMsg(packet(message))
}

func receive(stream Stream) (Message, error) {
	var envelope wrapperspb.BytesValue
	if err := stream.RecvMsg(&envelope); err != nil {
		return Message{}, err
	}
	return unpack(&envelope)
}

func packet(message Message) *wrapperspb.BytesValue {
	return wrapperspb.Bytes([]byte(wire.Marshal(message)))
}

func unpack(envelope *wrapperspb.BytesValue) (Message, error) {
	var message Message
	if len(envelope.Value) == 0 || len(envelope.Value) > maxMessage {
		return message, status.Error(codes.InvalidArgument, "Invalid relay envelope")
	}
	if err := json.Unmarshal(envelope.Value, &message); err != nil {
		return message, status.Error(codes.InvalidArgument, "Invalid relay message")
	}
	return message, nil
}

type server interface {
	Connect(grpc.ServerStream) error
	Forward(context.Context, *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error)
	PublishVideo(grpc.ServerStream) error
	WatchVideo(grpc.ServerStream) error
}

var relayService = grpc.ServiceDesc{
	ServiceName: serviceName,
	HandlerType: (*server)(nil),
	Streams: []grpc.StreamDesc{
		{StreamName: "Connect", ServerStreams: true, ClientStreams: true, Handler: func(srv any, stream grpc.ServerStream) error { return srv.(server).Connect(stream) }},
		{StreamName: "PublishVideo", ClientStreams: true, Handler: func(srv any, stream grpc.ServerStream) error { return srv.(server).PublishVideo(stream) }},
		{StreamName: "WatchVideo", ServerStreams: true, Handler: func(srv any, stream grpc.ServerStream) error { return srv.(server).WatchVideo(stream) }},
	},
	Methods: []grpc.MethodDesc{{MethodName: "Forward", Handler: func(srv any, ctx context.Context, decode func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		var request wrapperspb.BytesValue
		if err := decode(&request); err != nil {
			return nil, err
		}
		endpoint := srv.(server)
		if interceptor == nil {
			return endpoint.Forward(ctx, &request)
		}
		return interceptor(ctx, &request, &grpc.UnaryServerInfo{Server: srv, FullMethod: forwardMethod}, func(ctx context.Context, payload any) (any, error) {
			value, ok := payload.(*wrapperspb.BytesValue)
			if !ok {
				return nil, fmt.Errorf("invalid relay payload type")
			}
			return endpoint.Forward(ctx, value)
		})
	}}},
}
