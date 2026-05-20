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

	codexoauth "github.com/ziozzang/agentbridge/internal/oauth/codex"
	xaioauth "github.com/ziozzang/agentbridge/internal/oauth/xai"
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
	cfg        provider.Config
	routes     []route
	providers  map[string]provider.Config
	aliases    map[string]string
	mu         sync.Mutex
	rr         map[int]int
	limitedKey map[string]limitInfo
}

type route struct {
	Match           string         `json:"match" yaml:"match"`
	Model           string         `json:"model" yaml:"model"`
	Models          stringsList    `json:"models" yaml:"models"`
	Provider        string         `json:"provider" yaml:"provider"`
	TargetModel     string         `json:"target_model" yaml:"target_model"`
	Aliases         stringsList    `json:"aliases" yaml:"aliases"`
	Fallbacks       []route        `json:"fallbacks" yaml:"fallbacks"`
	RequestDefaults map[string]any `json:"request_defaults" yaml:"request_defaults"`
	APIKeys         stringsList    `json:"api_keys" yaml:"api_keys"`
	APIKeyEnvs      stringsList    `json:"api_key_envs" yaml:"api_key_envs"`
	Default         bool           `json:"default" yaml:"default"`
	MaxTokens       int            `json:"max_tokens" yaml:"max_tokens"`
	ContextWindow   int            `json:"context_window" yaml:"context_window"`
	RetryKeys       bool           `json:"retry_keys" yaml:"retry_keys"`
}

type routeFile struct {
	DefaultModel string            `json:"default_model" yaml:"default_model"`
	Aliases      map[string]string `json:"aliases" yaml:"aliases"`
	Routes       []route           `json:"routes" yaml:"routes"`
}

type routeSet struct {
	routes  []route
	aliases map[string]string
}

// New constructs a router.
func New(cfg provider.Config) (*Client, error) {
	providers, _ := cfg.Extra["_providers"].(map[string]provider.Config)
	if len(providers) == 0 {
		return nil, errors.New("router: no provider configs were injected")
	}
	loaded, defaultModel, err := loadRoutes(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = defaultModel
	}
	if cfg.DefaultModel == "" && len(loaded.routes) > 0 {
		cfg.DefaultModel = firstNonEmpty(loaded.routes[0].Match, loaded.routes[0].Model, loaded.routes[0].TargetModel)
	}
	return &Client{cfg: cfg, routes: loaded.routes, aliases: loaded.aliases, providers: providers, rr: map[int]int{}, limitedKey: map[string]limitInfo{}}, nil
}

func (c *Client) Name() string { return firstNonEmpty(c.cfg.Name, Kind) }
func (c *Client) Kind() string { return Kind }

func (c *Client) AvailableModels() []provider.ModelInfo {
	seen := map[string]struct{}{}
	out := make([]provider.ModelInfo, 0, len(c.cfg.Models)+len(c.routes))
	if len(c.cfg.Models) > 0 {
		for _, m := range c.cfg.Models {
			if m.ModelID == "" {
				continue
			}
			if m.Provider == "" {
				m.Provider = Kind
			}
			seen[m.ModelID] = struct{}{}
			out = append(out, m)
		}
	}
	for _, r := range c.routes {
		if cfg, ok := c.providers[r.Provider]; ok {
			for _, m := range cfg.Models {
				if m.ModelID == "" {
					continue
				}
				if _, ok := seen[m.ModelID]; ok {
					continue
				}
				if m.Provider == "" {
					m.Provider = cfg.Name
				}
				if m.Description == "" && cfg.Name != "" {
					m.Description = "provider: " + cfg.Name
				}
				seen[m.ModelID] = struct{}{}
				out = append(out, m)
			}
		}
		if strings.Contains(r.primaryPattern(), "*") {
			continue
		}
		id := firstNonEmpty(r.Match, r.Model, r.TargetModel)
		if id == "" || strings.Contains(id, "*") {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, provider.ModelInfo{ModelID: id, Name: id, Provider: firstNonEmpty(r.Provider, Kind)})
	}
	return out
}

func (c *Client) DefaultModel() string { return c.cfg.DefaultModel }

