package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ziozzang/agentbridge/internal/provider"
)

func TestStreamChatConverseSignsAndParsesResponse(t *testing.T) {
	var path string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Amz-Date") == "" || r.Header.Get("X-Amz-Content-Sha256") == "" {
			t.Fatalf("missing sigv4 headers: %v", r.Header)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"hello"}]}},"stopReason":"end_turn","usage":{"inputTokens":3,"outputTokens":2,"totalTokens":5}}`)
	}))
	defer srv.Close()

	t.Setenv("AWS_ACCESS_KEY_ID", "akid")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	c := New(provider.Config{
		Name: "amazon-bedrock", BaseURL: srv.URL, DefaultModel: "anthropic.claude-sonnet-4-5",
		Extra: map[string]any{"region": "us-east-1"},
	})
	c.HTTPClient = srv.Client()
	chunks, errs := c.StreamChat(context.Background(), []provider.Message{
		{Role: "system", Content: "be brief"},
		{Role: "user", Content: "hi"},
	}, provider.StreamOptions{})
	var text string
	var usage *provider.Usage
	for ch := range chunks {
		text += ch.Text
		if ch.Usage != nil {
			usage = ch.Usage
		}
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if path != "/model/anthropic.claude-sonnet-4-5/converse" {
		t.Fatalf("path = %q", path)
	}
	if text != "hello" || usage == nil || usage.TotalTokens != 5 {
		t.Fatalf("text=%q usage=%#v", text, usage)
	}
	if body["system"] == nil || body["messages"] == nil {
		t.Fatalf("body = %#v", body)
	}
}

func TestTranslateToolRoundTrip(t *testing.T) {
	_, msgs := translateMessages([]provider.Message{
		{Role: "assistant", ToolCalls: []provider.ToolCallMsg{{
			ID: "tool-1",
			Function: provider.ToolCallMsgFunction{
				Name:      "read_file",
				Arguments: `{"path":"README.md"}`,
			},
		}}},
		{Role: "tool", ToolCallID: "tool-1", Content: "ok"},
	})
	if len(msgs) != 2 {
		t.Fatalf("msgs = %#v", msgs)
	}
	if msgs[0].Content[0].ToolUse == nil || msgs[0].Content[0].ToolUse.Name != "read_file" {
		t.Fatalf("assistant tool use = %#v", msgs[0])
	}
	if msgs[1].Role != "user" || msgs[1].Content[0].ToolResult == nil {
		t.Fatalf("tool result = %#v", msgs[1])
	}
}
