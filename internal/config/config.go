// Package config loads AgentBridge's layered configuration.
//
// Sources (later wins):
//  1. Embedded defaults (internal/config/providers.yaml).
//  2. User file at $XDG_CONFIG_HOME/agentbridge/providers.yaml (or
//     ~/.config/agentbridge/providers.yaml), with acp-harness as a legacy fallback.
//  3. Override file specified by AGENTBRIDGE_PROVIDERS_FILE
//     or legacy ACP_HARNESS_PROVIDERS_FILE.
//  4. Environment variable expansion (${VAR[:-default]}) applied at load
//     time over the merged YAML before unmarshalling.
//
// The active provider is selected by AGENTBRIDGE_PROVIDER (default: "glm"
// for back-compat with the original GLM port). ACP_HARNESS_PROVIDER remains
// a supported alias.
package config

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ziozzang/agentbridge/internal/provider"
)

//go:embed providers.yaml
var embeddedProvidersYAML []byte

// rawConfig is the YAML schema. It mirrors provider.Config but with
// snake_case field names and a "models" entry list using nested keys.
type rawConfig struct {
	Providers map[string]rawProvider `yaml:"providers"`
}

type rawProvider struct {
	Kind          string            `yaml:"kind"`
	BaseURL       string            `yaml:"base_url"`
	APIKey        string            `yaml:"api_key"`
	AuthHeader    string            `yaml:"auth_header"`
	AuthPrefix    string            `yaml:"auth_prefix"`
	DefaultModel  string            `yaml:"default_model"`
	MaxTokens     int               `yaml:"max_tokens"`
	ContextWindow int               `yaml:"context_window"`
	Thinking      string            `yaml:"thinking"`
	Headers       map[string]string `yaml:"headers"`
	Extra         map[string]any    `yaml:"extra"`
	Models        []rawModel        `yaml:"models"`
}

type rawModel struct {
	ID            string         `yaml:"id"`
	Name          string         `yaml:"name"`
	Description   string         `yaml:"description"`
	Provider      string         `yaml:"provider"`
	API           string         `yaml:"api"`
	BaseURL       string         `yaml:"base_url"`
	Input         []string       `yaml:"input"`
	Reasoning     *bool          `yaml:"reasoning"`
	ContextWindow int            `yaml:"context_window"`
	ContextTokens int            `yaml:"context_tokens"`
	MaxTokens     int            `yaml:"max_tokens"`
	Status        string         `yaml:"status"`
	StatusReason  string         `yaml:"status_reason"`
	Replaces      []string       `yaml:"replaces"`
	ReplacedBy    string         `yaml:"replaced_by"`
	Aliases       []string       `yaml:"aliases"`
	Tags          []string       `yaml:"tags"`
	Compat        map[string]any `yaml:"compat"`
	Cost          map[string]any `yaml:"cost"`
}

// Manifest is the parsed, post-expansion configuration.
type Manifest struct {
	Providers map[string]provider.Config
}

// Load reads embedded + user + override YAML, expands ${VAR[:-default]}
// references against the current environment, and returns the resulting
// manifest. An error is returned only if all sources fail; a missing user /
// override file is not an error.
func Load() (*Manifest, error) {
	merged, err := readMergedYAML()
	if err != nil {
		return nil, err
	}
	expanded := expandEnv(merged, os.Getenv)
	var raw rawConfig
	if err := yaml.Unmarshal([]byte(expanded), &raw); err != nil {
		return nil, fmt.Errorf("config: parse: %w", err)
	}
	out := &Manifest{Providers: map[string]provider.Config{}}
	for name, p := range raw.Providers {
		out.Providers[name] = convertProvider(name, p)
	}
	for name, cfg := range out.Providers {
		if cfg.Kind != "router" {
			continue
		}
		if cfg.Extra == nil {
			cfg.Extra = map[string]any{}
		}
		providers := map[string]provider.Config{}
		for nestedName, nested := range out.Providers {
			if nestedName == name {
				continue
			}
			providers[nestedName] = applyPerProviderOverrides(nestedName, nested)
		}
		cfg.Extra["_providers"] = providers
		out.Providers[name] = cfg
	}
	return out, nil
}

