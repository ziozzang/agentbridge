package openaichat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ziozzang/agentbridge/internal/provider"
)

func collect(t *testing.T, chunks <-chan provider.Chunk, errs <-chan error) (text, thinking string, tcs []provider.ToolCall, stop string, usage *provider.Usage) {
	t.Helper()
	for ch := range chunks {
		text += ch.Text
		thinking += ch.Thinking
		if ch.ToolCall != nil {
			tcs = append(tcs, *ch.ToolCall)
		}
		if ch.Usage != nil {
			usage = ch.Usage
		}
		if ch.Done {
			stop = ch.StopReason
		}
	}
	if err := <-errs; err != nil {
		t.Fatalf("stream error: %v", err)
	}
	return
}

func TestStreamChatAggregatesEverything(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer key" {
			t.Errorf("auth header = %q", got)
		}
		// Verify the request body has model & messages.
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "test-model" {
			t.Errorf("model = %v", body["model"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"choices":[{"index":0,"delta":{"content":"Hel"}}]}`,
			`{"choices":[{"index":0,"delta":{"reasoning_content":"think"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"lo"}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"tc1","function":{"name":"read_file","arguments":"{\"p"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ath\":\"x\"}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`,
		}
		for _, e := range events {
			fmt.Fprintf(w, "data: %s\n\n", e)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := New(provider.Config{
		Name: "test", BaseURL: srv.URL, APIKey: "key", MaxTokens: 100,
		Models: []provider.ModelInfo{{ModelID: "test-model"}},
	})
	c.HTTPClient = srv.Client()
	chunks, errs := c.StreamChat(context.Background(),
		[]provider.Message{{Role: "user", Content: "hi"}},
		provider.StreamOptions{Model: "test-model"})
	text, thinking, tcs, stop, usage := collect(t, chunks, errs)
	if text != "Hello" {
		t.Errorf("text=%q", text)
	}
	if thinking != "think" {
		t.Errorf("thinking=%q", thinking)
	}
	if len(tcs) != 1 || tcs[0].ID != "tc1" || tcs[0].Name != "read_file" || tcs[0].Arguments != `{"path":"x"}` {
		t.Errorf("tool calls = %+v", tcs)
	}
	if stop != "tool_calls" {
		t.Errorf("stop=%q", stop)
	}
	if usage == nil || usage.TotalTokens != 8 {
		t.Errorf("usage=%+v", usage)
	}
}

func TestStreamChatContextOverflowError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"code":"1261","message":"context length exceeded"}}`))
	}))
	defer srv.Close()
	c := New(provider.Config{BaseURL: srv.URL, APIKey: "k"})
	c.HTTPClient = srv.Client()
	chunks, errs := c.StreamChat(context.Background(),
		[]provider.Message{{Role: "user", Content: "hi"}}, provider.StreamOptions{Model: "m"})
	for range chunks {
	}
	err := <-errs
	if !provider.IsContextOverflow(err) {
		t.Fatalf("expected ContextOverflowError, got %T %v", err, err)
	}
}

func TestStreamChatPartialToolCallNotFlushed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Only emits arguments fragment — no id and no name.
		fmt.Fprintf(w, "data: %s\n\n",
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"foo\":1}"}}]}}]}`)
		fmt.Fprintf(w, "data: %s\n\n",
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := New(provider.Config{BaseURL: srv.URL, APIKey: "k"})
	c.HTTPClient = srv.Client()
	chunks, errs := c.StreamChat(context.Background(),
		[]provider.Message{{Role: "user", Content: "hi"}}, provider.StreamOptions{Model: "m"})
	_, _, tcs, _, _ := collect(t, chunks, errs)
	if len(tcs) != 0 {
		t.Errorf("partial tool call should not be flushed; got %+v", tcs)
	}
}

func TestAuthHeaderOverride(t *testing.T) {
	var sawHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawHeader = r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := New(provider.Config{BaseURL: srv.URL, APIKey: "abc", AuthHeader: "X-Api-Key"})
	c.HTTPClient = srv.Client()
	chunks, errs := c.StreamChat(context.Background(), nil, provider.StreamOptions{Model: "m"})
	for range chunks {
	}
	<-errs
	if sawHeader != "abc" {
		t.Errorf("wanted X-Api-Key=abc, got %q", sawHeader)
	}
}

func TestExtraHeadersPropagated(t *testing.T) {
	var sawReferer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawReferer = r.Header.Get("HTTP-Referer")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := New(provider.Config{
		BaseURL: srv.URL, APIKey: "k",
		Headers: map[string]string{"HTTP-Referer": "https://example.com"},
	})
	c.HTTPClient = srv.Client()
	chunks, errs := c.StreamChat(context.Background(), nil, provider.StreamOptions{})
	for range chunks {
	}
	<-errs
	if sawReferer != "https://example.com" {
		t.Errorf("missing extra header: %q", sawReferer)
	}
}

func TestThinkingEnabledWhenConfigured(t *testing.T) {
	var sawThinking bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["thinking"]; ok {
			sawThinking = true
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := New(provider.Config{BaseURL: srv.URL, APIKey: "k", Thinking: "enabled"})
	c.HTTPClient = srv.Client()
	chunks, errs := c.StreamChat(context.Background(), nil, provider.StreamOptions{Model: "m"})
	for range chunks {
	}
	<-errs
	if !sawThinking {
		t.Errorf("expected thinking flag in request body")
	}
}

func TestRequestDefaultsInjected(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := New(provider.Config{
		BaseURL: srv.URL, APIKey: "k",
		Extra: map[string]any{"request_defaults": map[string]any{
			"reasoning":      "off",
			"reasoning_cost": 1234,
		}},
	})
	c.HTTPClient = srv.Client()
	chunks, errs := c.StreamChat(context.Background(), nil, provider.StreamOptions{Model: "m"})
	for range chunks {
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if body["reasoning"] != "off" || body["reasoning_cost"].(float64) != 1234 {
		t.Fatalf("defaults not injected: %#v", body)
	}
}
