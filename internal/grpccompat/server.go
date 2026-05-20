// Package grpccompat exposes a small gRPC API over the configured provider.
package grpccompat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ziozzang/agentbridge/internal/httpcompat"
	"github.com/ziozzang/agentbridge/internal/provider"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

const ServiceName = "agentbridge.v1.AgentService"

// NewServer returns a gRPC server that accepts google.protobuf.Struct request
// messages. This keeps the wire protocol standard gRPC/protobuf without
// requiring generated code in this repository.
func NewServer() *grpc.Server {
	s := grpc.NewServer()
	RegisterAgentServiceServer(s, &server{})
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(s, healthServer)
	return s
}

type AgentServiceServer interface {
	Chat(context.Context, *structpb.Struct) (*structpb.Struct, error)
	A2A(context.Context, *structpb.Struct) (*structpb.Struct, error)
	ChatStream(*structpb.Struct, AgentService_ChatStreamServer) error
	A2AStream(*structpb.Struct, AgentService_A2AStreamServer) error
}

type AgentService_ChatStreamServer interface {
	Send(*structpb.Struct) error
	grpc.ServerStream
}

type AgentService_A2AStreamServer interface {
	Send(*structpb.Struct) error
	grpc.ServerStream
}

func RegisterAgentServiceServer(s grpc.ServiceRegistrar, srv AgentServiceServer) {
	s.RegisterService(&grpc.ServiceDesc{
		ServiceName: ServiceName,
		HandlerType: (*AgentServiceServer)(nil),
		Methods: []grpc.MethodDesc{
			{MethodName: "Chat", Handler: chatHandler},
			{MethodName: "A2A", Handler: a2aHandler},
		},
		Streams: []grpc.StreamDesc{
			{StreamName: "ChatStream", Handler: chatStreamHandler, ServerStreams: true},
			{StreamName: "A2AStream", Handler: a2aStreamHandler, ServerStreams: true},
		},
	}, srv)
}

type server struct{}

func (s *server) Chat(ctx context.Context, req *structpb.Struct) (*structpb.Struct, error) {
	model, messages, err := requestMessages(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	text, usage, stop, err := httpcompat.RunProvider(ctx, model, messages)
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return structFromMap(map[string]any{
		"id":          responseID("grpc-chat"),
		"object":      "chat.completion",
		"model":       model,
		"content":     text,
		"stop_reason": stopReason(stop),
		"usage":       usageMap(usage),
	})
}

func (s *server) A2A(ctx context.Context, req *structpb.Struct) (*structpb.Struct, error) {
	resp, err := s.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	taskID := firstString(req, "taskId")
	if taskID == "" {
		taskID = responseID("task")
	}
	contextID := firstString(req, "contextId")
	if contextID == "" {
		contextID = responseID("ctx")
	}
	text := stringField(resp, "content")
	return structFromMap(map[string]any{
		"taskId":    taskID,
		"contextId": contextID,
		"status": map[string]any{
			"state":     "TASK_STATE_COMPLETED",
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"message": map[string]any{
				"role":      "agent",
				"messageId": responseID("msg"),
				"contextId": contextID,
				"taskId":    taskID,
				"parts":     []any{map[string]any{"kind": "text", "type": "text", "text": text}},
			},
		},
		"artifacts": []any{map[string]any{
			"artifactId": "response",
			"name":       "response",
			"parts":      []any{map[string]any{"kind": "text", "type": "text", "text": text}},
		}},
	})
}

func (s *server) ChatStream(req *structpb.Struct, stream AgentService_ChatStreamServer) error {
	model, messages, err := requestMessages(req)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	chunks, errs, err := httpcompat.StreamProvider(stream.Context(), model, messages)
	if err != nil {
		return status.Error(codes.Unavailable, err.Error())
	}
	var b strings.Builder
	for ch := range chunks {
		if ch.Text == "" {
			continue
		}
		b.WriteString(ch.Text)
		ev, err := structFromMap(map[string]any{
			"type":    "delta",
			"delta":   ch.Text,
			"content": b.String(),
		})
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		if err := stream.Send(ev); err != nil {
			return err
		}
	}
	if err := <-errs; err != nil {
		return status.Error(codes.Unavailable, err.Error())
	}
	ev, err := structFromMap(map[string]any{"type": "completed", "content": b.String()})
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	return stream.Send(ev)
}

func (s *server) A2AStream(req *structpb.Struct, stream AgentService_A2AStreamServer) error {
	taskID := firstString(req, "taskId")
	if taskID == "" {
		taskID = responseID("task")
	}
	contextID := firstString(req, "contextId")
	if contextID == "" {
		contextID = responseID("ctx")
	}
	statusEvent, _ := structFromMap(map[string]any{"statusUpdate": map[string]any{
		"taskId":    taskID,
		"contextId": contextID,
		"status":    map[string]any{"state": "TASK_STATE_WORKING", "timestamp": time.Now().UTC().Format(time.RFC3339Nano)},
	}})
	if err := stream.Send(statusEvent); err != nil {
		return err
	}
	model, messages, err := requestMessages(req)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	chunks, errs, err := httpcompat.StreamProvider(stream.Context(), model, messages)
	if err != nil {
		return status.Error(codes.Unavailable, err.Error())
	}
	var b strings.Builder
	for ch := range chunks {
		if ch.Text == "" {
			continue
		}
		b.WriteString(ch.Text)
		ev, err := structFromMap(map[string]any{"artifactUpdate": map[string]any{
			"taskId":    taskID,
			"contextId": contextID,
			"artifact": map[string]any{
				"artifactId": "response",
				"name":       "response",
				"parts":      []any{map[string]any{"kind": "text", "type": "text", "text": ch.Text}},
			},
		}})
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		if err := stream.Send(ev); err != nil {
			return err
		}
	}
	if err := <-errs; err != nil {
		return status.Error(codes.Unavailable, err.Error())
	}
	done, _ := structFromMap(map[string]any{"statusUpdate": map[string]any{
		"taskId":    taskID,
		"contextId": contextID,
		"status":    map[string]any{"state": "TASK_STATE_COMPLETED", "timestamp": time.Now().UTC().Format(time.RFC3339Nano)},
	}})
	return stream.Send(done)
}

func chatHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(structpb.Struct)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AgentServiceServer).Chat(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/" + ServiceName + "/Chat"}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(AgentServiceServer).Chat(ctx, req.(*structpb.Struct))
	}
	return interceptor(ctx, in, info, handler)
}

