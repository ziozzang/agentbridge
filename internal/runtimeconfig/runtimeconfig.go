package runtimeconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server     Server     `yaml:"server"`
	Compaction Compaction `yaml:"compaction"`
	PII        PII        `yaml:"pii"`
	Sanitize   Sanitize   `yaml:"sanitize"`
	Cache      Cache      `yaml:"cache"`
	Inject     []Inject   `yaml:"inject"`
}

type Server struct {
	Enabled    bool   `yaml:"enabled"`
	Listen     string `yaml:"listen"`
	PoolSize   int    `yaml:"pool_size"`
	WaitSize   *int   `yaml:"wait_size"`
	HTTPListen string `yaml:"http_listen"`
	GRPCListen string `yaml:"grpc_listen"`
}

type Compaction struct {
	Enabled           *bool    `yaml:"enabled"`
	Native            *bool    `yaml:"native"`
	Summary           *bool    `yaml:"summary"`
	PruneFallback     *bool    `yaml:"prune_fallback"`
	ThresholdPct      *float64 `yaml:"threshold_pct"`
	TargetPct         *float64 `yaml:"target_pct"`
	OverflowTargetPct *float64 `yaml:"overflow_target_pct"`
	PreserveTurns     int      `yaml:"preserve_turns"`
	KeepRecentTokens  int      `yaml:"keep_recent_tokens"`
	ReserveTokens     int      `yaml:"reserve_tokens"`
}

type PII struct {
	Enabled         bool         `yaml:"enabled"`
	Mask            *bool        `yaml:"mask"`
	DisableDefaults bool         `yaml:"disable_defaults"`
	Patterns        []PIIPattern `yaml:"patterns"`
	Routing         PIIRouting   `yaml:"routing"`
}

type PIIPattern struct {
	Name  string `yaml:"name"`
	Regex string `yaml:"regex"`
	Mask  string `yaml:"mask"`
}

type PIIRouting struct {
	RouteTo       string `yaml:"route_to"`
	Reject        bool   `yaml:"reject"`
	RejectMessage string `yaml:"reject_message"`
}

type Sanitize struct {
	StripThinkTags bool     `yaml:"strip_think_tags"`
	Tags           []string `yaml:"tags"`
}

type Cache struct {
	Enabled bool     `yaml:"enabled"`
	TTL     string   `yaml:"ttl"`
	MaxSize int      `yaml:"max_size"`
	Models  []string `yaml:"models_to_cache"`
}

type Inject struct {
	When             string              `yaml:"when"`
	Set              map[string]any      `yaml:"set"`
	Prepend          []map[string]string `yaml:"prepend_messages"`
	SystemPrompt     string              `yaml:"system_prompt"`
	SystemPromptMode string              `yaml:"system_prompt_mode"`
	UserPrefix       string              `yaml:"user_prefix"`
	UserSuffix       string              `yaml:"user_suffix"`
	Remove           []string            `yaml:"remove"`
	RequestRegex     []RegexEdit         `yaml:"request_regex"`
}

type RegexEdit struct {
	Pattern string   `yaml:"pattern"`
	Replace string   `yaml:"replace"`
	Roles   []string `yaml:"roles"`
}

func Defaults() Config {
	return Config{Server: Server{Listen: "127.0.0.1:8765", PoolSize: 4}}
}

func Load() (Config, error) {
	cfg := Defaults()
	path := firstNonEmpty(os.Getenv("AGENTBRIDGE_CONFIG_FILE"), os.Getenv("ACP_HARNESS_CONFIG_FILE"))
	if path == "" {
		path = defaultPath()
	}
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("runtimeconfig: read %s: %w", path, err)
	}
	expanded := os.ExpandEnv(string(data))
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return cfg, fmt.Errorf("runtimeconfig: parse %s: %w", path, err)
	}
	if cfg.Server.Listen == "" {
		cfg.Server.Listen = "127.0.0.1:8765"
	}
	if cfg.Server.PoolSize == 0 {
		cfg.Server.PoolSize = 4
	}
	return cfg, nil
}

func defaultPath() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		p := filepath.Join(dir, "agentbridge", "config.yaml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
