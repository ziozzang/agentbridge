// Package router implements a model-name router provider.
//
// It lets AgentBridge behave like a small LiteLLM-style frontend: callers use
// one active provider while the requested model selects the real backend,
// upstream model name, and optionally one of several API keys.
package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/ziozzang/agentbridge/internal/provider"
)

// Kind is the registry key for the router provider.
const Kind = "router"

func init() {
	provider.Register(Kind, func(cfg provider.Config) (provider.Provider, error) {
		return New(cfg)
	})
}

// Client routes each request to another configured provider.
type Client struct {
	cfg       provider.Config
	routes    []route
	providers map[string]provider.Config
	mu        sync.Mutex
	rr        map[int]int
}

type route struct {
	Match         string   `json:"match" yaml:"match"`
	Model         string   `json:"model" yaml:"model"`
	Provider      string   `json:"provider" yaml:"provider"`
	TargetModel   string   `json:"target_model" yaml:"target_model"`
	APIKeys       []string `json:"api_keys" yaml:"api_keys"`
	APIKeyEnvs    []string `json:"api_key_envs" yaml:"api_key_envs"`
	Default       bool     `json:"default" yaml:"default"`
	MaxTokens     int      `json:"max_tokens" yaml:"max_tokens"`
	ContextWindow int      `json:"context_window" yaml:"context_window"`
}

type routeFile struct {
	DefaultModel string  `json:"default_model" yaml:"default_model"`
	Routes       []route `json:"routes" yaml:"routes"`
}

// New constructs a router.
func New(cfg provider.Config) (*Client, error) {
	providers, _ := cfg.Extra["_providers"].(map[string]provider.Config)
	if len(providers) == 0 {
		return nil, errors.New("router: no provider configs were injected")
	}
	routes, defaultModel, err := loadRoutes(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = defaultModel
	}
	if cfg.DefaultModel == "" && len(routes) > 0 {
		cfg.DefaultModel = firstNonEmpty(routes[0].Match, routes[0].Model, routes[0].TargetModel)
	}
	return &Client{cfg: cfg, routes: routes, providers: providers, rr: map[int]int{}}, nil
}

func (c *Client) Name() string { return firstNonEmpty(c.cfg.Name, Kind) }
func (c *Client) Kind() string { return Kind }

func (c *Client) AvailableModels() []provider.ModelInfo {
	if len(c.cfg.Models) > 0 {
		out := make([]provider.ModelInfo, len(c.cfg.Models))
		copy(out, c.cfg.Models)
		return out
	}
	out := make([]provider.ModelInfo, 0, len(c.routes))
	seen := map[string]struct{}{}
	for _, r := range c.routes {
		id := firstNonEmpty(r.Match, r.Model, r.TargetModel)
		if id == "" || strings.Contains(id, "*") {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, provider.ModelInfo{ModelID: id, Name: id})
	}
	return out
}

func (c *Client) DefaultModel() string { return c.cfg.DefaultModel }

func (c *Client) ContextWindow(model string) int {
	r, cfg, target, ok := c.resolve(model)
	if ok {
		if r.ContextWindow > 0 {
			return r.ContextWindow
		}
		p, err := provider.Build(cfg)
		if err == nil {
			return p.ContextWindow(target)
		}
	}
	if c.cfg.ContextWindow > 0 {
		return c.cfg.ContextWindow
	}
	return 128_000
}

func (c *Client) StreamChat(ctx context.Context, messages []provider.Message, opts provider.StreamOptions) (<-chan provider.Chunk, <-chan error) {
	model := opts.Model
	if model == "" {
		model = c.DefaultModel()
	}
	_, cfg, target, ok := c.resolve(model)
	if !ok {
		chunks := make(chan provider.Chunk)
		errs := make(chan error, 1)
		close(chunks)
		errs <- fmt.Errorf("router: no route for model %q", model)
		close(errs)
		return chunks, errs
	}
	p, err := provider.Build(cfg)
	if err != nil {
		chunks := make(chan provider.Chunk)
		errs := make(chan error, 1)
		close(chunks)
		errs <- err
		close(errs)
		return chunks, errs
	}
	opts.Model = target
	return p.StreamChat(ctx, messages, opts)
}

func (c *Client) resolve(model string) (route, provider.Config, string, bool) {
	var fallback *route
	for i := range c.routes {
		r := &c.routes[i]
		if r.Default {
			fallback = r
		}
		if routeMatches(*r, model) {
			cfg, target, ok := c.targetConfig(i, *r, model)
			return *r, cfg, target, ok
		}
	}
	if fallback != nil {
		cfg, target, ok := c.targetConfig(-1, *fallback, model)
		return *fallback, cfg, target, ok
	}
	return route{}, provider.Config{}, "", false
}