func a2aHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(structpb.Struct)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AgentServiceServer).A2A(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/" + ServiceName + "/A2A"}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(AgentServiceServer).A2A(ctx, req.(*structpb.Struct))
	}
	return interceptor(ctx, in, info, handler)
}

func chatStreamHandler(srv any, stream grpc.ServerStream) error {
	in := new(structpb.Struct)
	if err := stream.RecvMsg(in); err != nil {
		return err
	}
	return srv.(AgentServiceServer).ChatStream(in, &chatStreamServer{stream})
}

func a2aStreamHandler(srv any, stream grpc.ServerStream) error {
	in := new(structpb.Struct)
	if err := stream.RecvMsg(in); err != nil {
		return err
	}
	return srv.(AgentServiceServer).A2AStream(in, &a2aStreamServer{stream})
}

type chatStreamServer struct{ grpc.ServerStream }

func (s *chatStreamServer) Send(m *structpb.Struct) error { return s.ServerStream.SendMsg(m) }

type a2aStreamServer struct{ grpc.ServerStream }

func (s *a2aStreamServer) Send(m *structpb.Struct) error { return s.ServerStream.SendMsg(m) }

func requestMessages(req *structpb.Struct) (string, []provider.Message, error) {
	if req == nil {
		return "", nil, errors.New("request is required")
	}
	model := firstString(req, "model")
	var messages []provider.Message
	if list := req.GetFields()["messages"].GetListValue(); list != nil {
		for _, item := range list.Values {
			fields := item.GetStructValue().GetFields()
			role := fields["role"].GetStringValue()
			if role == "" {
				role = "user"
			}
			messages = append(messages, provider.Message{Role: role, Content: fields["content"].GetStringValue()})
		}
	}
	if len(messages) == 0 {
		prompt := firstString(req, "input", "prompt", "content", "text")
		if prompt == "" {
			prompt = a2aText(req)
		}
		if prompt != "" {
			messages = append(messages, provider.Message{Role: "user", Content: prompt})
		}
	}
	if len(messages) == 0 {
		return model, nil, errors.New("messages or input is required")
	}
	return model, messages, nil
}

func a2aText(req *structpb.Struct) string {
	msg := req.GetFields()["message"].GetStructValue()
	if msg == nil {
		return ""
	}
	var b strings.Builder
	parts := msg.GetFields()["parts"].GetListValue()
	if parts == nil {
		return ""
	}
	for _, p := range parts.Values {
		text := p.GetStructValue().GetFields()["text"].GetStringValue()
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(text)
	}
	return b.String()
}

func firstString(req *structpb.Struct, keys ...string) string {
	if req == nil {
		return ""
	}
	for _, key := range keys {
		if v := strings.TrimSpace(req.GetFields()[key].GetStringValue()); v != "" {
			return v
		}
	}
	return ""
}

func stringField(req *structpb.Struct, key string) string {
	if req == nil {
		return ""
	}
	return req.GetFields()[key].GetStringValue()
}

func structFromMap(v map[string]any) (*structpb.Struct, error) {
	out, err := structpb.NewStruct(v)
	if err != nil {
		return nil, fmt.Errorf("encode struct response: %w", err)
	}
	return out, nil
}

func usageMap(u provider.Usage) map[string]any {
	return map[string]any{
		"input_tokens":  u.InputTokens,
		"output_tokens": u.OutputTokens,
		"total_tokens":  u.TotalTokens,
	}
}

func stopReason(stop string) string {
	if stop == "" || stop == "end_turn" {
		return "stop"
	}
	return stop
}

func responseID(prefix string) string {
	return prefix + "-" + fmt.Sprintf("%d", time.Now().UTC().UnixNano())
}
