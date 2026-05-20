package openairesp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ziozzang/agentbridge/internal/provider"
)

func TestStreamChatResponsesEnd2End(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer key" {
			t.Errorf("auth = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"type":"response.output_text.delta","delta":"Hel"}`,
			`{"type":"response.output_text.delta","delta":"lo"}`,
			`{"type":"response.reasoning_summary_text.delta","delta":"think"}`,
			`{"type":"response.output_item.added","item":{"type":"function_call","id":"item1","call_id":"call1","name":"read_file"}}`,
			`{"type":"response.function_call_arguments.delta","item_id":"item1","delta":"{\"pa"}`,
			`{"type":"response.function_call_arguments.delta","item_id":"item1","delta":"th\":\"x\"}"}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","id":"item1","call_id":"call1","name":"read_file"}}`,
			`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}}`,
		}
		for _, e := range events {
			fmt.Fprintf(w, "data: %s\n\n", e)
		}
	}))
	defer srv.Close()

	c := New(provider.Config{
		Name: "resp-test", BaseURL: srv.URL, APIKey: "key",
		Models: []provider.ModelInfo{{ModelID: "test"}},
	})
	c.HTTPClient = srv.Client()
	chunks, errs := c.StreamChat(context.Background(),
		[]provider.Message{
			{Role: "system", Content: "be helpful"},
			{Role: "user", Content: "hello"},
		},
		provider.StreamOptions{Model: "test"})
	var text, thinking string
	var tcs []provider.ToolCall
	var stop string
	var usage *provider.Usage
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
		t.Fatalf("err: %v", err)
	}
	if text != "Hello" {
		t.Errorf("text=%q", text)
	}
	if thinking != "think" {
		t.Errorf("thinking=%q", thinking)
	}
	if len(tcs) != 1 || tcs[0].ID != "call1" || tcs[0].Name != "read_file" || tcs[0].Arguments != `{"path":"x"}` {
		t.Errorf("tool calls=%+v", tcs)
	}
	if usage == nil || usage.TotalTokens != 8 {
		t.Errorf("usage=%+v", usage)
	}
	if stop != "end_turn" {
		t.Errorf("stop=%q", stop)
	}
}

func TestResponsesContextOverflow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"code":"context_length_exceeded","message":"context length exceeded"}}`))
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

func TestCodexStyleResponsesOptions(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if got := r.Header.Get("ChatGPT-Account-ID"); got != "account-123" {
			t.Errorf("account header = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"type":"response.output_text.delta","delta":"OK"}`)
		fmt.Fprintln(w)
		fmt.Fprintln(w, `data: {"type":"response.completed","response":{"status":"completed"}}`)
		fmt.Fprintln(w)
	}))
	defer srv.Close()

	c := New(provider.Config{
		Name:    "codex-test",
		BaseURL: srv.URL,
		APIKey:  "key",
		Headers: map[string]string{"ChatGPT-Account-ID": "account-123"},
		Extra: map[string]any{
			"responses_path":              "/responses",
			"instructions":                "Be concise.",
			"prompt_cache_key":            "thread-123",
			"reasoning_effort":            "medium",
			"include_reasoning_encrypted": true,
			"omit_max_output_tokens":      true,
			"service_tier":                "priority",
		},
	})
	c.HTTPClient = srv.Client()
	chunks, errs := c.StreamChat(context.Background(),
		[]provider.Message{{Role: "user", Content: "hi"}},
		provider.StreamOptions{Model: "gpt-5.5"})
	for range chunks {
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if gotPath != "/responses" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody["instructions"] != "Be concise." || gotBody["prompt_cache_key"] != "thread-123" {
		t.Fatalf("body missing codex fields: %#v", gotBody)
	}
	if gotBody["service_tier"] != "priority" {
		t.Fatalf("service_tier = %#v", gotBody["service_tier"])
	}
	if _, ok := gotBody["max_output_tokens"]; ok {
		t.Fatalf("max_output_tokens should be omitted: %#v", gotBody["max_output_tokens"])
	}
	reasoning, _ := gotBody["reasoning"].(map[string]any)
	if reasoning["effort"] != "medium" {
		t.Fatalf("reasoning = %#v", gotBody["reasoning"])
	}
	include, _ := gotBody["include"].([]any)
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v", gotBody["include"])
	}
}

func TestTranslateAssistantToolCalls(t *testing.T) {
	in := []provider.Message{
		{Role: "system", Content: "S"},
		{Role: "user", Content: "U"},
		{Role: "assistant", ToolCalls: []provider.ToolCallMsg{{
			ID: "id1", Type: "function",
			Function: provider.ToolCallMsgFunction{Name: "f", Arguments: `{"x":1}`},
		}}},
		{Role: "tool", ToolCallID: "id1", Content: "ok"},
	}
	inst, items := translateMessages(in)
	if inst != "S" {
		t.Errorf("instructions: %q", inst)
	}
	if len(items) != 3 {
		t.Fatalf("items: %d", len(items))
	}
	if items[1].Type != "function_call" || items[1].CallID != "id1" || items[1].Name != "f" {
		t.Errorf("function_call: %+v", items[1])
	}
	if items[2].Type != "function_call_output" || items[2].CallID != "id1" || items[2].Output != "ok" {
		t.Errorf("function_call_output: %+v", items[2])
	}
}
