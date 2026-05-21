package httpcompat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeRuntimeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

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
	t.Setenv("AGENTBRIDGE_CONFIG_FILE", writeRuntimeConfig(t, ""))

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

func TestChatCompletionsStreamingFlushesUpstreamChunks(t *testing.T) {
	release := make(chan struct{})
	releaseUpstream := func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}
	defer releaseUpstream()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected upstream path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"A\"}}]}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"B\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}\n\n")
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
	t.Setenv("AGENTBRIDGE_CONFIG_FILE", writeRuntimeConfig(t, ""))

	srv := httptest.NewServer(NewHandler())
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%q", resp.StatusCode, string(body))
	}

	lines := make(chan string, 16)
	readErr := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		readErr <- scanner.Err()
		close(lines)
	}()

	var sawA bool
	for !sawA {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("stream closed before first content chunk")
			}
			if strings.Contains(line, `"content":"A"`) {
				sawA = true
			}
		case <-time.After(2 * time.Second):
			t.Fatal("first content chunk was not flushed while upstream was still open")
		}
	}

	releaseUpstream()
	var rest strings.Builder
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				if err := <-readErr; err != nil {
					t.Fatal(err)
				}
				got := rest.String()
				if !strings.Contains(got, `"content":"B"`) || !strings.Contains(got, "data: [DONE]") {
					t.Fatalf("stream did not finish correctly: %q", got)
				}
				return
			}
			rest.WriteString(line)
			rest.WriteString("\n")
		case <-time.After(2 * time.Second):
			t.Fatal("stream did not finish after upstream released")
		}
	}
}

func TestExperimentalIntentionProbe(t *testing.T) {
	var sawLogprobs bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected upstream path: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		sawLogprobs, _ = body["logprobs"].(bool)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"A"},"logprobs":{"content":[{"token":"A","logprob":-0.1,"top_logprobs":[{"token":"A","logprob":-0.1},{"token":"B","logprob":-2.4}]}]}}]}`)
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
	t.Setenv("AGENTBRIDGE_EXPERIMENTAL_INTENTION_PROBE", "1")

	srv := httptest.NewServer(NewHandler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/experimental/intention", "application/json", strings.NewReader(`{"prompt":"capital?","choices":["Seoul","Busan"]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%q", resp.StatusCode, string(body))
	}
	if !sawLogprobs {
		t.Fatal("upstream request did not enable logprobs")
	}
	if !strings.Contains(string(body), `"answer":"A"`) || !strings.Contains(string(body), `"experimental":true`) {
		t.Fatalf("bad intention response: %q", string(body))
	}
}

func TestProviderStatusAndUI(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	resp, err := http.Get(srv.URL + "/v1/providers/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var status struct {
		Provider map[string]any `json:"provider"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Provider["kind"] != "openai-chat" {
		t.Fatalf("provider status = %#v", status.Provider)
	}

	uiResp, err := http.Get(srv.URL + "/ui/")
	if err != nil {
		t.Fatal(err)
	}
	defer uiResp.Body.Close()
	body, _ := io.ReadAll(uiResp.Body)
	if uiResp.StatusCode != http.StatusOK || !strings.Contains(string(body), "AgentBridge Status") {
		t.Fatalf("ui status=%d body=%q", uiResp.StatusCode, string(body))
	}
}

func TestExperimentalIntentionProbeDisabledByDefault(t *testing.T) {
	t.Setenv("AGENTBRIDGE_EXPERIMENTAL_INTENTION_PROBE", "")
	t.Setenv("AGENTBRIDGE_EXPERIMENTS", "")
	srv := httptest.NewServer(NewHandler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/experimental/intention", "application/json", strings.NewReader(`{"choices":["A","B"]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%q", resp.StatusCode, string(body))
	}
}

func TestChatCompletionsAgentModelRunsToolLoop(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "visible.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	var requests []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		if len(requests) == 1 {
			fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"tc1","type":"function","function":{"name":"list_files","arguments":"{\"path\":\".\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"saw visible.txt"},"finish_reason":"stop"}]}`+"\n\n")
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
	t.Setenv("AGENTBRIDGE_CONFIG_FILE", writeRuntimeConfig(t, ""))

	srv := httptest.NewServer(NewHandler())
	defer srv.Close()

	body := `{"model":"agent:test-model","metadata":{"cwd":` + strconv.Quote(tmp) + `},"messages":[{"role":"user","content":"list files"}]}`
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, string(out))
	}
	if !strings.Contains(string(out), "saw visible.txt") {
		t.Fatalf("agent final response missing: %s", string(out))
	}
	if len(requests) != 2 {
		t.Fatalf("expected two upstream calls, got %d", len(requests))
	}
	if strings.Contains(requests[0], `"model":"agent:test-model"`) || !strings.Contains(requests[0], `"model":"test-model"`) {
		t.Fatalf("agent prefix was not stripped before upstream call:\n%s", requests[0])
	}
	if !strings.Contains(requests[1], `"role":"tool"`) || !strings.Contains(requests[1], "visible.txt") {
		t.Fatalf("tool result was not fed back to upstream:\n%s", requests[1])
	}
}

