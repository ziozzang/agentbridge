package ollama

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ziozzang/agentbridge/internal/provider"
)

func TestStreamChatOllamaEnd2End(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Ollama uses NDJSON, not SSE.
		lines := []string{
			`{"model":"m","message":{"role":"assistant","content":"Hel"},"done":false}`,
			`{"model":"m","message":{"role":"assistant","content":"lo"},"done":false}`,
			`{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"read_file","arguments":{"path":"x"}}}]},"done":false}`,
			`{"model":"m","done":true,"done_reason":"stop","prompt_eval_count":3,"eval_count":5}`,
		}
		for _, l := range lines {
			fmt.Fprintln(w, l)
		}
	}))
	defer srv.Close()
	c := New(provider.Config{
		Name: "ollama-test", BaseURL: srv.URL,
		Models: []provider.ModelInfo{{ModelID: "m"}},
	})
	c.HTTPClient = srv.Client()
	chunks, errs := c.StreamChat(context.Background(),
		[]provider.Message{{Role: "user", Content: "hi"}},
		provider.StreamOptions{Model: "m"})
	var text string
	var tcs []provider.ToolCall
	var stop string
	var usage *provider.Usage
	for ch := range chunks {
		text += ch.Text
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
		t.Fatalf("err: %v", err)
	}
	if text != "Hello" {
		t.Errorf("text=%q", text)
	}
	if len(tcs) != 1 || tcs[0].Name != "read_file" || tcs[0].Arguments != `{"path":"x"}` {
		t.Errorf("tool calls=%+v", tcs)
	}
	if stop != "stop" {
		t.Errorf("stop=%q", stop)
	}
	if usage == nil || usage.InputTokens != 3 || usage.OutputTokens != 5 {
		t.Errorf("usage=%+v", usage)
	}
}
