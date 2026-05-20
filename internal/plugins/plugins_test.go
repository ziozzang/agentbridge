package plugins

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type fakePlugin struct {
	called string
}

func (f *fakePlugin) Name() string { return "fake" }
func (f *fakePlugin) Tools() []ToolDef {
	return []ToolDef{{Name: "ping", Description: "ping", Parameters: json.RawMessage(`{"type":"object"}`)}}
}
func (f *fakePlugin) Call(_ context.Context, tool string, _ json.RawMessage) (string, error) {
	f.called = tool
	return "pong", nil
}

func TestRegisterAndLoadActive(t *testing.T) {
	Register("fake", func() Plugin { return &fakePlugin{} })
	t.Setenv("ACP_HARNESS_PLUGINS", "fake, unknown ,")
	a := LoadActive()
	if names := a.ActiveNames(); len(names) != 1 || names[0] != "fake" {
		t.Errorf("active names = %v", names)
	}
}

func TestActiveToolsAndDispatch(t *testing.T) {
	Register("fake", func() Plugin { return &fakePlugin{} })
	os.Setenv("ACP_HARNESS_PLUGINS", "fake")
	defer os.Unsetenv("ACP_HARNESS_PLUGINS")
	a := LoadActive()
	tools := a.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	want := ToolName("fake", "ping")
	if tools[0].Function.Name != want {
		t.Errorf("tool name = %q want %q", tools[0].Function.Name, want)
	}
	out, ok, err := a.Dispatch(context.Background(), want, json.RawMessage(`{}`))
	if err != nil || !ok || out != "pong" {
		t.Errorf("dispatch: out=%q ok=%v err=%v", out, ok, err)
	}
	if _, ok, _ := a.Dispatch(context.Background(), "read_file", nil); ok {
		t.Errorf("non-plugin name should not be claimed")
	}
}

func TestDisabledPlugins(t *testing.T) {
	Register("disableme", func() Plugin { return &fakePlugin{} })
	t.Setenv("AGENTBRIDGE_PLUGINS", "disableme")
	t.Setenv("AGENTBRIDGE_DISABLED_PLUGINS", "disableme")
	a := LoadActive()
	if len(a.ActiveNames()) != 0 {
		t.Fatalf("active plugins = %v", a.ActiveNames())
	}
}

func TestSplitToolName(t *testing.T) {
	plug, tool, ok := SplitToolName("plugin__sqlite__query")
	if !ok || plug != "sqlite" || tool != "query" {
		t.Errorf("split = %s/%s/%v", plug, tool, ok)
	}
	if _, _, ok := SplitToolName("not-a-plugin"); ok {
		t.Errorf("should not split non-plugin name")
	}
	if _, _, ok := SplitToolName("plugin__missingTool"); ok {
		t.Errorf("should not split malformed plugin name")
	}
}

func TestAvailableSorted(t *testing.T) {
	Register("zeta", func() Plugin { return &fakePlugin{} })
	Register("alpha", func() Plugin { return &fakePlugin{} })
	names := Available()
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "alpha") || !strings.Contains(joined, "zeta") {
		t.Errorf("Available missing entries: %v", names)
	}
	// Check sort order
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Errorf("Available not sorted: %v", names)
			break
		}
	}
}
