package jinaplugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReaderAndSearch(t *testing.T) {
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer test-key" {
			sawAuth = true
		}
		w.Header().Set("Content-Type", "text/plain")
		switch {
		case strings.Contains(r.URL.Path, "https://example.com"):
			_, _ = w.Write([]byte("Title: Example\n\nReader body"))
		case strings.Contains(r.URL.RawPath, "agentbridge") || strings.Contains(r.URL.Path, "agentbridge"):
			_, _ = w.Write([]byte("Search result for AgentBridge"))
		default:
			t.Fatalf("unexpected request path: path=%q raw=%q", r.URL.Path, r.URL.RawPath)
		}
	}))
	defer srv.Close()

	p := New(Config{
		APIKey:        "test-key",
		ReaderBaseURL: srv.URL,
		SearchBaseURL: srv.URL,
		HTTPClient:    srv.Client(),
	})
	readerOut, err := p.Call(context.Background(), "jina_reader", json.RawMessage(`{"url":"https://example.com"}`))
	if err != nil {
		t.Fatalf("reader failed: %v", err)
	}
	if !strings.Contains(readerOut, "Reader body") {
		t.Fatalf("reader output missing body: %s", readerOut)
	}
	searchOut, err := p.Call(context.Background(), "jina_search", json.RawMessage(`{"query":"agentbridge"}`))
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if !strings.Contains(searchOut, "Search result") {
		t.Fatalf("search output missing body: %s", searchOut)
	}
	if !sawAuth {
		t.Fatal("expected Authorization header")
	}
}

func TestEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Fatalf("path = %q, want /embeddings", r.URL.Path)
		}
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
		if body["model"] != "jina-embeddings-v3" {
			t.Fatalf("model = %v", body["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"jina-embeddings-v3","usage":{"total_tokens":2}}`))
	}))
	defer srv.Close()

	p := New(Config{
		APIKey:            "test-key",
		EmbeddingsBaseURL: srv.URL,
		HTTPClient:        srv.Client(),
	})
	out, err := p.Call(context.Background(), "jina_embed", json.RawMessage(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("embed failed: %v", err)
	}
	if !strings.Contains(out, `"embedding":[0.1,0.2]`) {
		t.Fatalf("embedding response not returned: %s", out)
	}
}
