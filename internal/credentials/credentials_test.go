package credentials

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPathHonorsXDGConfigHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	got := Path()
	want := filepath.Join(dir, "glm-acp-agent", "credentials.json")
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestPathFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/tmp/some-home")
	got := Path()
	if filepath.Base(got) != "credentials.json" {
		t.Errorf("Path() basename = %q", filepath.Base(got))
	}
}

func TestWriteAndRead(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := Write("super-secret-key-9999", ""); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := ReadFromFile("")
	if err != nil {
		t.Fatalf("ReadFromFile: %v", err)
	}
	if got != "super-secret-key-9999" {
		t.Errorf("ReadFromFile = %q", got)
	}

	// Verify file mode
	info, err := os.Stat(Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" {
		if info.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %o, want 0600", info.Mode().Perm())
		}
	}

	// Validate JSON shape
	raw, _ := os.ReadFile(Path())
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if body["z_ai_api_key"] != "super-secret-key-9999" {
		t.Errorf("unexpected json body: %v", body)
	}
}

func TestWriteRejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := Write("", ""); err == nil {
		t.Error("expected error for empty key")
	}
}

func TestReadMissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	got, err := ReadFromFile(filepath.Join(dir, "nope.json"))
	if err != nil || got != "" {
		t.Errorf("got (%q,%v), want (\"\", nil)", got, err)
	}
}

func TestReadMalformedFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "creds.json")
	_ = os.WriteFile(p, []byte("not-json"), 0o600)
	got, err := ReadFromFile(p)
	if err != nil || got != "" {
		t.Errorf("got (%q,%v)", got, err)
	}
}

func TestResolvePrefersEnvOverFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("Z_AI_API_KEY", "env-key")
	if err := Write("file-key", ""); err != nil {
		t.Fatal(err)
	}
	if got := Resolve(); got != "env-key" {
		t.Errorf("Resolve() = %q, want env-key", got)
	}
}

func TestResolveFallsBackToFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("Z_AI_API_KEY", "")
	if err := Write("file-key", ""); err != nil {
		t.Fatal(err)
	}
	if got := Resolve(); got != "file-key" {
		t.Errorf("Resolve() = %q, want file-key", got)
	}
}

func TestResolveEmptyWhenNothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("Z_AI_API_KEY", "")
	if got := Resolve(); got != "" {
		t.Errorf("Resolve() = %q, want empty", got)
	}
}