func (c *Client) targetConfig(routeIndex int, r route, requested string) (provider.Config, string, bool) {
	cfg, ok := c.providers[r.Provider]
	if !ok {
		return provider.Config{}, "", false
	}
	if r.MaxTokens > 0 {
		cfg.MaxTokens = r.MaxTokens
	}
	if r.ContextWindow > 0 {
		cfg.ContextWindow = r.ContextWindow
	}
	if key := c.pickKey(routeIndex, r); key != "" {
		cfg.APIKey = key
	}
	target := resolveTargetModel(r, requested, cfg.DefaultModel)
	return cfg, target, true
}

func (c *Client) pickKey(routeIndex int, r route) string {
	keys := append([]string{}, r.APIKeys...)
	for _, env := range r.APIKeyEnvs {
		if v := os.Getenv(strings.TrimSpace(env)); v != "" {
			keys = append(keys, v)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	if len(keys) == 1 {
		return keys[0]
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	next := c.rr[routeIndex] % len(keys)
	c.rr[routeIndex] = next + 1
	return keys[next]
}

func routeMatches(r route, model string) bool {
	pat := firstNonEmpty(r.Match, r.Model)
	if pat == "" {
		return r.Default
	}
	return globMatch(pat, model)
}

func resolveTargetModel(r route, requested, fallback string) string {
	target := firstNonEmpty(r.TargetModel, r.Model, requested, fallback)
	if target == "$model" {
		return requested
	}
	if strings.Contains(target, "$1") {
		target = strings.ReplaceAll(target, "$1", wildcardCapture(firstNonEmpty(r.Match, r.Model), requested))
	}
	return target
}

func wildcardCapture(pattern, value string) string {
	i := strings.Index(pattern, "*")
	if i < 0 {
		return ""
	}
	prefix := pattern[:i]
	suffix := pattern[i+1:]
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, suffix) {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(value, prefix), suffix)
}

func globMatch(pattern, value string) bool {
	if pattern == value {
		return true
	}
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return false
	}
	parts := strings.Split(pattern, "*")
	if len(parts) == 2 {
		return strings.HasPrefix(value, parts[0]) && strings.HasSuffix(value, parts[1])
	}
	pos := 0
	for i, p := range parts {
		if p == "" {
			continue
		}
		idx := strings.Index(value[pos:], p)
		if idx < 0 {
			return false
		}
		if i == 0 && idx != 0 {
			return false
		}
		pos += idx + len(p)
	}
	last := parts[len(parts)-1]
	return last == "" || strings.HasSuffix(value, last)
}

func loadRoutes(cfg provider.Config) ([]route, string, error) {
	var routes []route
	if raw, ok := cfg.Extra["routes"]; ok {
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, "", err
		}
		if err := json.Unmarshal(b, &routes); err != nil {
			return nil, "", fmt.Errorf("router: parse routes: %w", err)
		}
	}
	path := firstNonEmpty(strExtra(cfg.Extra, "routes_file"), os.Getenv("AGENTBRIDGE_ROUTER_FILE"))
	if path == "" {
		path = defaultRouteFile()
	}
	defaultModel := ""
	if path != "" {
		more, def, err := loadRouteFile(path)
		if err != nil {
			return nil, "", err
		}
		if def != "" {
			defaultModel = def
		}
		routes = append(routes, more...)
	}
	if len(routes) == 0 {
		return nil, "", errors.New("router: no routes configured")
	}
	return routes, defaultModel, nil
}

func defaultRouteFile() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		p := filepath.Join(dir, "agentbridge", "router.yaml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
		p = filepath.Join(dir, "agentbridge", "router.json")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func loadRouteFile(path string) ([]route, string, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, "", fmt.Errorf("router: read routes file %s: %w", path, err)
	}
	var rf routeFile
	switch {
	case strings.HasSuffix(path, ".yaml"), strings.HasSuffix(path, ".yml"):
		if err := yaml.Unmarshal(data, &rf); err != nil {
			return nil, "", fmt.Errorf("router: parse routes file %s: %w", path, err)
		}
	default:
		if err := json.Unmarshal(data, &rf); err != nil {
			return nil, "", fmt.Errorf("router: parse routes file %s: %w", path, err)
		}
	}
	return rf.Routes, rf.DefaultModel, nil
}

func strExtra(extra map[string]any, key string) string {
	if extra == nil {
		return ""
	}
	if s, ok := extra[key].(string); ok {
		return s
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
