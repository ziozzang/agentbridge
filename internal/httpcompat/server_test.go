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

func TestA2AAgentCard(t *testing.T) {
	srv := httptest.NewServer(NewHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var card map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	if card["preferredTransport"] != "JSONRPC" || !strings.HasSuffix(card["url"].(string), "/a2a/rpc") {
		t.Fatalf("bad agent card: %+v", card)
	}

	resp, err = http.Get(srv.URL + "/v1/a2a/agent-card.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("v1 status=%d", resp.StatusCode)
	}
}

func TestA2ASendGetListAndAlias(t *testing.T) {
	withMockHTTPProvider(t, func(srv *httptest.Server) {
		sendBody := `{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"role":"user","parts":[{"text":"hi"}],"messageId":"msg-1"},"model":"test-model"}}`
		resp, err := http.Post(srv.URL+"/v1/a2a/rpc", "application/json", strings.NewReader(sendBody))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var send struct {
			Result a2aTask       `json:"result"`
			Error  *jsonRPCError `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&send); err != nil {
			t.Fatal(err)
		}
		if send.Error != nil {
			t.Fatalf("send error: %+v", send.Error)
		}
		if send.Result.TaskID == "" || send.Result.ContextID == "" {
			t.Fatalf("missing task identity: %+v", send.Result)
		}
		if send.Result.Status.State != "TASK_STATE_COMPLETED" {
			t.Fatalf("status=%s", send.Result.Status.State)
		}
		if len(send.Result.Artifacts) != 1 || send.Result.Artifacts[0].Parts[0].Text != "OK" {
			t.Fatalf("bad artifact: %+v", send.Result.Artifacts)
		}

		getBody := fmt.Sprintf(`{"jsonrpc":"2.0","id":"2","method":"tasks/get","params":{"taskId":%q,"contextId":%q}}`, send.Result.TaskID, send.Result.ContextID)
		resp, err = http.Post(srv.URL+"/a2a/rpc", "application/json", strings.NewReader(getBody))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var get struct {
			Result a2aTask `json:"result"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&get); err != nil {
			t.Fatal(err)
		}
		if get.Result.TaskID != send.Result.TaskID || get.Result.Status.State != "TASK_STATE_COMPLETED" {
			t.Fatalf("bad get result: %+v", get.Result)
		}

		listBody := fmt.Sprintf(`{"jsonrpc":"2.0","id":"3","method":"ListTasks","params":{"contextId":%q}}`, send.Result.ContextID)
		resp, err = http.Post(srv.URL+"/a2a/rpc", "application/json", strings.NewReader(listBody))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), send.Result.TaskID) {
			t.Fatalf("list response %q missing task", string(body))
		}

		cancelBody := fmt.Sprintf(`{"jsonrpc":"2.0","id":"4","method":"CancelTask","params":{"taskId":%q,"contextId":%q}}`, send.Result.TaskID, send.Result.ContextID)
		resp, err = http.Post(srv.URL+"/a2a/rpc", "application/json", strings.NewReader(cancelBody))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var cancel struct {
			Result a2aTask       `json:"result"`
			Error  *jsonRPCError `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&cancel); err != nil {
			t.Fatal(err)
		}
		if cancel.Error != nil || cancel.Result.TaskID != send.Result.TaskID {
			t.Fatalf("bad cancel response: result=%+v error=%+v", cancel.Result, cancel.Error)
		}
	})
}

func TestA2AStreamingMessage(t *testing.T) {
	withMockHTTPProvider(t, func(srv *httptest.Server) {
		body := `{"jsonrpc":"2.0","id":"1","method":"SendStreamingMessage","params":{"message":{"role":"user","parts":[{"text":"hi"}]}}}`
		resp, err := http.Post(srv.URL+"/a2a/rpc", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
			t.Fatalf("content-type=%q", ct)
		}
		got, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(got), "artifactUpdate") || !strings.Contains(string(got), "TASK_STATE_COMPLETED") {
			t.Fatalf("bad stream: %q", string(got))
		}
	})
}

func withMockHTTPProvider(t *testing.T, fn func(*httptest.Server)) {
	t.Helper()
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
	fn(srv)
}
