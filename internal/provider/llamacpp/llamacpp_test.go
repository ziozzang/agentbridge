package llamacpp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ziozzang/agentbridge/internal/provider"
)

func TestStreamChatOmitsModelWhenUnset(t *testing.T) {
	var got map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()

	c := New(provider.Config{Name: "llama-test", Kind: Kind, BaseURL: upstream.URL, DefaultModel: "local.gguf"})
	chunks, errs := c.StreamChat(context.Background(), []provider.Message{{Role: "user", Content: "hi"}}, provider.StreamOptions{})
	var text string
	for ch := range chunks {
		text += ch.Text
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if text != "OK" {
		t.Fatalf("text=%q", text)
	}
	if _, ok := got["model"]; ok {
		t.Fatalf("model should be omitted: %#v", got)
	}
}

func TestProbeIntentionUsesCompletionsLogprobs(t *testing.T) {
	var got map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/completions" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"local.gguf","choices":[{"text":" A","logprobs":{"content":[{"token":" A","logprob":-0.1,"top_logprobs":[{"token":" A","logprob":-0.1},{"token":" B","logprob":-2.0}]}]}}]}`)
	}))
	defer upstream.Close()

	c := New(provider.Config{Name: "llama-test", Kind: Kind, BaseURL: upstream.URL, DefaultModel: "local.gguf"})
	out, err := c.ProbeIntention(context.Background(), provider.IntentionProbeRequest{
		Prompt:  "capital?",
		Choices: []provider.IntentionChoice{{Text: "Seoul"}, {Text: "Busan"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Answer != "A" || out.Index != 0 || out.Confidence <= 0.8 {
		t.Fatalf("bad probe result: %#v", out)
	}
	if _, ok := got["model"]; ok {
		t.Fatalf("model should be omitted: %#v", got)
	}
	if got["logprobs"] == nil {
		t.Fatalf("logprobs missing: %#v", got)
	}
}