func (c *Client) ContextWindow(model string) int {
	model = c.expandAlias(model)
	chain, ok := c.resolveChain(model)
	if ok && len(chain) > 0 {
		first := chain[0]
		cfg, target, _, ok := c.targetConfig(first.index, first.route, model)
		if ok && first.route.ContextWindow > 0 {
			return first.route.ContextWindow
		}
		if ok {
			p, err := provider.Build(cfg)
			if err == nil {
				return p.ContextWindow(target)
			}
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
	model = c.expandAlias(model)
	chain, ok := c.resolveChain(model)
	if !ok {
		chunks := make(chan provider.Chunk)
		errs := make(chan error, 1)
		close(chunks)
		errs <- fmt.Errorf("router: no route for model %q", model)
		close(errs)
		return chunks, errs
	}
	return c.streamChain(ctx, chain, model, messages, opts)
}

type resolvedRoute struct {
	index int
	route route
}

func (c *Client) resolveChain(model string) ([]resolvedRoute, bool) {
	var fallback *route
	fallbackIndex := -1
	for i := range c.routes {
		r := &c.routes[i]
		if r.Default {
			fallback = r
			fallbackIndex = i
		}
		if routeMatches(*r, model) {
			return buildChain(i, *r), true
		}
	}
	if fallback != nil {
		return buildChain(fallbackIndex, *fallback), true
	}
	return nil, false
}

func buildChain(index int, r route) []resolvedRoute {
	out := []resolvedRoute{{index: index, route: r}}
	for i, fb := range r.Fallbacks {
		fb.normalize()
		out = append(out, resolvedRoute{index: fallbackRouteIndex(index, i), route: fb})
	}
	return out
}

func fallbackRouteIndex(parent, child int) int {
	return -((parent+1)*1000 + child + 1)
}

func (c *Client) expandAlias(model string) string {
	seen := map[string]struct{}{}
	for {
		next, ok := c.aliases[model]
		if !ok || next == "" {
			return model
		}
		if _, loop := seen[model]; loop {
			return model
		}
		seen[model] = struct{}{}
		model = next
	}
}

func (c *Client) targetConfig(routeIndex int, r route, requested string) (provider.Config, string, string, bool) {
	cfg, ok := c.providers[r.Provider]
	if !ok {
		return provider.Config{}, "", "", false
	}
	if r.MaxTokens > 0 {
		cfg.MaxTokens = r.MaxTokens
	}
	if r.ContextWindow > 0 {
		cfg.ContextWindow = r.ContextWindow
	}
	if len(r.RequestDefaults) > 0 {
		if cfg.Extra == nil {
			cfg.Extra = map[string]any{}
		}
		cfg.Extra["request_defaults"] = mergeMaps(asMap(cfg.Extra["request_defaults"]), r.RequestDefaults)
	}
	key, keySig := c.pickKey(routeIndex, r)
	if key != "" {
		cfg.APIKey = key
	}
	target := resolveTargetModel(r, requested, cfg.DefaultModel)
	return cfg, target, keySig, true
}

func (c *Client) pickKey(routeIndex int, r route) (string, string) {
	keys := append([]string{}, r.APIKeys...)
	for _, env := range r.APIKeyEnvs {
		if v := os.Getenv(strings.TrimSpace(env)); v != "" {
			keys = append(keys, v)
		}
	}
	if len(keys) == 0 {
		return "", ""
	}
	if len(keys) == 1 {
		return keys[0], keySignature(routeIndex, 0, keys[0])
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for tries := 0; tries < len(keys); tries++ {
		next := c.rr[routeIndex] % len(keys)
		c.rr[routeIndex] = next + 1
		sig := keySignature(routeIndex, next, keys[next])
		if _, limited := c.limitedKey[sig]; !limited {
			return keys[next], sig
		}
	}
	next := c.rr[routeIndex] % len(keys)
	c.rr[routeIndex] = next + 1
	return keys[next], keySignature(routeIndex, next, keys[next])
}

func (c *Client) streamChain(ctx context.Context, chain []resolvedRoute, requested string, messages []provider.Message, opts provider.StreamOptions) (<-chan provider.Chunk, <-chan error) {
	out := make(chan provider.Chunk)
	errs := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errs)
		var lastErr error
		for _, candidate := range chain {
			r := candidate.route
			attempts := 1
			if r.RetryKeys && len(r.keys()) > 1 {
				attempts = len(r.keys())
			}
			for attempt := 0; attempt < attempts; attempt++ {
				cfg, target, keySig, ok := c.targetConfig(candidate.index, r, requested)
				if !ok {
					if lastErr == nil {
						lastErr = fmt.Errorf("router: route provider %q is not configured", r.Provider)
					}
					break
				}
				if err := resolveOAuthConfig(&cfg); err != nil {
					errs <- err
					return
				}
				p, err := provider.Build(cfg)
				if err != nil {
					errs <- err
					return
				}
				callOpts := opts
				callOpts.Model = target
				chunks, upstreamErrs := p.StreamChat(ctx, messages, callOpts)
				buffered := make([]provider.Chunk, 0, 16)
				for ch := range chunks {
					buffered = append(buffered, ch)
				}
				err = <-upstreamErrs
				if err == nil {
					for _, ch := range buffered {
						out <- ch
					}
					errs <- nil
					return
				}
				lastErr = err
				if isLimitError(err) && keySig != "" {
					c.markLimited(keySig, err)
				}
				if len(buffered) > 0 {
					for _, ch := range buffered {
						out <- ch
					}
					errs <- err
					return
				}
				if r.RetryKeys && isLimitError(err) && attempt+1 < attempts {
					continue
				}
				if !routeShouldFallback(err) {
					errs <- err
					return
				}
				break
			}
		}
		if lastErr == nil {
			lastErr = errors.New("router: no fallback route succeeded")
		}
		errs <- lastErr
	}()
	return out, errs
}

func resolveOAuthConfig(cfg *provider.Config) error {
	if cfg == nil || !strings.HasPrefix(cfg.APIKey, "oauth:") {
		return nil
	}
	flavour := strings.TrimPrefix(cfg.APIKey, "oauth:")
	switch flavour {
	case "codex", "openai":
		tok, err := codexoauth.NewForFlavour(flavour, "").ResolveToken(context.Background())
		if err != nil {
			return err
		}
		cfg.APIKey = tok.AccessToken
		if tok.AccountID != "" {
			if cfg.Headers == nil {
				cfg.Headers = map[string]string{}
			}
			if cfg.Headers["ChatGPT-Account-ID"] == "" {
				cfg.Headers["ChatGPT-Account-ID"] = tok.AccountID
			}
		}
		return nil
	case "xai", "xai-oauth", "grok-oauth":
		tok, err := xaioauth.New("").ResolveToken(context.Background())
		if err != nil {
			return err
		}
		cfg.APIKey = tok.AccessToken
		return nil
	default:
		return fmt.Errorf("oauth resolver for %q is not registered", flavour)
	}
}

func routeShouldFallback(err error) bool {
	if err == nil {
		return false
	}
	if provider.IsContextOverflow(err) {
		return false
	}
	return true
}

func routeMatches(r route, model string) bool {
	for _, alias := range r.Aliases {
		if globMatch(alias, model) {
			return true
		}
	}
	for _, pat := range r.patterns() {
		if globMatch(pat, model) {
			return true
		}
	}
	return len(r.patterns()) == 0 && r.Default
}

func resolveTargetModel(r route, requested, fallback string) string {
	target := firstNonEmpty(r.TargetModel, r.Model, requested, fallback)
	if target == "$model" {
		return requested
	}
	if strings.Contains(target, "$1") {
		target = strings.ReplaceAll(target, "$1", wildcardCapture(r.primaryPattern(), requested))
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

func loadRoutes(cfg provider.Config) (routeSet, string, error) {
	var routes []route
	hadInlineRoutes := false
	aliases := map[string]string{}
	if raw, ok := cfg.Extra["aliases"]; ok {
		b, err := json.Marshal(raw)
		if err != nil {
			return routeSet{}, "", err
		}
		if err := json.Unmarshal(b, &aliases); err != nil {
			return routeSet{}, "", fmt.Errorf("router: parse aliases: %w", err)
		}
	}
	if raw, ok := cfg.Extra["routes"]; ok {
		hadInlineRoutes = true
		b, err := json.Marshal(raw)
		if err != nil {
			return routeSet{}, "", err
		}
		if err := json.Unmarshal(b, &routes); err != nil {
			return routeSet{}, "", fmt.Errorf("router: parse routes: %w", err)
		}
	}
	path := firstNonEmpty(strExtra(cfg.Extra, "routes_file"), os.Getenv("AGENTBRIDGE_ROUTER_FILE"))
	if path == "" && !hadInlineRoutes {
		path = defaultRouteFile()
	}
	defaultModel := ""
	if path != "" {
		more, def, err := loadRouteFile(path)
		if err != nil {
			return routeSet{}, "", err
		}
		if def != "" {
			defaultModel = def
		}
		for k, v := range more.Aliases {
			aliases[k] = v
		}
		routes = append(routes, more.Routes...)
	}
	if len(routes) == 0 {
		return routeSet{}, "", errors.New("router: no routes configured")
	}
	routes = expandRoutes(routes)
	for i := range routes {
		for _, alias := range routes[i].Aliases {
			aliases[alias] = firstNonEmpty(routes[i].Match, routes[i].Model, routes[i].TargetModel)
		}
	}
	return routeSet{routes: routes, aliases: aliases}, defaultModel, nil
}

type stringsList []string

func (s *stringsList) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*s = splitList(one)
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err == nil {
		*s = normalizeList(many)
		return nil
	}
	var anyMany []any
	if err := json.Unmarshal(data, &anyMany); err != nil {
		return err
	}
	vals := make([]string, 0, len(anyMany))
	for _, v := range anyMany {
		vals = append(vals, fmt.Sprint(v))
	}
	*s = normalizeList(vals)
	return nil
}

func (s *stringsList) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		*s = splitList(value.Value)
		return nil
	case yaml.SequenceNode:
		vals := make([]string, 0, len(value.Content))
		for _, node := range value.Content {
			vals = append(vals, node.Value)
		}
		*s = normalizeList(vals)
		return nil
	default:
		return fmt.Errorf("expected string or list, got YAML kind %d", value.Kind)
	}
}

