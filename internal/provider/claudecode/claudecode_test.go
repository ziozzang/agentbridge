package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ziozzang/glm-acp/internal/provider"
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