func TestChatCompletionsAgentStreamEmitsIntermediateEvents(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "visible.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	var requests []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		if len(requests) == 1 {
			fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"tc1","type":"function","function":{"name":"list_files","arguments":"{\"path\":\".\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"saw visible.txt"},"finish_reason":"stop"}]}`+"\n\n")
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
	t.Setenv("AGENTBRIDGE_CONFIG_FILE", writeRuntimeConfig(t, ""))

	srv := httptest.NewServer(NewHandler())
	defer srv.Close()

	body := `{"stream":true,"model":"agent:test-model","metadata":{"cwd":` + strconv.Quote(tmp) + `},"messages":[{"role":"user","content":"list files"}]}`
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, string(out))
	}
	got := string(out)
	for _, want := range []string{
		`"agent_event"`,
		`"type":"tool_call"`,
		`"type":"tool_result"`,
		`"content":"saw visible.txt"`,
		`data: [DONE]`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("stream missing %s:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{`"rawOutput"`, `"rawInput"`} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("agent event leaked %s: %s", forbidden, got)
		}
	}
}

func TestChatCompletionsAgentDefaultBypassesWritePermission(t *testing.T) {
	tmp := t.TempDir()
	var requests []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		if len(requests) == 1 {
			fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"tc1","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"out.txt\",\"content\":\"created\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		if !strings.Contains(requests[1], "File written successfully") {
			t.Fatalf("write result was not fed back to upstream:\n%s", requests[1])
		}
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"write ok"},"finish_reason":"stop"}]}`+"\n\n")
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
	t.Setenv("AGENTBRIDGE_CONFIG_FILE", writeRuntimeConfig(t, ""))

	srv := httptest.NewServer(NewHandler())
	defer srv.Close()

	body := `{"model":"agent:test-model","metadata":{"cwd":` + strconv.Quote(tmp) + `},"messages":[{"role":"user","content":"write out.txt"}]}`
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, string(out))
	}
	data, err := os.ReadFile(filepath.Join(tmp, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "created" {
		t.Fatalf("file content = %q", string(data))
	}
	if !strings.Contains(string(out), "write ok") {
		t.Fatalf("bad response: %s", string(out))
	}
}

func TestChatCompletionsAgentYoloModeFalseRejectsWriteAndStreamsPermissionEvents(t *testing.T) {
	tmp := t.TempDir()
	var requests []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		if len(requests) == 1 {
			fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"tc1","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"out.txt\",\"content\":\"secret-ish\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		if !strings.Contains(requests[1], "Write rejected by user.") {
			t.Fatalf("rejected write result was not fed back to upstream:\n%s", requests[1])
		}
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"write rejected"},"finish_reason":"stop"}]}`+"\n\n")
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
	t.Setenv("AGENTBRIDGE_CONFIG_FILE", writeRuntimeConfig(t, "agent:\n  yolo_mode: false\n"))

	srv := httptest.NewServer(NewHandler())
	defer srv.Close()

	body := `{"stream":true,"model":"agent:test-model","metadata":{"cwd":` + strconv.Quote(tmp) + `},"messages":[{"role":"user","content":"try write"}]}`
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, string(out))
	}
	if _, err := os.Stat(filepath.Join(tmp, "out.txt")); !os.IsNotExist(err) {
		t.Fatalf("file should not have been written, stat err=%v", err)
	}
	got := string(out)
	for _, want := range []string{
		`"type":"session/request_permission"`,
		`"status":"failed"`,
		`"type":"tool_result"`,
		`"content":"write rejected"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("stream missing %s:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{`"rawInput"`, `"rawOutput"`, `secret-ish`} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("agent event leaked %s: %s", forbidden, got)
		}
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

	resp, err = http.Post(srv.URL+"/v1/tools/duckdb_status", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(got), `"status\":\"unavailable\"`) {
		t.Fatalf("bad OpenAPI-style tool response: %s", string(got))
	}

	resp, err = http.Get(srv.URL + "/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(got), `/v1/tools/duckdb_status`) || strings.Contains(string(got), `/v1/tools/plugin__duckdb__duckdb_status`) {
		t.Fatalf("tool path missing from OpenAPI: %s", string(got))
	}

	resp, err = http.Get(srv.URL + "/v1/tool-catalog")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(got), `"object":"catalog"`) || !strings.Contains(string(got), `"name":"plugin__duckdb__duckdb_status"`) {
		t.Fatalf("plugin tool missing from catalog: %s", string(got))
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

func TestResponsesCompactPrune(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "providers.yaml")
	if err := os.WriteFile(cfg, []byte(`providers:
  test-http:
    kind: openai-chat
    base_url: http://127.0.0.1:1
    api_key: test-key
    default_model: test-model
    context_window: 200
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

	body := `{"model":"test-model","strategy":"prune","target_tokens":20,"messages":[` +
		`{"role":"system","content":"system"},` +
		`{"role":"user","content":"first long message that can be pruned"},` +
		`{"role":"assistant","content":"first answer"},` +
		`{"role":"user","content":"second long message that should remain near the end"},` +
		`{"role":"assistant","content":"second answer"}]}`
	resp, err := http.Post(srv.URL+"/v1/responses/compact", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, string(out))
	}
	text := string(out)
	if !strings.Contains(text, `"object":"conversation.compaction"`) || !strings.Contains(text, `"strategy":"prune"`) || !strings.Contains(text, `"compacted":true`) {
		t.Fatalf("bad compaction response: %s", text)
	}
}

func TestModelsExposeProviderMetadata(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "providers.yaml")
	if err := os.WriteFile(cfg, []byte(`providers:
  meta-http:
    kind: openai-chat
    base_url: http://127.0.0.1:1
    api_key: test-key
    default_model: grok-4
    models:
      - id: grok-4
        name: Grok 4
        description: xAI Grok via AgentBridge
        provider: xai
        api: responses
        input: [text, image]
        reasoning: true
        context_window: 256000
        max_tokens: 8192
        aliases: [grok]
        tags: [search]
        compat:
          responses: true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACP_HARNESS_PROVIDERS_FILE", cfg)
	t.Setenv("ACP_HARNESS_PROVIDER", "meta-http")
	t.Setenv("ACP_HARNESS_MODEL", "")
	t.Setenv("ACP_HARNESS_API_KEY", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing"))

	srv := httptest.NewServer(NewHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	text := string(out)
	if !strings.Contains(text, `"id":"grok-4"`) || !strings.Contains(text, `"owned_by":"xai"`) {
		t.Fatalf("provider owner missing: %s", text)
	}
	if !strings.Contains(text, `"reasoning":true`) || !strings.Contains(text, `"context_window":256000`) || !strings.Contains(text, `"aliases":["grok"]`) {
		t.Fatalf("model metadata missing: %s", text)
	}
	if !strings.Contains(text, `"kind":"llm"`) || !strings.Contains(text, `"capabilities":["chat"]`) || !strings.Contains(text, `"modalities":{"input":["text","image"],"output":["text"]}`) {
		t.Fatalf("model capability metadata missing: %s", text)
	}
}

func TestLlamaCppProviderOmitsModelByDefault(t *testing.T) {
	var chatBody map[string]any
	var completionBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"object":"list","data":[{"id":"local.gguf","owned_by":"llamacpp","meta":{"n_ctx":13312}}]}`)
		case "/v1/chat/completions":
			if err := json.NewDecoder(r.Body).Decode(&chatBody); err != nil {
				t.Fatal(err)
			}
			fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		case "/v1/completions":
			if err := json.NewDecoder(r.Body).Decode(&completionBody); err != nil {
				t.Fatal(err)
			}
			fmt.Fprint(w, `{"model":"local.gguf","choices":[{"text":" A","logprobs":{"content":[{"token":" A","logprob":-0.1,"top_logprobs":[{"token":" A","logprob":-0.1},{"token":" B","logprob":-2.0}]}]}}]}`)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	cfg := filepath.Join(t.TempDir(), "providers.yaml")
	if err := os.WriteFile(cfg, []byte(`providers:
  llama-local:
    kind: llama.cpp
    base_url: `+upstream.URL+`
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACP_HARNESS_PROVIDERS_FILE", cfg)
	t.Setenv("ACP_HARNESS_PROVIDER", "llama-local")
	t.Setenv("ACP_HARNESS_MODEL", "")
	t.Setenv("ACP_HARNESS_API_KEY", "")
	t.Setenv("AGENTBRIDGE_EXPERIMENTAL_INTENTION_PROBE", "1")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing"))

	srv := httptest.NewServer(NewHandler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if _, ok := chatBody["model"]; ok {
		t.Fatalf("llama.cpp chat request should omit model by default: %#v", chatBody)
	}
	resp, err = http.Post(srv.URL+"/experimental/intention", "application/json", strings.NewReader(`{"prompt":"capital?","choices":["Seoul","Busan"]}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if _, ok := completionBody["model"]; ok {
		t.Fatalf("llama.cpp intention request should omit model by default: %#v", completionBody)
	}
}

func TestEmbeddingsEndpointUsesActiveJinaPlugin(t *testing.T) {
	var got map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Fatalf("unexpected embeddings path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"jina-embeddings-v3","usage":{"total_tokens":2}}`))
	}))
	defer upstream.Close()

	t.Setenv("AGENTBRIDGE_PLUGINS", "jina")
	t.Setenv("AGENTBRIDGE_JINA_EMBEDDINGS_BASE_URL", upstream.URL)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing"))

	srv := httptest.NewServer(NewHandler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/embeddings", "application/json", strings.NewReader(`{"model":"jina-embeddings-v3","input":["hello","world"]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"embedding":[0.1,0.2]`) {
		t.Fatalf("bad embeddings response status=%d body=%q", resp.StatusCode, string(body))
	}
	if got["model"] != "jina-embeddings-v3" {
		t.Fatalf("bad upstream model: %#v", got)
	}
	inputs, ok := got["input"].([]any)
	if !ok || len(inputs) != 2 {
		t.Fatalf("bad upstream input: %#v", got)
	}

	resp, err = http.Post(srv.URL+"/v1/embeddings", "application/json", strings.NewReader(`"hello"`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"embedding":[0.1,0.2]`) {
		t.Fatalf("bad shorthand embeddings response status=%d body=%q", resp.StatusCode, string(body))
	}
	if got["input"] != "hello" {
		t.Fatalf("bad shorthand upstream input: %#v", got)
	}

	resp, err = http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"id":"jina-embeddings-v3"`) || !strings.Contains(string(body), `"owned_by":"jina"`) {
		t.Fatalf("embedding model not exposed: %q", string(body))
	}
	if !strings.Contains(string(body), `"kind":"embedding"`) || !strings.Contains(string(body), `"capabilities":["embeddings"]`) || !strings.Contains(string(body), `"output":["embedding"]`) {
		t.Fatalf("embedding metadata not exposed: %q", string(body))
	}
}

func TestRouterEmbeddingMappingExposesProviderOwners(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "agentbridge")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `providers:
  router:
    extra:
      embeddings:
        default: router-embed
        models:
          router-embed:
            base_url: http://127.0.0.1:28080/v1
            model: upstream-router-embed
            provider: router-local
            description: Router configured embedding
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTBRIDGE_PLUGINS", "openai_embed")
	t.Setenv("XDG_CONFIG_HOME", dir)
	_ = os.Unsetenv("AGENTBRIDGE_EMBEDDINGS_FILE")
	_ = os.Unsetenv("AGENTBRIDGE_EMBEDDINGS_MAP")

	srv := httptest.NewServer(NewHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	if !strings.Contains(text, `"id":"router-embed"`) || !strings.Contains(text, `"owned_by":"router-local"`) {
		t.Fatalf("router embedding model not exposed correctly: %s", text)
	}
	if !strings.Contains(text, `"kind":"embedding"`) || !strings.Contains(text, `"endpoint":"/v1/embeddings"`) {
		t.Fatalf("router embedding metadata missing: %s", text)
	}
	if strings.Contains(text, `upstream-router-embed`) {
		t.Fatalf("upstream model should not be exposed as public id: %s", text)
	}
}

func TestRerankEndpointUsesActiveJinaPlugin(t *testing.T) {
	var got map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" {
			t.Fatalf("unexpected rerank path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jina-reranker-v3","results":[{"index":0,"relevance_score":0.99}]}`))
	}))
	defer upstream.Close()

	t.Setenv("AGENTBRIDGE_PLUGINS", "jina")
	t.Setenv("AGENTBRIDGE_JINA_RERANK_BASE_URL", upstream.URL)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing"))

	srv := httptest.NewServer(NewHandler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/rerank", "application/json", strings.NewReader(`{"query":"agentbridge","documents":["AgentBridge routes models.","Other"],"top_n":1}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"relevance_score":0.99`) {
		t.Fatalf("bad rerank response status=%d body=%q", resp.StatusCode, string(body))
	}
	if got["model"] != "jina-reranker-v3" || got["query"] != "agentbridge" {
		t.Fatalf("bad upstream rerank body: %#v", got)
	}

	resp, err = http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"id":"jina-reranker-v3"`) || !strings.Contains(string(body), `"owned_by":"jina"`) {
		t.Fatalf("rerank model not exposed: %q", string(body))
	}
	if !strings.Contains(string(body), `"kind":"reranker"`) || !strings.Contains(string(body), `"capabilities":["rerank"]`) || !strings.Contains(string(body), `"output":["ranking"]`) {
		t.Fatalf("rerank metadata not exposed: %q", string(body))
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

func TestA2ASendMessageAgentConfigurationRunsToolLoop(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "a2a.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	var requests []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		if len(requests) == 1 {
			fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"tc1","type":"function","function":{"name":"list_files","arguments":"{\"path\":\".\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"A2A saw a2a.txt"},"finish_reason":"stop"}]}`+"\n\n")
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

	sendBody := `{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"configuration":{"agent":true,"cwd":` + strconv.Quote(tmp) + `},"message":{"role":"user","parts":[{"text":"list files"}],"messageId":"msg-1"},"model":"test-model"}}`
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
	if send.Result.Status.State != "TASK_STATE_COMPLETED" {
		t.Fatalf("status=%s", send.Result.Status.State)
	}
	if len(send.Result.Artifacts) != 1 || !strings.Contains(send.Result.Artifacts[0].Parts[0].Text, "A2A saw a2a.txt") {
		t.Fatalf("bad artifact: %+v", send.Result.Artifacts)
	}
	if len(requests) != 2 || !strings.Contains(requests[1], `"role":"tool"`) || !strings.Contains(requests[1], "a2a.txt") {
		t.Fatalf("tool result was not fed back to upstream: calls=%d second=%q", len(requests), requests)
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

		resp, err = http.Get(srv.URL + "/v1/models")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ = io.ReadAll(resp.Body)
		if !strings.Contains(string(body), `"object":"list"`) || !strings.Contains(string(body), `"id":"test-model"`) {
			t.Fatalf("bad models: %q", string(body))
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