func (r *route) normalize() {
	r.Models = normalizeList(r.Models)
	r.Aliases = normalizeList(r.Aliases)
	r.APIKeys = normalizeList(r.APIKeys)
	r.APIKeyEnvs = normalizeList(r.APIKeyEnvs)
	for i := range r.Fallbacks {
		r.Fallbacks[i].normalize()
	}
}

func (r route) patterns() []string {
	out := make([]string, 0, 2+len(r.Models))
	out = appendIfNonEmpty(out, r.Match)
	out = appendIfNonEmpty(out, r.Model)
	out = append(out, r.Models...)
	return out
}

func (r route) primaryPattern() string {
	return firstNonEmpty(append([]string{r.Match, r.Model}, r.Models...)...)
}

func expandRoutes(routes []route) []route {
	out := make([]route, 0, len(routes))
	for _, r := range routes {
		r.normalize()
		if len(r.Models) == 0 {
			out = append(out, r)
			continue
		}
		for _, model := range r.Models {
			cp := r
			cp.Match = model
			cp.Model = ""
			cp.Models = nil
			out = append(out, cp)
		}
	}
	return out
}

func appendIfNonEmpty(out []string, v string) []string {
	if strings.TrimSpace(v) == "" {
		return out
	}
	return append(out, strings.TrimSpace(v))
}

