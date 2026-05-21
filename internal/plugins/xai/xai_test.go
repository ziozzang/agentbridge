package xaiplugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestXSearchPostsResponsesTool(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("auth = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"id":"resp_1","output_text":"ok"}`))
	}))
	defer srv.Close()

	p := New(Config{APIKey: "test-key", BaseURL: srv.URL, SearchModel: "grok-4.3", OAuthPath: filepath.Join(t.TempDir(), "missing.json"), HTTPClient: srv.Client()})
	out, err := p.Call(context.Background(), "xai_x_search", json.RawMessage(`{"query":"xAI news","allowed_x_handles":["xai"],"enable_image_understanding":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"output_text":"ok"`) {
		t.Fatalf("out = %s", out)
	}
	if got["model"] != "grok-4.3" {
		t.Fatalf("model = %#v", got["model"])
	}
	tools, ok := got["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", got["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "x_search" || tool["enable_image_understanding"] != true {
		t.Fatalf("tool = %#v", tool)
	}
}

func TestImageGeneratePostsImagesEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/generations" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got["model"] != defaultImageModel || got["prompt"] != "draw a bridge" {
			t.Fatalf("payload = %#v", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"url":"https://example.test/image.png"}]}`))
	}))
	defer srv.Close()

	p := New(Config{APIKey: "test-key", BaseURL: srv.URL, HTTPClient: srv.Client()})
	if _, err := p.Call(context.Background(), "xai_image_generate", json.RawMessage(`{"prompt":"draw a bridge"}`)); err != nil {
		t.Fatal(err)
	}
}
