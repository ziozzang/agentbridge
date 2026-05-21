package codexnative

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziozzang/agentbridge/internal/provider"
)

func TestStreamChatAndSessionReuse(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "turn-inputs.log")
	t.Setenv("GO_WANT_CODEX_HELPER", "1")
	t.Setenv("CODEX_NATIVE_HELPER_MODE", "stream")
	t.Setenv("CODEX_NATIVE_HELPER_LOG", logPath)

	client := New(provider.Config{
		Name:          "codex-app",
		Kind:          Kind,
		DefaultModel:  "gpt-5",
		ContextWindow: 400000,
		Extra: map[string]any{
			"argv": []any{os.Args[0], "-test.run=TestCodexNativeHelperProcess", "--"},
		},
	})

	msgs1 := []provider.Message{{Role: "user", Content: "hello first"}}
	chunks, errs := client.StreamChat(context.Background(), msgs1, provider.StreamOptions{
		Model:           "gpt-5",
		SessionID:       "sess-1",
		ServiceTier:     "priority",
		ReasoningEffort: "high",
	})
	var text strings.Builder
	var usage provider.Usage
	for ch := range chunks {
		text.WriteString(ch.Text)
		if ch.Usage != nil {
			usage = *ch.Usage
		}
	}
	if err := <-errs; err != nil {
		t.Fatalf("first stream error: %v", err)
	}
	if got := text.String(); got != "hello world" {
		t.Fatalf("first text = %q", got)
	}
	if usage.TotalTokens != 16 || usage.CachedReadTokens != 2 || usage.ThoughtTokens != 4 {
		t.Fatalf("usage = %#v", usage)
	}

	msgs2 := []provider.Message{
		{Role: "user", Content: "hello first"},
		{Role: "assistant", Content: "hello world"},
		{Role: "user", Content: "second question"},
	}
	chunks, errs = client.StreamChat(context.Background(), msgs2, provider.StreamOptions{
		Model:           "gpt-5",
		SessionID:       "sess-1",
		ServiceTier:     "priority",
		ReasoningEffort: "high",
	})
	text.Reset()
	for ch := range chunks {
		text.WriteString(ch.Text)
	}
	if err := <-errs; err != nil {
		t.Fatalf("second stream error: %v", err)
	}
	if got := text.String(); got != "hello world" {
		t.Fatalf("second text = %q", got)
	}

	lines := readHelperLog(t, logPath)
	if len(lines) != 2 {
		t.Fatalf("logged prompts = %#v", lines)
	}
	if !strings.Contains(lines[0], "hello first") {
		t.Fatalf("first prompt = %q", lines[0])
	}
	if strings.Contains(lines[1], "hello first") {
		t.Fatalf("second prompt should be incremental, got %q", lines[1])
	}
	if !strings.Contains(lines[1], "second question") || !strings.Contains(lines[1], "hello world") {
		t.Fatalf("second prompt missing delta context: %q", lines[1])
	}
}

func TestCompactConversationUsesRemoteThreadAndReturnsCheckpoint(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "compact.log")
	t.Setenv("GO_WANT_CODEX_HELPER", "1")
	t.Setenv("CODEX_NATIVE_HELPER_MODE", "compact")
	t.Setenv("CODEX_NATIVE_HELPER_LOG", logPath)

	client := New(provider.Config{
		Name:          "codex-app",
		Kind:          Kind,
		DefaultModel:  "gpt-5",
		ContextWindow: 400000,
		Extra: map[string]any{
			"argv": []any{os.Args[0], "-test.run=TestCodexNativeHelperProcess", "--"},
		},
	})

	client.recordSession("sess-compact", "thread-compact", []string{"x"})
	in := []provider.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "two"},
		{Role: "user", Content: "three"},
		{Role: "assistant", Content: "four"},
	}
	out, err := client.CompactConversation(context.Background(), in, provider.CompactOptions{
		SessionID: "sess-compact",
		Reason:    "token pressure",
	})
	if err != nil {
		t.Fatalf("compact error: %v", err)
	}
	if len(out) >= len(in) {
		t.Fatalf("expected compacted local checkpoint, got len=%d in=%d", len(out), len(in))
	}
	if out[1].Role != "user" || !strings.Contains(contentText(out[1].Content), "compacted") {
		t.Fatalf("checkpoint message = %#v", out[1])
	}
	lines := readHelperLog(t, logPath)
	if len(lines) == 0 || lines[len(lines)-1] != "thread/compact/start" {
		t.Fatalf("compact log = %#v", lines)
	}
}

func TestSanitizeStreamOptionsDropsPromptCacheHints(t *testing.T) {
	client := New(provider.Config{})
	opts := client.SanitizeStreamOptions(provider.StreamOptions{
		PromptCacheKey:       "session-1",
		PromptCacheRetention: "1h",
		ServiceTier:          "priority",
		ReasoningEffort:      "medium",
	})
	if opts.PromptCacheKey != "session-1" || opts.PromptCacheRetention != "" {
		t.Fatalf("sanitize stream opts = %#v", opts)
	}
	if opts.ServiceTier != "priority" || opts.ReasoningEffort != "medium" {
		t.Fatalf("sanitize stream opts lost supported fields = %#v", opts)
	}
}

