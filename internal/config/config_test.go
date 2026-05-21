package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziozzang/agentbridge/internal/provider"
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
	expect := []string{
		"ai-gateway",
		"alibaba",
		"alibaba-coding-plan",
		"amazon-bedrock",
		"amazon-bedrock-mantle",
		"anthropic",
		"arcee",
		"byteplus",
		"byteplus-plan",
		"cerebras",
		"chutes",
		"claude-code",
		"cloudflare-ai-gateway",
		"codex",
		"deepinfra",
		"deepseek",
		"fireworks",
		"github-copilot",
		"glm",
		"gmi",
		"google",
		"google-antigravity",
		"google-vertex",
		"groq",
		"huggingface",
		"kilocode",
		"kimi-coding",
		"kimi-coding-cn",
		"litellm",
		"lmstudio",
		"microsoft-foundry",
		"minimax",
		"minimax-portal",
		"mistral",
		"modelstudio",
		"moonshot",
		"novita",
		"nvidia",
		"ollama",
		"ollama-cloud",
		"openai",
		"openai-responses",
		"opencode-go",
		"opencode-zen",
		"openrouter",
		"qianfan",
		"qwen",
		"qwencloud",
		"router",
		"sglang",
		"stepfun",
		"tencent-tokenhub",
		"together",
		"venice",
		"vllm",
		"volcengine",
		"volcengine-plan",
		"xai",
		"xai-oauth",
		"xiaomi",
		"zai",
	}
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

func TestLoadEmbeddedOllamaAPIKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing"))
	_ = os.Unsetenv("ACP_HARNESS_PROVIDERS_FILE")
	t.Setenv("OLLAMA_API_KEY", "ollama-key")
	t.Setenv("ACP_HARNESS_API_KEY", "")

	m, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := m.Resolve("ollama")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "ollama-key" {
		t.Errorf("key = %q", cfg.APIKey)
	}
}

func TestLoadEmbeddedCodexWebSearchDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing"))
	_ = os.Unsetenv("ACP_HARNESS_PROVIDERS_FILE")
	t.Setenv("CODEX_WEB_SEARCH", "")

	m, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := m.Resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Extra["web_search"] != "cached" {
		t.Fatalf("web_search = %#v", cfg.Extra["web_search"])
	}
	if cfg.Extra["compaction"] != "responses_compact" || cfg.Extra["compact_path"] != "/responses/compact" {
		t.Fatalf("codex compaction defaults = %#v", cfg.Extra)
	}
	tools, ok := cfg.Extra["tools"].(map[string]any)
	if !ok {
		t.Fatalf("tools = %#v", cfg.Extra["tools"])
	}
	if _, ok := tools["web_search"].(map[string]any); !ok {
		t.Fatalf("tools.web_search = %#v", tools["web_search"])
	}
}

func TestLoadEmbeddedOpenAIResponsesCompactionDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing"))
	_ = os.Unsetenv("ACP_HARNESS_PROVIDERS_FILE")
	t.Setenv("OPENAI_COMPACTION", "")

	m, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := m.Resolve("openai-responses")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Extra["responses_path"] != "/v1/responses" {
		t.Fatalf("responses_path = %#v", cfg.Extra["responses_path"])
	}
	if cfg.Extra["compaction"] != "responses_compact" || cfg.Extra["compact_path"] != "/v1/responses/compact" {
		t.Fatalf("openai responses compaction defaults = %#v", cfg.Extra)
	}
}

func TestHermesDerivedProviderDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing"))
	_ = os.Unsetenv("ACP_HARNESS_PROVIDERS_FILE")
	t.Setenv("AGENTBRIDGE_API_KEY", "")
	t.Setenv("ACP_HARNESS_API_KEY", "")

	m, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		baseURL string
		model   string
	}{
		"zai":                 {"https://api.z.ai/api/paas/v4", "glm-5.1"},
		"kimi-coding":         {"https://api.kimi.com/coding/v1", "kimi-k2.6"},
		"deepseek":            {"https://api.deepseek.com/v1", "deepseek-chat"},
		"alibaba":             {"https://dashscope-intl.aliyuncs.com/compatible-mode/v1", "qwen3.6-plus"},
		"alibaba-coding-plan": {"https://coding-intl.dashscope.aliyuncs.com/v1", "qwen3.6-plus"},
		"opencode-go":         {"https://opencode.ai/zen/go/v1", "kimi-k2.6"},
		"huggingface":         {"https://router.huggingface.co/v1", "moonshotai/Kimi-K2.6"},
		"ollama-cloud":        {"https://ollama.com/v1", "gpt-oss:120b"},
	}
	for name, want := range cases {
		cfg, err := m.Resolve(name)
		if err != nil {
			t.Fatalf("Resolve(%s): %v", name, err)
		}
		if cfg.Kind != "openai-chat" {
			t.Errorf("%s kind = %q", name, cfg.Kind)
		}
		if cfg.BaseURL != want.baseURL {
			t.Errorf("%s base url = %q", name, cfg.BaseURL)
		}
		if cfg.DefaultModel != want.model {
			t.Errorf("%s default model = %q", name, cfg.DefaultModel)
		}
	}
}

func TestRouterProviderReceivesNestedProviderConfigs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing"))
	_ = os.Unsetenv("ACP_HARNESS_PROVIDERS_FILE")
	m, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := m.Resolve("router")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Kind != "router" {
		t.Fatalf("kind = %q", cfg.Kind)
	}
	providers, ok := cfg.Extra["_providers"].(map[string]provider.Config)
	if !ok {
		t.Fatalf("nested providers missing: %#v", cfg.Extra["_providers"])
	}
	if providers["zai"].Kind != "openai-chat" || providers["xai"].Kind != "openai-responses" {
		t.Fatalf("bad nested providers: zai=%+v xai=%+v", providers["zai"], providers["xai"])
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

func TestUserConfigFileOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agentbridge")
	_ = os.MkdirAll(cfgPath, 0o755)
	override := `providers:
  router:
    default_model: routed-model
    extra:
      routes:
        - match: routed-model
          provider: glm
          target_model: glm-5.1
`
	if err := os.WriteFile(filepath.Join(cfgPath, "config.yaml"), []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
	_ = os.Unsetenv("ACP_HARNESS_PROVIDERS_FILE")
	m, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := m.Resolve("router")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "routed-model" {
		t.Fatalf("router default model = %q", cfg.DefaultModel)
	}
	routes, ok := cfg.Extra["routes"].([]any)
	if !ok || len(routes) != 1 {
		t.Fatalf("routes = %#v", cfg.Extra["routes"])
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
