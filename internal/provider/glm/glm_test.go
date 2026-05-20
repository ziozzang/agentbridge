package glm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestThinkingEnabled(t *testing.T) {
	os.Unsetenv("ACP_GLM_THINKING")
	cases := map[string]bool{
		"glm-5.1": true, "glm-5-turbo": true, "glm-4.7": true, "glm-4.6": true, "glm-4.5-air": true,
		"glm-4.4": false, "glm-3": false, "gpt-4": false,
	}
	for m, want := range cases {
		if got := ThinkingEnabled(m); got != want {
			t.Errorf("Thinking(%s) = %v, want %v", m, got, want)
		}
	}
	t.Setenv("ACP_GLM_THINKING", "false")
	if ThinkingEnabled("glm-5.1") {
		t.Errorf("override false should disable")
	}
	t.Setenv("ACP_GLM_THINKING", "1")
	if !ThinkingEnabled("gpt-4") {
		t.Errorf("override 1 should enable")
	}
}

func TestAvailableModelsBuiltin(t *testing.T) {
	t.Setenv("ACP_GLM_AVAILABLE_MODELS", "")
	got := AvailableModels()
	if len(got) != len(BuiltinAvailableModels) {
		t.Errorf("len mismatch: %d vs %d", len(got), len(BuiltinAvailableModels))
	}
}

func TestAvailableModelsOverride(t *testing.T) {
	t.Setenv("ACP_GLM_AVAILABLE_MODELS", "glm-5.1, custom-foo ,, ")
	got := AvailableModels()
	if len(got) != 2 || got[0].ModelID != "glm-5.1" || got[1].ModelID != "custom-foo" {
		t.Errorf("unexpected: %+v", got)
	}
	if got[0].Description == "" {
		t.Error("expected built-in description for glm-5.1")
	}
	if got[1].Name != "custom-foo" {
		t.Errorf("custom name fallback wrong: %+v", got[1])
	}
}

func TestMaxTokensEnv(t *testing.T) {
	t.Setenv("ACP_GLM_MAX_TOKENS", "")
	if MaxTokensEnv() != DefaultMaxTokens {
		t.Errorf("default wrong")
	}
	t.Setenv("ACP_GLM_MAX_TOKENS", "1024")
	if MaxTokensEnv() != 1024 {
		t.Errorf("override failed")
	}
	t.Setenv("ACP_GLM_MAX_TOKENS", "garbage")
	if MaxTokensEnv() != DefaultMaxTokens {
		t.Errorf("garbage should fall back")
	}
	t.Setenv("ACP_GLM_MAX_TOKENS", "-5")
	if MaxTokensEnv() != DefaultMaxTokens {
		t.Errorf("negative should fall back")
	}
}

func TestStreamChatAssemblesEverything(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer testkey" {
			t.Errorf("missing auth header: %v", r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		// Two text deltas, one reasoning delta, two tool-call delta segments,
		// final usage chunk.
		events := []string{
			`{"choices":[{"index":0,"delta":{"content":"Hel"}}]}`,
			`{"choices":[{"index":0,"delta":{"reasoning_content":"thinking..."}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"lo"}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{\"pa"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"x\"}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":5,"total_tokens":16}}`,
		}
		for _, e := range events {
			fmt.Fprintf(w, "data: %s\n\n", e)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := &Client{APIKey: "testkey", BaseURL: srv.URL, MaxTokens: 100, HTTPClient: srv.Client()}
	chunks, errs := c.StreamChat(context.Background(),
		[]Message{{Role: "user", Content: "hi"}},
		StreamOptions{Model: "glm-5.1"})

	var text, thinking string
	var tc *ToolCall
	var usage *Usage
	var done bool
	var stop string
	for ch := range chunks {
		text += ch.Text
		thinking += ch.Thinking
		if ch.ToolCall != nil {
			tc = ch.ToolCall
		}
		if ch.Usage != nil {
			usage = ch.Usage
		}
		if ch.Done {
			done = true
			stop = ch.StopReason
		}
	}
	if err := <-errs; err != nil {
		t.Fatalf("err: %v", err)
	}
	if text != "Hello" {
		t.Errorf("text = %q", text)
	}
	if thinking != "thinking..." {
		t.Errorf("thinking = %q", thinking)
	}
	if tc == nil || tc.ID != "call_1" || tc.Name != "read_file" || tc.Arguments != `{"path":"x"}` {
		t.Errorf("toolcall = %+v", tc)
	}
	if usage == nil || usage.InputTokens != 11 || usage.TotalTokens != 16 {
		t.Errorf("usage = %+v", usage)
	}
	if !done || stop != "tool_calls" {
		t.Errorf("done=%v stop=%q", done, stop)
	}
}

func TestStreamChatHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":"unauthorized"}`)
	}))
	defer srv.Close()
	c := &Client{APIKey: "k", BaseURL: srv.URL, MaxTokens: 10, HTTPClient: srv.Client()}
	chunks, errs := c.StreamChat(context.Background(), nil, StreamOptions{Model: "glm-5.1"})
	for range chunks {
	}
	err := <-errs
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 error, got %v", err)
	}
}