func TestAvailableModelsCanUseNativeModelList(t *testing.T) {
	t.Setenv("GO_WANT_CODEX_HELPER", "1")
	t.Setenv("CODEX_NATIVE_HELPER_MODE", "models")

	client := New(provider.Config{
		Name:         "codex-app",
		Kind:         Kind,
		DefaultModel: "gpt-5.5",
		Extra: map[string]any{
			"argv":       []any{os.Args[0], "-test.run=TestCodexNativeHelperProcess", "--"},
			"model_list": "native",
		},
		Models: []provider.ModelInfo{{ModelID: "gpt-5-static", Name: "GPT-5 Static"}},
	})
	models := client.AvailableModels()
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}
	if models[0].ModelID != "gpt-5.5" || models[1].ModelID != "gpt-5.4" {
		t.Fatalf("native models = %+v", models)
	}
	for _, m := range models {
		if m.Provider != "codex-app" || m.Compat["agent_loop"] != "provider_native" {
			t.Fatalf("metadata not attached: %+v", m)
		}
	}
}

func TestWildcardModelsFallbackToGPT5StaticList(t *testing.T) {
	client := New(provider.Config{
		Name:          "codex-app",
		Kind:          Kind,
		DefaultModel:  "gpt-5.5",
		ContextWindow: 400000,
		Extra:         map[string]any{"model_list": "static"},
		Models:        []provider.ModelInfo{{ModelID: "*"}},
	})
	models := client.AvailableModels()
	if len(models) != 6 {
		t.Fatalf("models = %+v", models)
	}
	if models[0].ModelID != "gpt-5.5" || models[5].ModelID != "gpt-5.2" {
		t.Fatalf("fallback models = %+v", models)
	}
	if models[2].ModelID != "gpt-5.4-mini" || models[4].ModelID != "gpt-5.3-codex-spark" {
		t.Fatalf("fallback models = %+v", models)
	}
}

func TestBinaryPathTakesPrecedenceOverCommand(t *testing.T) {
	client := New(provider.Config{Extra: map[string]any{
		"binary_path": "/opt/codex/bin/codex",
		"command":     "codex",
	}})
	command, args := client.commandAndArgs()
	if command != "/opt/codex/bin/codex" {
		t.Fatalf("command = %q", command)
	}
	if strings.Join(args, " ") != "app-server --listen stdio://" {
		t.Fatalf("args = %v", args)
	}
}

func TestCodexNativeHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_CODEX_HELPER") != "1" {
		return
	}
	mode := os.Getenv("CODEX_NATIVE_HELPER_MODE")
	logPath := os.Getenv("CODEX_NATIVE_HELPER_LOG")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		method := stringAt(msg, "method")
		switch method {
		case "initialize":
			respondHelper(msg["id"], map[string]any{"sessionId": "helper-session"})
		case "initialized":
		case "thread/start":
			notifyHelper("configWarning", map[string]any{"summary": "test warning"})
			respondHelper(msg["id"], map[string]any{"thread": map[string]any{"id": "thread-1"}})
		case "thread/resume":
			notifyHelper("configWarning", map[string]any{"summary": "test warning"})
			respondHelper(msg["id"], map[string]any{})
		case "model/list":
			notifyHelper("configWarning", map[string]any{"summary": "test warning"})
			respondHelper(msg["id"], map[string]any{"data": []any{
				map[string]any{"id": "gpt-5.5", "name": "GPT-5.5"},
				map[string]any{"id": "gpt-5.4", "name": "GPT-5.4"},
			}})
		case "turn/start":
			logHelper(logPath, contentText(nestedInputText(msg)))
			respondHelper(msg["id"], map[string]any{"turn": map[string]any{"id": "turn-1", "status": "in_progress"}})
			notifyHelper("item/agentMessage/delta", map[string]any{
				"threadId": "thread-1",
				"turnId":   "turn-1",
				"itemId":   "item-1",
				"delta":    "hello ",
			})
			notifyHelper("item/agentMessage/delta", map[string]any{
				"threadId": "thread-1",
				"turnId":   "turn-1",
				"itemId":   "item-1",
				"delta":    "world",
			})
			notifyHelper("thread/tokenUsage/updated", map[string]any{
				"threadId": "thread-1",
				"turnId":   "turn-1",
				"tokenUsage": map[string]any{
					"last": map[string]any{
						"inputTokens":           10,
						"cachedInputTokens":     2,
						"outputTokens":          6,
						"reasoningOutputTokens": 4,
						"totalTokens":           16,
					},
				},
			})
			notifyHelper("turn/completed", map[string]any{
				"threadId": "thread-1",
				"turn": map[string]any{
					"id":     "turn-1",
					"status": "completed",
				},
			})
		case "thread/compact/start":
			logHelper(logPath, "thread/compact/start")
			respondHelper(msg["id"], map[string]any{})
		default:
			if msg["id"] != nil {
				respondHelper(msg["id"], map[string]any{})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		os.Exit(2)
	}
	if mode == "compact" || mode == "stream" {
		os.Exit(0)
	}
}

func nestedInputText(msg map[string]any) any {
	params, _ := msg["params"].(map[string]any)
	input, _ := params["input"].([]any)
	if len(input) == 0 {
		return ""
	}
	first, _ := input[0].(map[string]any)
	return first["text"]
}

func respondHelper(id any, result map[string]any) {
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"id":     id,
		"result": result,
	})
}

func notifyHelper(method string, params map[string]any) {
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"method": method,
		"params": params,
	})
}

func logHelper(path, line string) {
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	data, _ := json.Marshal(line)
	_, _ = f.WriteString(string(data) + "\n")
}

func readHelperLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var value string
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatalf("decode helper log line %q: %v", line, err)
		}
		out = append(out, value)
	}
	return out
}
