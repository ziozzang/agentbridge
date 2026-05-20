package openaiembedplugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbedPostsOpenAICompatibleRequest(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer embed-key" {
			t.Fatalf("auth = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"embedding":[0.1,0.2]}]}`))
	}))
	defer srv.Close()

	p := New(Config{APIKey: "embed-key", BaseURL: srv.URL, Model: "text-embedding-test", HTTPClient: srv.Client()})
	out, err := p.Call(context.Background(), "embed", json.RawMessage(`{"inputs":["a","b"],"dimensions":128}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"embedding"`) {
		t.Fatalf("out = %s", out)
	}
	if got["model"] != "text-embedding-test" || got["dimensions"].(float64) != 128 {
		t.Fatalf("payload = %#v", got)
	}
}

func TestEmbedUsesExternalModelMapping(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Route") != "mapped" {
			t.Fatalf("missing mapped header: %q", r.Header.Get("X-Test-Route"))
		}
		if r.Header.Get("Authorization") != "Bearer mapped-key" {
			t.Fatalf("auth = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"embedding":[0.3]}]}`))
	}))
	defer srv.Close()

	t.Setenv("MAPPED_EMBED_KEY", "mapped-key")
	dir := t.TempDir()
	path := filepath.Join(dir, "embeddings.json")
	body := `{
  "default": "small",
  "models": {
    "small": {
      "base_url": "` + srv.URL + `",
      "api_key_env": "MAPPED_EMBED_KEY",
      "model": "upstream-small",
      "dimensions": 64,
      "headers": {"X-Test-Route": "mapped"}
    }
  }
}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	p := New(Config{MappingFile: path, HTTPClient: srv.Client()})
	if _, err := p.Call(context.Background(), "embed", json.RawMessage(`{"input":"hello"}`)); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "upstream-small" || got["dimensions"].(float64) != 64 {
		t.Fatalf("payload = %#v", got)
	}
}
