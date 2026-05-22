package openaichat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	if usage != nil {
		t.Errorf("tool-call streams should return as soon as tool_calls finishes; usage=%+v", usage)
	}
}

func TestStreamChatFlushesToolCallsBeforeUpstreamClose(t *testing.T) {
	upstreamDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamDone)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n",
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"tc1","function":{"name":"client__run_command","arguments":"{\"command\":\"ps\"}"}}]}}]}`)
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := New(provider.Config{BaseURL: srv.URL, APIKey: "k"})
	c.HTTPClient = srv.Client()
	chunks, errs := c.StreamChat(context.Background(),
		[]provider.Message{{Role: "user", Content: "hi"}}, provider.StreamOptions{Model: "m"})
	_, _, tcs, stop, _ := collect(t, chunks, errs)
	if len(tcs) != 1 || tcs[0].Name != "client__run_command" {
		t.Fatalf("tool calls = %+v", tcs)
	}
	if stop != "tool_calls" {
		t.Fatalf("stop = %q", stop)
	}
	select {
	case <-upstreamDone:
	case <-time.After(time.Second):
		t.Fatalf("upstream request was not closed")
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

func TestAvailableModelsFetchesWildcardModelList(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-oss:120b","owned_by":"ollama"}]}`))
	}))
	defer srv.Close()
	c := New(provider.Config{
		Name: "ollama-cloud", BaseURL: srv.URL, APIKey: "k",
		DefaultModel: "gpt-oss:120b",
		Models:       []provider.ModelInfo{{ModelID: "*"}},
	})
	c.HTTPClient = srv.Client()
	models := c.AvailableModels()
	if sawAuth != "Bearer k" {
		t.Fatalf("auth = %q", sawAuth)
	}
	if len(models) != 1 || models[0].ModelID != "gpt-oss:120b" || models[0].Provider != "ollama-cloud" {
		t.Fatalf("models = %+v", models)
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

func TestKimiReasoningEffortAndThinkingExtraBody(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := New(provider.Config{
		Name: "kimi-coding", BaseURL: srv.URL, APIKey: "k",
		Extra: map[string]any{"reasoning_effort": "high"},
	})
	c.HTTPClient = srv.Client()
	chunks, errs := c.StreamChat(context.Background(), nil, provider.StreamOptions{Model: "kimi-k2.6"})
	for range chunks {
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if body["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %#v", body["reasoning_effort"])
	}
	extraBody, _ := body["extra_body"].(map[string]any)
	thinking, _ := extraBody["thinking"].(map[string]any)
	if thinking["type"] != "enabled" {
		t.Fatalf("extra_body.thinking = %#v", extraBody["thinking"])
	}
}

func TestDeepSeekReasoningOnlyForThinkingModels(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := New(provider.Config{
		Name: "deepseek", BaseURL: srv.URL, APIKey: "k",
		Extra: map[string]any{"reasoning_effort": "xhigh"},
	})
	c.HTTPClient = srv.Client()
	chunks, errs := c.StreamChat(context.Background(), nil, provider.StreamOptions{Model: "deepseek-chat"})
	for range chunks {
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	chunks, errs = c.StreamChat(context.Background(), nil, provider.StreamOptions{Model: "deepseek-reasoner"})
	for range chunks {
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if _, ok := bodies[0]["reasoning_effort"]; ok {
		t.Fatalf("deepseek-chat should not get reasoning_effort: %#v", bodies[0])
	}
	if bodies[1]["reasoning_effort"] != "max" {
		t.Fatalf("deepseek-reasoner reasoning_effort = %#v", bodies[1]["reasoning_effort"])
	}
	extraBody, _ := bodies[1]["extra_body"].(map[string]any)
	if extraBody["thinking"] == nil {
		t.Fatalf("deepseek-reasoner missing extra_body.thinking: %#v", bodies[1])
	}
}

func TestTogetherReasoningUsesReasoningObject(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := New(provider.Config{
		Name: "together", BaseURL: srv.URL, APIKey: "k",
		Extra: map[string]any{"reasoning_effort": "high", "thinking_format": "together"},
	})
	c.HTTPClient = srv.Client()
	chunks, errs := c.StreamChat(context.Background(), nil, provider.StreamOptions{Model: "Qwen/Qwen3-Coder-480B-A35B-Instruct"})
	for range chunks {
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Fatalf("together should not get reasoning_effort: %#v", body)
	}
	reasoning, _ := body["reasoning"].(map[string]any)
	if reasoning["enabled"] != true {
		t.Fatalf("reasoning = %#v", reasoning)
	}
}

func TestPromptCacheControlMarksSystemAndLastThreeChatMessages(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := New(provider.Config{
		Name: "alibaba", BaseURL: srv.URL, APIKey: "k",
	})
	c.HTTPClient = srv.Client()
	chunks, errs := c.StreamChat(context.Background(), []provider.Message{
		{Role: "system", Content: "S"},
		{Role: "user", Content: "U1"},
		{Role: "assistant", Content: "A1"},
		{Role: "user", Content: "U2"},
		{Role: "assistant", Content: "A2"},
	}, provider.StreamOptions{Model: "qwen3.6-plus"})
	for range chunks {
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	messages, _ := body["messages"].([]any)
	marked := 0
	for _, raw := range messages {
		msg, _ := raw.(map[string]any)
		content, _ := msg["content"].([]any)
		if len(content) == 0 {
			continue
		}
		part, _ := content[len(content)-1].(map[string]any)
		if cache, ok := part["cache_control"].(map[string]any); ok && cache["type"] == "ephemeral" {
			marked++
		}
	}
	if marked != 4 {
		t.Fatalf("cache_control marked messages = %d body=%#v", marked, body)
	}
}

func TestOpenRouterResponseCacheHeaders(t *testing.T) {
	var headers http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := New(provider.Config{
		Name: "openrouter", BaseURL: srv.URL, APIKey: "k",
		Extra: map[string]any{
			"response_cache":       true,
			"response_cache_ttl":   90000,
			"response_cache_clear": true,
		},
	})
	c.HTTPClient = srv.Client()
	chunks, errs := c.StreamChat(context.Background(), nil, provider.StreamOptions{Model: "anthropic/claude-sonnet-4.5"})
	for range chunks {
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if headers.Get("X-OpenRouter-Cache") != "true" {
		t.Fatalf("X-OpenRouter-Cache = %q", headers.Get("X-OpenRouter-Cache"))
	}
	if headers.Get("X-OpenRouter-Cache-TTL") != "86400" {
		t.Fatalf("X-OpenRouter-Cache-TTL = %q", headers.Get("X-OpenRouter-Cache-TTL"))
	}
	if headers.Get("X-OpenRouter-Cache-Clear") != "true" {
		t.Fatalf("X-OpenRouter-Cache-Clear = %q", headers.Get("X-OpenRouter-Cache-Clear"))
	}
}
