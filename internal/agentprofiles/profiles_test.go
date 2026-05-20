package agentprofiles

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProfilesAndPromptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "foo.md"), []byte("file prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agents.yaml")
	if err := os.WriteFile(path, []byte(`agents:
  - name: foo
    description: Foo agent
    target_model: glm-5.1
    system_prompt: inline prompt
    prompt_file: foo.md
    tools: [read_file, "mcp__search__*"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTBRIDGE_AGENTS_FILE", path)

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "foo" || got[0].TargetModel != "glm-5.1" {
		t.Fatalf("profiles=%#v", got)
	}
	prompt := got[0].Prompt()
	if prompt != "inline prompt\n\nfile prompt" {
		t.Fatalf("prompt=%q", prompt)
	}
}
