package agentprofiles

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Agents []Profile `json:"agents" yaml:"agents"`
}

type Profile struct {
	Name         string   `json:"name" yaml:"name"`
	Description  string   `json:"description" yaml:"description"`
	TargetModel  string   `json:"target_model" yaml:"target_model"`
	SystemPrompt string   `json:"system_prompt" yaml:"system_prompt"`
	PromptFile   string   `json:"prompt_file" yaml:"prompt_file"`
	Tools        []string `json:"tools" yaml:"tools"`
	baseDir      string
}

func Load() ([]Profile, error) {
	path := firstNonEmpty(os.Getenv("AGENTBRIDGE_AGENTS_FILE"), os.Getenv("AGENTBRIDGE_AGENT_PROFILES_FILE"))
	if path == "" {
		path = defaultPath()
	}
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("agentprofiles: read %s: %w", path, err)
	}
	var cfg Config
	switch {
	case strings.HasSuffix(path, ".yaml"), strings.HasSuffix(path, ".yml"):
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("agentprofiles: parse %s: %w", path, err)
		}
	default:
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("agentprofiles: parse %s: %w", path, err)
		}
	}
	base := filepath.Dir(path)
	out := make([]Profile, 0, len(cfg.Agents))
	for _, p := range cfg.Agents {
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" {
			continue
		}
		p.TargetModel = strings.TrimSpace(p.TargetModel)
		p.baseDir = base
		out = append(out, p)
	}
	return out, nil
}

func (p Profile) Prompt() string {
	parts := []string{}
	if s := strings.TrimSpace(p.SystemPrompt); s != "" {
		parts = append(parts, s)
	}
	if p.PromptFile != "" {
		path := os.ExpandEnv(p.PromptFile)
		if !filepath.IsAbs(path) {
			path = filepath.Join(p.baseDir, path)
		}
		if body, err := os.ReadFile(path); err == nil {
			parts = append(parts, strings.TrimSpace(string(body)))
		}
	}
	return strings.Join(parts, "\n\n")
}

func defaultPath() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		for _, name := range []string{"agents.yaml", "agents.json"} {
			p := filepath.Join(dir, "agentbridge", name)
			if _, err := os.Stat(p); err == nil {
				return p
			}
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
