package grpccompat

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestChatUnaryAndStream(t *testing.T) {
	withMockProvider(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer()
	go func() { _ = srv.Serve(ln) }()
	defer srv.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	req, err := structpb.NewStruct(map[string]any{"input": "hi", "model": "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	var resp structpb.Struct
	if err := conn.Invoke(ctx, "/"+ServiceName+"/Chat", req, &resp); err != nil {
		t.Fatal(err)
	}
	if got := resp.GetFields()["content"].GetStringValue(); got != "OK" {
		t.Fatalf("content=%q", got)
	}

	desc := &grpc.StreamDesc{ServerStreams: true}
	stream, err := conn.NewStream(ctx, desc, "/"+ServiceName+"/ChatStream")
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.SendMsg(req); err != nil {
		t.Fatal(err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	var sawDelta, sawDone bool
	for {
		var ev structpb.Struct
		err := stream.RecvMsg(&ev)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch ev.GetFields()["type"].GetStringValue() {
		case "delta":
			sawDelta = true
		case "completed":
			sawDone = true
		}
	}
	if !sawDelta || !sawDone {
		t.Fatalf("stream sawDelta=%v sawDone=%v", sawDelta, sawDone)
	}
}

func TestRealGLMSmoke(t *testing.T) {
	if os.Getenv("ACP_GRPC_REAL_SMOKE") != "1" {
		t.Skip("set ACP_GRPC_REAL_SMOKE=1 to run a real provider smoke test")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer()
	go func() { _ = srv.Serve(ln) }()
	defer srv.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	req, err := structpb.NewStruct(map[string]any{"input": "Reply with exactly: OK", "model": "glm-5.1"})
	if err != nil {
		t.Fatal(err)
	}
	var resp structpb.Struct
	if err := conn.Invoke(ctx, "/"+ServiceName+"/Chat", req, &resp); err != nil {
		t.Fatal(err)
	}
	if got := resp.GetFields()["content"].GetStringValue(); got != "OK" {
		t.Fatalf("content=%q", got)
	}
}

func withMockProvider(t *testing.T) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected upstream path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(upstream.Close)

	cfg := filepath.Join(t.TempDir(), "providers.yaml")
	if err := os.WriteFile(cfg, []byte(`providers:
  test-http:
    kind: openai-chat
    base_url: `+upstream.URL+`
    api_key: test-key
    default_model: test-model
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACP_HARNESS_PROVIDERS_FILE", cfg)
	t.Setenv("ACP_HARNESS_PROVIDER", "test-http")
	t.Setenv("ACP_HARNESS_MODEL", "")
	t.Setenv("ACP_HARNESS_API_KEY", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing"))
}
