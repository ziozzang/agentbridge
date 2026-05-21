package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ziozzang/agentbridge/internal/provider"
)

func TestStreamChatInvokesClaudeJSON(t *testing.T) {
	dir := t.TempDir()
	cmdPath := filepath.Join(dir, "claude")
	if runtime.GOOS == "windows" {
		cmdPath += ".bat"
	}
	script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"OK\",\"stop_reason\":\"end_turn\",\"usage\":{\"input_tokens\":3,\"cache_read_input_tokens\":1,\"output_tokens\":2}}'\n"
	if err := os.WriteFile(cmdPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	c := New(provider.Config{
		DefaultModel: "sonnet",
		Extra:        map[string]any{"command": cmdPath, "permission_mode": "default"},
	})
	chunks, errs := c.StreamChat(context.Background(),
		[]provider.Message{{Role: "user", Content: "Reply OK"}},
		provider.StreamOptions{})

	var text string
	var usage *provider.Usage
	for ch := range chunks {
		text += ch.Text
		if ch.Usage != nil {
			usage = ch.Usage
		}
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if text != "OK" {
		t.Fatalf("text = %q", text)
	}
	if usage == nil || usage.TotalTokens != 5 || usage.CachedReadTokens != 1 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestRunInjectsClaudeEnvironment(t *testing.T) {
	dir := t.TempDir()
	cmdPath := filepath.Join(dir, "claude")
	logPath := filepath.Join(dir, "env.log")
	if runtime.GOOS == "windows" {
		cmdPath += ".bat"
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$ANTHROPIC_AUTH_TOKEN|$ANTHROPIC_BASE_URL|$ANTHROPIC_MODEL|$API_TIMEOUT_MS|$CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC\" > \"$CLAUDE_ENV_LOG\"\nprintf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"OK\"}'\n"
	if err := os.WriteFile(cmdPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "parent-token")

	c := New(provider.Config{
		DefaultModel: "sonnet",
		Extra: map[string]any{
			"command": cmdPath,
			"env": map[string]any{
				"ANTHROPIC_AUTH_TOKEN":                       "token",
				"ANTHROPIC_BASE_URL":                         "https://anthropic.test",
				"ANTHROPIC_MODEL":                            "custom-claude-model",
				"API_TIMEOUT_MS":                             "120000",
				"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":   "true",
				"CLAUDE_ENV_LOG":                             logPath,
				"EMPTY_VALUE_SHOULD_NOT_OVERRIDE_PARENT_ENV": "",
			},
		},
	})
	if _, err := c.run(context.Background(), "hi", "sonnet"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "token|https://anthropic.test|custom-claude-model|120000|true\n"
	if string(data) != want {
		t.Fatalf("env = %q want %q", string(data), want)
	}
}
