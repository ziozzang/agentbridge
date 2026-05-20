package ollamasearchplugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchAndFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("missing authorization header")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/web_search":
			if body["query"] != "agentbridge" {
				t.Fatalf("query = %v", body["query"])
			}
			if body["max_results"].(float64) != 3 {
				t.Fatalf("max_results = %v", body["max_results"])
			}
			_, _ = w.Write([]byte(`{"results":[{"title":"AgentBridge","url":"https://example.com","content":"result"}]}`))
		case "/api/web_fetch":
			if body["url"] != "https://example.com" {
				t.Fatalf("url = %v", body["url"])
			}
			_, _ = w.Write([]byte(`{"title":"Example","content":"body","links":["https://example.com/a"]}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := New(Config{APIKey: "test-key", BaseURL: srv.URL, HTTPClient: srv.Client()})
	searchOut, err := p.Call(context.Background(), "ollama_search", json.RawMessage(`{"query":"agentbridge","max_results":3}`))
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if !strings.Contains(searchOut, `"results"`) {
		t.Fatalf("search output missing results: %s", searchOut)
	}
	fetchOut, err := p.Call(context.Background(), "ollama_fetch", json.RawMessage(`{"url":"https://example.com"}`))
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if !strings.Contains(fetchOut, `"links"`) {
		t.Fatalf("fetch output missing links: %s", fetchOut)
	}
}

func TestSearchRequiresAPIKey(t *testing.T) {
	p := New(Config{BaseURL: "http://127.0.0.1"})
	_, err := p.Call(context.Background(), "ollama_search", json.RawMessage(`{"query":"agentbridge"}`))
	if err == nil || !strings.Contains(err.Error(), "API key is required") {
		t.Fatalf("error = %v", err)
	}
}
