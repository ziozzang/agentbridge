package glmprov

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ziozzang/agentbridge/internal/provider"
)

func TestGLMRegistration(t *testing.T) {
	p, err := provider.Build(provider.Config{Kind: Kind})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if p.Kind() != Kind {
		t.Errorf("kind=%q", p.Kind())
	}
	if p.DefaultModel() != "glm-5.1" {
		t.Errorf("default=%q", p.DefaultModel())
	}
	if len(p.AvailableModels()) == 0 {
		t.Errorf("expected default models")
	}
}

func TestGLMInjectsThinkingForGLM5Models(t *testing.T) {
	var saw bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["thinking"]; ok {
			saw = true
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := provider.Build(provider.Config{
		Kind: Kind, BaseURL: srv.URL, APIKey: "k",
	})
	if err != nil {
		t.Fatal(err)
	}
	chunks, errs := p.StreamChat(context.Background(), nil,
		provider.StreamOptions{Model: "glm-5.1"})
	for range chunks {
	}
	<-errs
	if !saw {
		t.Errorf("expected thinking flag injected for glm-5.1")
	}
}

func TestGLMSkipsThinkingForNonGLM5Models(t *testing.T) {
	var saw bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["thinking"]; ok {
			saw = true
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := provider.Build(provider.Config{
		Kind: Kind, BaseURL: srv.URL, APIKey: "k",
	})
	if err != nil {
		t.Fatal(err)
	}
	chunks, errs := p.StreamChat(context.Background(), nil,
		provider.StreamOptions{Model: "glm-4.0"})
	for range chunks {
	}
	<-errs
	if saw {
		t.Errorf("did not expect thinking flag for glm-4.0")
	}
}
