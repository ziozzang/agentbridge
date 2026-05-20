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

func TestMCPExposesPluginToolsWithoutCallingLLM(t *testing.T) {
	t.Setenv("AGENTBRIDGE_PLUGINS", "duckdb")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing"))
	srv := httptest.NewServer(NewHandler())
	defer srv.Close()

	listBody := `{"jsonrpc":"2.0","id":"1","method":"tools/list"}`
	resp, err := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(listBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range list.Result.Tools {
		if tool.Name == "plugin__duckdb__duckdb_status" {
			found = true
		}
	}
	if !found {
		t.Fatalf("plugin tool not listed: %+v", list.Result.Tools)
	}

	callBody := `{"jsonrpc":"2.0","id":"2","method":"tools/call","params":{"name":"plugin__duckdb__duckdb_status","arguments":{}}}`
	resp, err = http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(callBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(got), `"status\":\"unavailable\"`) {
		t.Fatalf("bad plugin call response: %s", string(got))
	}
}

func TestMCPExposesConfiguredExternalMCPAndMetrics(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("MCP-Session-Id", "sess")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"protocolVersion": "2025-06-18"}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": []map[string]any{{
				"name": "search", "description": "Search test", "inputSchema": map[string]any{"type": "object"},
			}}}})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
				"content": []map[string]any{{"type": "text", "text": "searched"}},
			}})
		default:
			t.Fatalf("unexpected method %s", req.Method)
		}
	}))
	defer upstream.Close()
	cfg := filepath.Join(t.TempDir(), "mcp.yaml")
	if err := os.WriteFile(cfg, []byte(`mcp_servers:
  - name: ext
    type: http
    url: `+upstream.URL+`
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTBRIDGE_MCP_FILE", cfg)
	t.Setenv("AGENTBRIDGE_PLUGINS", "")
	srv := httptest.NewServer(NewHandler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	list, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(list), "mcp__ext__search") {
		t.Fatalf("external MCP tool not listed: %s", string(list))
	}
	call := `{"jsonrpc":"2.0","id":"2","method":"tools/call","params":{"name":"mcp__ext__search","arguments":{"query":"x"}}}`
	resp, err = http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(call))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "searched") {
		t.Fatalf("bad call body: %s", string(body))
	}
	resp, err = http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	metrics, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(metrics), `agentbridge_tool_calls_total{kind="mcp",name="mcp__ext__search",status="ok"}`) {
		t.Fatalf("missing tool metric: %s", string(metrics))
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

func TestResponsesPreviousResponseAndRetrieve(t *testing.T) {
	withMockHTTPProvider(t, func(srv *httptest.Server) {
		resp, err := http.Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(`{"input":"hi","model":"test-model","store":true}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var first struct {
			ID         string `json:"id"`
			OutputText string `json:"output_text"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&first); err != nil {
			t.Fatal(err)
		}
		if first.ID == "" || first.OutputText != "OK" {
			t.Fatalf("bad first response: %+v", first)
		}

		body := fmt.Sprintf(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"again"}]}],"previous_response_id":%q,"parallel_tool_calls":false,"prompt_cache_key":"k"}`, first.ID)
		resp, err = http.Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var second struct {
			PreviousResponseID string `json:"previous_response_id"`
			ParallelToolCalls  bool   `json:"parallel_tool_calls"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&second); err != nil {
			t.Fatal(err)
		}
		if second.PreviousResponseID != first.ID || second.ParallelToolCalls {
			t.Fatalf("bad second response: %+v", second)
		}

		resp, err = http.Get(srv.URL + "/v1/responses/" + first.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		got, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(got), `"output_text":"OK"`) {
			t.Fatalf("bad retrieve: %q", string(got))
		}
	})
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

func TestMCPAGUIOpenAPIMetrics(t *testing.T) {
	withMockHTTPProvider(t, func(srv *httptest.Server) {
		initBody := `{"jsonrpc":"2.0","id":"1","method":"initialize","params":{"protocolVersion":"2025-06-18"}}`
		resp, err := http.Post(srv.URL+"/v1/mcp", "application/json", strings.NewReader(initBody))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.Header.Get("Mcp-Session-Id") == "" {
			t.Fatal("missing MCP session id")
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), `"protocolVersion":"2025-06-18"`) {
			t.Fatalf("bad initialize: %q", string(body))
		}

		callBody := `{"jsonrpc":"2.0","id":"2","method":"tools/call","params":{"name":"chat","arguments":{"input":"hi","model":"test-model"}}}`
		resp, err = http.Post(srv.URL+"/v1/mcp", "application/json", strings.NewReader(callBody))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ = io.ReadAll(resp.Body)
		if !strings.Contains(string(body), `"text":"OK"`) {
			t.Fatalf("bad tool call: %q", string(body))
		}

		resp, err = http.Post(srv.URL+"/v1/agui/run", "application/json", strings.NewReader(`{"input":"hi","model":"test-model"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ = io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "TEXT_MESSAGE_START") || !strings.Contains(string(body), "TEXT_MESSAGE_CONTENT") || !strings.Contains(string(body), "RUN_FINISHED") {
			t.Fatalf("bad ag-ui stream: %q", string(body))
		}

		resp, err = http.Get(srv.URL + "/openapi.json")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ = io.ReadAll(resp.Body)
		if !strings.Contains(string(body), `"openapi":"3.1.1"`) || !strings.Contains(string(body), `/v1/mcp`) {
			t.Fatalf("bad openapi: %q", string(body))
		}

		resp, err = http.Get(srv.URL + "/swagger")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ = io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "SwaggerUIBundle") {
			t.Fatalf("bad swagger ui: %q", string(body))
		}

		resp, err = http.Get(srv.URL + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ = io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "agentbridge_http_requests_total") {
			t.Fatalf("bad metrics: %q", string(body))
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