func (r route) keys() []string {
	keys := append([]string{}, r.APIKeys...)
	for _, env := range r.APIKeyEnvs {
		if v := os.Getenv(strings.TrimSpace(env)); v != "" {
			keys = append(keys, v)
		}
	}
	return normalizeList(keys)
}

func splitList(s string) stringsList {
	if s == "" {
		return nil
	}
	f := func(r rune) bool {
		return r == ',' || r == '\n' || r == ';'
	}
	return normalizeList(strings.FieldsFunc(s, f))
}

func normalizeList(in []string) stringsList {
	out := make([]string, 0, len(in))
	for _, v := range in {
		for _, part := range splitWhitespace(v) {
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func splitWhitespace(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if strings.ContainsAny(s, ",;\n") {
		return []string(splitList(s))
	}
	return []string{s}
}

type limitInfo struct {
	Message string
}

func (c *Client) markLimited(sig string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.limitedKey[sig] = limitInfo{Message: err.Error()}
}

func isLimitError(err error) bool {
	if err == nil {
		return false
	}
	var httpStatus interface{ StatusCode() int }
	if errors.As(err, &httpStatus) {
		if httpStatus.StatusCode() == 429 {
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, " 429") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "rate_limit") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "weekly limit") ||
		strings.Contains(msg, "5h") ||
		strings.Contains(msg, "quota")
}

func keySignature(routeIndex, keyIndex int, key string) string {
	return fmt.Sprintf("%d:%d:%d", routeIndex, keyIndex, len(key))
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func mergeMaps(base, overlay map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
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

func loadRouteFile(path string) (routeFile, string, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return routeFile{}, "", fmt.Errorf("router: read routes file %s: %w", path, err)
	}
	var rf routeFile
	switch {
	case strings.HasSuffix(path, ".yaml"), strings.HasSuffix(path, ".yml"):
		if err := yaml.Unmarshal(data, &rf); err != nil {
			return routeFile{}, "", fmt.Errorf("router: parse routes file %s: %w", path, err)
		}
	default:
		if err := json.Unmarshal(data, &rf); err != nil {
			return routeFile{}, "", fmt.Errorf("router: parse routes file %s: %w", path, err)
		}
	}
	return rf, rf.DefaultModel, nil
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
