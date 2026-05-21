package definitions

import (
	"encoding/json"
	"testing"
)

func TestAllNonEmptyAndDistinct(t *testing.T) {
	tools := All()
	if len(tools) != 7 {
		t.Errorf("expected 7 tools, got %d", len(tools))
	}
	seen := map[string]bool{}
	for _, tool := range tools {
		if tool.Type != "function" {
			t.Errorf("tool %s has type %q", tool.Function.Name, tool.Type)
		}
		if seen[tool.Function.Name] {
			t.Errorf("duplicate tool: %s", tool.Function.Name)
		}
		seen[tool.Function.Name] = true
		var params map[string]any
		if err := json.Unmarshal(tool.Function.Parameters, &params); err != nil {
			t.Errorf("tool %s: invalid parameters JSON: %v", tool.Function.Name, err)
		}
		if params["type"] != "object" {
			t.Errorf("tool %s: parameters.type != object", tool.Function.Name)
		}
	}
}

func TestByName(t *testing.T) {
	if got := ByName("read_file"); got == nil || got.Function.Name != "read_file" {
		t.Errorf("ByName(read_file) = %v", got)
	}
	if got := ByName("nonexistent"); got != nil {
		t.Errorf("ByName(nonexistent) = %v, want nil", got)
	}
}

func TestFilterPreservesOrder(t *testing.T) {
	got := Filter([]string{"web_reader", "read_file"})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Function.Name != "read_file" || got[1].Function.Name != "web_reader" {
		t.Errorf("order wrong: %v", []string{got[0].Function.Name, got[1].Function.Name})
	}
}

func TestExpectedToolNamesPresent(t *testing.T) {
	want := []string{"read_file", "write_file", "list_files", "web_search", "web_reader", "image_analysis", "client_run_lua"}
	for _, n := range want {
		if ByName(n) == nil {
			t.Errorf("missing tool: %s", n)
		}
	}
}