func convertProvider(name string, p rawProvider) provider.Config {
	cfg := provider.Config{
		Name:          name,
		Kind:          p.Kind,
		BaseURL:       p.BaseURL,
		APIKey:        p.APIKey,
		AuthHeader:    p.AuthHeader,
		AuthPrefix:    p.AuthPrefix,
		DefaultModel:  p.DefaultModel,
		MaxTokens:     p.MaxTokens,
		ContextWindow: p.ContextWindow,
		Thinking:      p.Thinking,
		Headers:       p.Headers,
		Extra:         p.Extra,
	}
	for _, m := range p.Models {
		cfg.Models = append(cfg.Models, provider.ModelInfo{
			ModelID:       m.ID,
			Name:          m.Name,
			Description:   m.Description,
			Provider:      firstNonEmpty(m.Provider, name),
			API:           m.API,
			BaseURL:       m.BaseURL,
			Input:         m.Input,
			Reasoning:     m.Reasoning,
			ContextWindow: m.ContextWindow,
			ContextTokens: m.ContextTokens,
			MaxTokens:     m.MaxTokens,
			Status:        m.Status,
			StatusReason:  m.StatusReason,
			Replaces:      m.Replaces,
			ReplacedBy:    m.ReplacedBy,
			Aliases:       m.Aliases,
			Tags:          m.Tags,
			Compat:        m.Compat,
			Cost:          m.Cost,
		})
	}
	return cfg
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func readMergedYAML() ([]byte, error) {
	// Start from the embedded defaults so user files only need to overlay
	// the fields they want to change.
	var merged map[string]any
	if err := yaml.Unmarshal(embeddedProvidersYAML, &merged); err != nil {
		return nil, fmt.Errorf("config: embedded defaults: %w", err)
	}

	for _, user := range userConfigPaths() {
		if data, err := os.ReadFile(user); err == nil {
			var u map[string]any
			if err := yaml.Unmarshal(data, &u); err == nil {
				deepMerge(merged, u)
			}
		}
	}

	if override := envFirst("AGENTBRIDGE_CONFIG_FILE", "ACP_HARNESS_CONFIG_FILE"); override != "" {
		data, err := os.ReadFile(override)
		if err != nil {
			return nil, fmt.Errorf("config: open %s: %w", override, err)
		}
		var u map[string]any
		if err := yaml.Unmarshal(data, &u); err != nil {
			return nil, fmt.Errorf("config: parse %s: %w", override, err)
		}
		deepMerge(merged, u)
	}

	// Override file from env
	if override := envFirst("AGENTBRIDGE_PROVIDERS_FILE", "ACP_HARNESS_PROVIDERS_FILE"); override != "" {
		data, err := os.ReadFile(override)
		if err != nil {
			return nil, fmt.Errorf("config: open %s: %w", override, err)
		}
		var u map[string]any
		if err := yaml.Unmarshal(data, &u); err != nil {
			return nil, fmt.Errorf("config: parse %s: %w", override, err)
		}
		deepMerge(merged, u)
	}

	out, err := yaml.Marshal(merged)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func userConfigPaths() []string {
	if h := os.Getenv("XDG_CONFIG_HOME"); h != "" {
		return []string{
			filepath.Join(h, "agentbridge", "config.yaml"),
			filepath.Join(h, "agentbridge", "providers.yaml"),
			filepath.Join(h, "acp-harness", "config.yaml"),
			filepath.Join(h, "acp-harness", "providers.yaml"),
		}
	}
	if h, err := os.UserHomeDir(); err == nil {
		return []string{
			filepath.Join(h, ".config", "agentbridge", "config.yaml"),
			filepath.Join(h, ".config", "agentbridge", "providers.yaml"),
			filepath.Join(h, ".config", "acp-harness", "config.yaml"),
			filepath.Join(h, ".config", "acp-harness", "providers.yaml"),
		}
	}
	return nil
}

// deepMerge recursively overlays src onto dst. Maps merge key-by-key,
// non-map values in src replace those in dst.
func deepMerge(dst, src map[string]any) {
	for k, v := range src {
		if dv, ok := dst[k]; ok {
			if dm, ok := dv.(map[string]any); ok {
				if sm, ok := v.(map[string]any); ok {
					deepMerge(dm, sm)
					continue
				}
			}
		}
		dst[k] = v
	}
}

// expandEnv replaces ${VAR} and ${VAR:-default} markers in s using getenv.
// Defaults may contain nested expansions, e.g. ${A:-${B}}.
func expandEnv(s []byte, getenv func(string) string) string {
	return expandEnvString(string(s), getenv, 0)
}

func expandEnvString(in string, getenv func(string) string, depth int) string {
	if depth > 10 {
		return in
	}
	var b strings.Builder
	for i := 0; i < len(in); {
		if i+2 > len(in) || in[i] != '$' || in[i+1] != '{' {
			b.WriteByte(in[i])
			i++
			continue
		}
		start := i
		i += 2
		if i >= len(in) || !isEnvNameStart(in[i]) {
			b.WriteString(in[start:i])
			continue
		}
		nameStart := i
		i++
		for i < len(in) && isEnvNameChar(in[i]) {
			i++
		}
		name := in[nameStart:i]
		def := ""
		switch {
		case i < len(in) && in[i] == '}':
			i++
		case i+2 < len(in) && in[i] == ':' && in[i+1] == '-':
			i += 2
			defStart := i
			nested := 0
			for i < len(in) {
				if i+1 < len(in) && in[i] == '$' && in[i+1] == '{' {
					nested++
					i += 2
					continue
				}
				if in[i] == '}' {
					if nested == 0 {
						def = in[defStart:i]
						i++
						break
					}
					nested--
				}
				i++
			}
			if i > len(in) || (i == len(in) && (len(in) == 0 || in[len(in)-1] != '}')) {
				b.WriteString(in[start:])
				return b.String()
			}
		default:
			b.WriteString(in[start:i])
			continue
		}
		if v := getenv(name); v != "" {
			b.WriteString(v)
		} else {
			b.WriteString(expandEnvString(def, getenv, depth+1))
		}
	}
	return b.String()
}

func isEnvNameStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isEnvNameChar(c byte) bool {
	return isEnvNameStart(c) || (c >= '0' && c <= '9')
}

// SelectedProviderName returns the user's chosen provider name, falling
// back to "glm" for back-compat with the original port.
func SelectedProviderName() string {
	if v := envFirst("AGENTBRIDGE_PROVIDER", "ACP_HARNESS_PROVIDER"); v != "" {
		return v
	}
	if v := os.Getenv("ACP_PROVIDER"); v != "" {
		return v
	}
	return "glm"
}

// Resolve returns the provider.Config for the active provider, applying
// trailing env overrides for the model and per-provider API key. The
// returned config is ready to pass to provider.Build.
func (m *Manifest) Resolve(name string) (provider.Config, error) {
	if name == "" {
		name = SelectedProviderName()
	}
	cfg, ok := m.Providers[name]
	if !ok {
		return provider.Config{}, fmt.Errorf("config: unknown provider %q (known: %v)", name, m.Names())
	}
	// Late env overrides (these take precedence over YAML).
	if v := envFirst("AGENTBRIDGE_MODEL", "ACP_HARNESS_MODEL"); v != "" {
		cfg.DefaultModel = v
	}
	if v := envFirst("AGENTBRIDGE_BASE_URL", "ACP_HARNESS_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := envFirst("AGENTBRIDGE_API_KEY", "ACP_HARNESS_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	cfg = applyPerProviderOverrides(name, cfg)
	return cfg, nil
}

func applyPerProviderOverrides(name string, cfg provider.Config) provider.Config {
	// Per-provider key override: AGENTBRIDGE_<UPPER>_API_KEY, with
	// ACP_HARNESS_<UPPER>_API_KEY retained as an alias.
	suffix := strings.ToUpper(strings.NewReplacer("-", "_").Replace(name)) + "_API_KEY"
	if v := envFirst("AGENTBRIDGE_"+suffix, "ACP_HARNESS_"+suffix); v != "" {
		cfg.APIKey = v
	}
	return cfg
}

func envFirst(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

// Names returns the sorted list of configured provider names.
func (m *Manifest) Names() []string {
	out := make([]string, 0, len(m.Providers))
	for k := range m.Providers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
