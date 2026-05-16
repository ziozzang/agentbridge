package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ziozzang/glm-acp/internal/provider"
)

func TestStreamChatAnthropicEnd2End(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "ant-key" {
			t.Errorf("auth header = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			t.Errorf("missing anthropic-version header")
		}
		var body messagesRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.System != "be helpful" {
			t.Errorf("system = %q", body.System)
		}
		if len(body.Messages) != 1 || body.Messages[0].Role != "user" {
			t.Errorf("messages = %+v", body.Messages)
		}
		if len(body.Tools) == 0 {
			t.Errorf("expected tools")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// Anthropic stream: content_block_start (text), text_delta, content_block_stop,
		// content_block_start (tool_use), input_json_delta x2, content_block_stop,
		// message_delta (stop_reason, usage), message_stop.
		events := []string{
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","id":"","name":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"!"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"pa"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"th\":\"x\"}"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":3,"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		}
		for _, e := range events {
			fmt.Fprintf(w, "data: %s\n\n", e)
		}
	}))
	defer srv.Close()

	c := New(provider.Config{
		Name: "ant-test", BaseURL: srv.URL, APIKey: "ant-key",
		Models: []provider.ModelInfo{{ModelID: "claude-test"}},
	})
	c.HTTPClient = srv.Client()
	chunks, errs := c.StreamChat(context.Background(),
		[]provider.Message{
			{Role: "system", Content: "be helpful"},
			{Role: "user", Content: "hello"},
		},
		provider.StreamOptions{Model: "claude-test"})
	var text string
	var tcs []provider.ToolCall
	var stop string
	for ch := range chunks {
		text += ch.Text
		if ch.ToolCall != nil {
			tcs = append(tcs, *ch.ToolCall)
		}
		if ch.Done {
			stop = ch.StopReason
		}
	}
	if err := <-errs; err != nil {
		t.Fatalf("err: %v", err)
	}
	if text != "Hi!" {
		t.Errorf("text=%q", text)
	}
	if len(tcs) != 1 || tcs[0].ID != "toolu_1" || tcs[0].Name != "read_file" || tcs[0].Arguments != `{"path":"x"}` {
		t.Errorf("tool calls = %+v", tcs)
	}
	if stop != "tool_calls" {
		t.Errorf("stop=%q", stop)
	}
}

func TestAnthropicContextOverflow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long"}}`))
	}))
	defer srv.Close()
	c := New(provider.Config{BaseURL: srv.URL, APIKey: "k"})
	c.HTTPClient = srv.Client()
	chunks, errs := c.StreamChat(context.Background(),
		[]provider.Message{{Role: "user", Content: "hi"}},
		provider.StreamOptions{Model: "m"})
	for range chunks {
	}
	err := <-errs
	if !provider.IsContextOverflow(err) {
		t.Fatalf("expected ContextOverflowError, got %T %v", err, err)
	}
}

func TestTranslateAssistantToolCallsRoundTrip(t *testing.T) {
	in := []provider.Message{
		{Role: "system", Content: "S"},
		{Role: "user", Content: "U"},
		{Role: "assistant", ToolCalls: []provider.ToolCallMsg{{
			ID: "id1", Type: "function",
			Function: provider.ToolCallMsgFunction{Name: "f", Arguments: `{"x":1}`},
		}}},
		{Role: "tool", ToolCallID: "id1", Content: "ok"},
	}
	sys, out := translateMessages(in)
	if sys != "S" {
		t.Errorf("system: %q", sys)
	}
	if len(out) != 3 {
		t.Fatalf("messages count: %d", len(out))
	}
	if out[1].Role != "assistant" || out[1].Content[0].Type != "tool_use" || out[1].Content[0].ID != "id1" {
		t.Errorf("assistant tool_use missing: %+v", out[1])
	}
	if out[2].Role != "user" || out[2].Content[0].Type != "tool_result" || out[2].Content[0].ToolUseID != "id1" || out[2].Content[0].Content != "ok" {
		t.Errorf("tool result not translated: %+v", out[2])
	}
}
