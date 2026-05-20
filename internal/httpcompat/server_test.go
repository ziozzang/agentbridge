package httpcompat

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompatibilityEndpoints(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected upstream path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	cfg := filepath.Join(t.TempDir(), "providers.yaml")
	if err := os.WriteFile(cfg, []byte(`providers:
  test-http:
    kind: openai-chat
    base_url: `+upstream.URL+`
    api_key: test-key
    default_model: test-model
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACP_HARNESS_PROVIDERS_FILE", cfg)
	t.Setenv("ACP_HARNESS_PROVIDER", "test-http")
	t.Setenv("ACP_HARNESS_MODEL", "")
	t.Setenv("ACP_HARNESS_API_KEY", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing"))

	srv := httptest.NewServer(NewHandler())
	defer srv.Close()

	tests := []struct {
		path string
		body string
		want string
	}{
		{"/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`, `"content":"OK"`},
		{"/v1/responses", `{"input":"hi"}`, `"output_text":"OK"`},
		{"/v1/messages", `{"messages":[{"role":"user","content":"hi"}]}`, `"text":"OK"`},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := http.Post(srv.URL+tc.path, "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d", resp.StatusCode)
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), tc.want) {
				t.Fatalf("response %q does not contain %s", string(body), tc.want)
			}
		})
	}
}

func TestRequestIDAndCacheMetadata(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	cfg := filepath.Join(t.TempDir(), "providers.yaml")
	if err := os.WriteFile(cfg, []byte(`providers:
  test-http:
    kind: openai-chat
    base_url: `+upstream.URL+`
    api_key: test-key
    default_model: test-model
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACP_HARNESS_PROVIDERS_FILE", cfg)
	t.Setenv("ACP_HARNESS_PROVIDER", "test-http")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing"))

	srv := httptest.NewServer(NewHandler())
	defer srv.Close()
	body := `{"metadata":{"request_id":"client-123","cache":{"scope":"prompt"}},"messages":[{"role":"user","content":"hi"}]}`
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Request-Id"); got != "client-123" {
		t.Fatalf("X-Request-Id=%q", got)
	}
	var decoded struct {
		ID        string `json:"id"`
		RequestID string `json:"request_id"`
		Metadata  struct {
			RequestID   string         `json:"request_id"`
			Cache       map[string]any `json:"cache"`
			CacheStatus string         `json:"cache_status"`
		} `json:"metadata"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ID != "chatcmpl-client-123" || decoded.RequestID != "client-123" || decoded.Metadata.RequestID != "client-123" {
		t.Fatalf("bad request ids: %+v", decoded)
	}
	if decoded.Metadata.CacheStatus != "bypass" || decoded.Metadata.Cache["scope"] != "prompt" {
		t.Fatalf("bad cache metadata: %+v", decoded.Metadata)
	}
}
