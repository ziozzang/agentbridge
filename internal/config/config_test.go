package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEmbeddedDefaults(t *testing.T) {
	// Make sure no env vars leak into expansion.
	t.Setenv("Z_AI_API_KEY", "")
	t.Setenv("ACP_HARNESS_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ACP_GLM_MODEL", "")
	t.Setenv("ACP_HARNESS_MODEL", "")
	t.Setenv("ACP_HARNESS_BASE_URL", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing"))
	_ = os.Unsetenv("ACP_HARNESS_PROVIDERS_FILE")

	m, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	expect := []string{"anthropic", "codex", "glm", "litellm", "ollama", "openai", "openai-responses", "openrouter"}
	got := m.Names()
	if strings.Join(got, ",") != strings.Join(expect, ",") {
		t.Errorf("providers: %v", got)
	}
	// GLM should fall back to its default model.
	cfg, err := m.Resolve("glm")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "glm-5.1" {
		t.Errorf("default model = %q", cfg.DefaultModel)
	}
	if cfg.BaseURL != "https://api.z.ai/api/coding/paas/v4" {
		t.Errorf("base url = %q", cfg.BaseURL)
	}
}

func TestExpandEnvHonorsDefaults(t *testing.T) {
	got := expandEnv([]byte(`a: ${FOO:-bar}`), func(string) string { return "" })
	if !strings.Contains(got, "a: bar") {
		t.Errorf("default not expanded: %q", got)
	}
	got = expandEnv([]byte(`a: ${FOO}`), func(string) string { return "baz" })
	if !strings.Contains(got, "a: baz") {
		t.Errorf("var not expanded: %q", got)
	}
}

func TestExpandEnvHonorsNestedDefaults(t *testing.T) {
	env := map[string]string{"FALLBACK": "secret"}
	got := expandEnv([]byte(`a: ${PRIMARY:-${FALLBACK}}`), func(k string) string {
		return env[k]
	})
	if !strings.Contains(got, "a: secret") {
		t.Errorf("nested default not expanded: %q", got)
	}
	if strings.Contains(got, "}") {
		t.Errorf("nested default left trailing brace: %q", got)
	}

	env["PRIMARY"] = "primary"
	got = expandEnv([]byte(`a: ${PRIMARY:-${FALLBACK}}`), func(k string) string {
		return env[k]
	})
	if !strings.Contains(got, "a: primary") {
		t.Errorf("primary var not preferred: %q", got)
	}
}

func TestLoadEmbeddedGLMKeyFromNestedDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing"))
	_ = os.Unsetenv("ACP_HARNESS_PROVIDERS_FILE")
	t.Setenv("Z_AI_API_KEY", "")
	t.Setenv("ACP_HARNESS_API_KEY", "fallback-key")

	m, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := m.Resolve("glm")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "fallback-key" {
		t.Errorf("key = %q", cfg.APIKey)
	}
}

func TestResolveAppliesEnvOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing"))
	_ = os.Unsetenv("ACP_HARNESS_PROVIDERS_FILE")
	t.Setenv("ACP_HARNESS_MODEL", "override-model")
	t.Setenv("ACP_HARNESS_API_KEY", "override-key")
	m, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := m.Resolve("glm")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "override-model" {
		t.Errorf("model = %q", cfg.DefaultModel)
	}
	if cfg.APIKey != "override-key" {
		t.Errorf("key = %q", cfg.APIKey)
	}
}

func TestPerProviderAPIKeyOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing"))
	_ = os.Unsetenv("ACP_HARNESS_PROVIDERS_FILE")
	t.Setenv("ACP_HARNESS_OPENAI_RESPONSES_API_KEY", "or-secret")
	m, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := m.Resolve("openai-responses")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "or-secret" {
		t.Errorf("key = %q", cfg.APIKey)
	}
}

func TestUserProvidersFileOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "acp-harness")
	_ = os.MkdirAll(cfgPath, 0o755)
	override := `providers:
  glm:
    default_model: custom-glm
  myprov:
    kind: openai-chat
    base_url: https://example.com/v1
    api_key: hello
    default_model: my-model
`
	if err := os.WriteFile(filepath.Join(cfgPath, "providers.yaml"), []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
	_ = os.Unsetenv("ACP_HARNESS_PROVIDERS_FILE")
	_ = os.Unsetenv("ACP_HARNESS_MODEL")
	m, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	glm, err := m.Resolve("glm")
	if err != nil {
		t.Fatal(err)
	}
	if glm.DefaultModel != "custom-glm" {
		t.Errorf("glm overrides not applied: %q", glm.DefaultModel)
	}
	if glm.BaseURL == "" {
		t.Errorf("glm base url lost after merge")
	}
	custom, err := m.Resolve("myprov")
	if err != nil {
		t.Fatal(err)
	}
	if custom.Kind != "openai-chat" || custom.BaseURL != "https://example.com/v1" || custom.DefaultModel != "my-model" {
		t.Errorf("user provider not loaded: %+v", custom)
	}
}

func TestResolveSelectsDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing"))
	_ = os.Unsetenv("ACP_HARNESS_PROVIDERS_FILE")
	_ = os.Unsetenv("ACP_HARNESS_PROVIDER")
	_ = os.Unsetenv("ACP_PROVIDER")
	if SelectedProviderName() != "glm" {
		t.Errorf("default = %q", SelectedProviderName())
	}
	t.Setenv("ACP_HARNESS_PROVIDER", "anthropic")
	if SelectedProviderName() != "anthropic" {
		t.Errorf("env not honoured")
	}
}
