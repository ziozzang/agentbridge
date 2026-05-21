package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListAndLoadSkills(t *testing.T) {
	cwd := t.TempDir()
	projectDir := filepath.Join(cwd, ".agentbridge", "skills")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "repo.md"), []byte("repo skill"), 0o600); err != nil {
		t.Fatal(err)
	}

	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	userDir := filepath.Join(xdg, "agentbridge", "skills")
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "user.md"), []byte("user skill"), 0o600); err != nil {
		t.Fatal(err)
	}

	list, err := List(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("skills = %+v", list)
	}
	got, err := Load(cwd, "repo")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "repo" || got.Body != "repo skill" || got.Hash == "" {
		t.Fatalf("loaded = %+v", got)
	}
}

func TestLoadRejectsTraversal(t *testing.T) {
	if _, err := Load(t.TempDir(), "../x"); err == nil {
		t.Fatal("expected traversal-like name to be rejected")
	}
}
