package glm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestStreamChatDoesNotFlushPartialToolCall(t *testing.T) {
	// Stream a tool_call delta that only contains arguments — no id, no name.
	// The client must silently drop the partial entry. Mirrors the TS test
	// "streamChat does not flush partial tool calls (missing id or name)".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n",
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"foo\":1}"}}]}}]}`)
		fmt.Fprintf(w, "data: %s\n\n",
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := &Client{APIKey: "k", BaseURL: srv.URL, MaxTokens: 50, HTTPClient: srv.Client()}
	chunks, errs := c.StreamChat(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, StreamOptions{Model: "glm-5.1"})
	var tc *ToolCall
	for ch := range chunks {
		if ch.ToolCall != nil {
			tc = ch.ToolCall
		}
	}
	if err := <-errs; err != nil {
		t.Fatalf("err: %v", err)
	}
	if tc != nil {
		// A partial tool call (no terminating finish_reason) shouldn't surface.
		// This is a regression guard — partial JSON would crash downstream
		// JSON parsing in the executor.
		t.Errorf("partial tool call should not be flushed; got %+v", tc)
	}
}

func TestNewClientRequiresAPIKey(t *testing.T) {
	t.Setenv("Z_AI_API_KEY", "")
	// Make sure no credentials file is found either.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	_, err := New()
	if err == nil {
		t.Fatal("expected error when no API key is configured")
	}
}

func TestNewClientDefaultBaseURL(t *testing.T) {
	t.Setenv("Z_AI_API_KEY", "k")
	// Default base URL: the Coding Plan endpoint.
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL == "" {
		t.Errorf("BaseURL empty")
	}
}

func TestAvailableModelsHonoursEnv(t *testing.T) {
	// Only IDs already in the builtin list are surfaced — the override
	// filters the catalog rather than extending it.
	all := BuiltinAvailableModels
	if len(all) < 2 {
		t.Skip("not enough builtin models")
	}
	t.Setenv("ACP_GLM_AVAILABLE_MODELS", all[1].ModelID)
	models := AvailableModels()
	if len(models) != 1 || models[0].ModelID != all[1].ModelID {
		t.Errorf("env override not honoured: %+v", models)
	}
}

func TestDefaultModelEnv(t *testing.T) {
	t.Setenv("ACP_GLM_MODEL", "my-fav-model")
	if DefaultModelEnv() != "my-fav-model" {
		t.Errorf("env override missing")
	}
	_ = os.Unsetenv("ACP_GLM_MODEL")
	if DefaultModelEnv() == "" {
		t.Errorf("expected a non-empty fallback")
	}
}
