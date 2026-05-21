package google

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ziozzang/agentbridge/internal/provider"
)

func TestStreamChatGeminiWithCachedContent(t *testing.T) {
	var cacheBody map[string]any
	var streamBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "key" {
			t.Fatalf("auth = %q", r.Header.Get("x-goog-api-key"))
		}
		switch {
		case r.URL.Path == "/v1beta/cachedContents":
			_ = json.NewDecoder(r.Body).Decode(&cacheBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"cachedContents/system-cache-1","expireTime":"2099-01-01T00:00:00Z"}`))
		case strings.Contains(r.URL.Path, ":streamGenerateContent"):
			_ = json.NewDecoder(r.Body).Decode(&streamBody)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintln(w, `data: {"candidates":[{"content":{"parts":[{"text":"Hi"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12,"cachedContentTokenCount":7}}`)
			fmt.Fprintln(w)
		default:
			t.Fatalf("unexpected path: %s", r.URL.String())
		}
	}))
	defer srv.Close()
	googleCache = syncMapForTest()

	c := New(provider.Config{
		Name: "google", BaseURL: srv.URL, APIKey: "key",
		Extra: map[string]any{"cache_retention": "short"},
	})
	c.HTTPClient = srv.Client()
	chunks, errs := c.StreamChat(context.Background(), []provider.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "hello"},
	}, provider.StreamOptions{Model: "gemini-2.5-pro", SessionID: "s1"})
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
	if text != "Hi" {
		t.Fatalf("text = %q", text)
	}
	if cacheBody["ttl"] != "300s" {
		t.Fatalf("cache body = %#v", cacheBody)
	}
	if streamBody["cachedContent"] != "cachedContents/system-cache-1" {
		t.Fatalf("stream body = %#v", streamBody)
	}
	if _, ok := streamBody["systemInstruction"]; ok {
		t.Fatalf("systemInstruction should be omitted when cachedContent is used: %#v", streamBody)
	}
	if usage == nil || usage.CachedReadTokens != 7 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestTranslateMessagesMapsToolResponseToFunctionName(t *testing.T) {
	_, contents := translateMessages([]provider.Message{
		{
			Role: "assistant",
			ToolCalls: []provider.ToolCallMsg{{
				ID:   "call_1",
				Type: "function",
				Function: provider.ToolCallMsgFunction{
					Name:      "list_files",
					Arguments: `{"path":"."}`,
				},
			}},
		},
		{Role: "tool", ToolCallID: "call_1", Content: "README.md"},
	})
	if len(contents) != 2 {
		t.Fatalf("contents = %#v", contents)
	}
	toolMsg := contents[1]
	if toolMsg.Role != "user" {
		t.Fatalf("tool response role = %q", toolMsg.Role)
	}
	got := toolMsg.Parts[0].FunctionResponse
	if got == nil || got.Name != "list_files" {
		t.Fatalf("function response = %#v", got)
	}
}

func TestStreamURLUsesVertexPath(t *testing.T) {
	c := New(provider.Config{
		BaseURL: "https://aiplatform.googleapis.com",
		Extra: map[string]any{
			"vertex_project_id": "proj",
			"vertex_region":     "us-central1",
		},
	})
	got := c.streamURL("gemini-3-flash")
	want := "https://aiplatform.googleapis.com/v1/projects/proj/locations/us-central1/publishers/google/models/gemini-3-flash:streamGenerateContent?alt=sse"
	if got != want {
		t.Fatalf("url = %q want %q", got, want)
	}
}

func syncMapForTest() sync.Map {
	return sync.Map{}
}
