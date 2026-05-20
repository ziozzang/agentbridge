// Package openaiembedplugin exposes any OpenAI-compatible embeddings endpoint
// as an AgentBridge plugin. It is useful for LiteLLM, OpenAI, local vLLM, and
// other gateways that implement POST /v1/embeddings.
package openaiembedplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ziozzang/agentbridge/internal/config"
	"github.com/ziozzang/agentbridge/internal/plugins"
)

const (
	// Name is the plugin identifier.
	Name = "openai_embed"

	defaultBaseURL = "http://localhost:4000/v1"
	defaultModel   = "text-embedding-3-small"
	maxBodyBytes   = 16 << 20
)

func init() {
	plugins.Register(Name, func() plugins.Plugin { return New(ConfigFromEnv()) })
}

// Config controls the embeddings plugin.
type Config struct {
	APIKey      string
	BaseURL     string
	Model       string
	MappingFile string
	Mappings    map[string]ModelMapping
	DefaultMap  string
	HTTPClient  *http.Client
}

// ModelMapping maps a user-facing embedding model alias to an upstream
// OpenAI-compatible embeddings route.
type ModelMapping struct {
	BaseURL        string            `json:"base_url"`
	APIKey         string            `json:"api_key"`
	APIKeyEnv      string            `json:"api_key_env"`
	Model          string            `json:"model"`
	EncodingFormat string            `json:"encoding_format"`
	Dimensions     int               `json:"dimensions"`
	Headers        map[string]string `json:"headers"`
}

type mappingFile struct {
	Default string                  `json:"default"`
	Models  map[string]ModelMapping `json:"models"`
}

// ConfigFromEnv builds Config from AgentBridge and common OpenAI/LiteLLM vars.
func ConfigFromEnv() Config {
	return Config{
		APIKey:  envFirst("AGENTBRIDGE_EMBEDDINGS_API_KEY", "LITELLM_API_KEY", "LITELLM_OPENAI_API_KEY", "OPENAI_API_KEY", "AGENTBRIDGE_API_KEY"),
		BaseURL: envFirst("AGENTBRIDGE_EMBEDDINGS_BASE_URL", "LITELLM_BASE_URL", "LITELLM_OPENAI_BASE_URL", "OPENAI_BASE_URL"),
		Model:   envFirst("AGENTBRIDGE_EMBEDDINGS_MODEL", "LITELLM_EMBEDDINGS_MODEL", "OPENAI_EMBEDDINGS_MODEL"),
	}
}

// Plugin is the concrete OpenAI-compatible embeddings plugin.
type Plugin struct {
	cfg        Config
	client     *http.Client
	mappingErr error
}

// New constructs a plugin.
func New(cfg Config) *Plugin {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.Mappings == nil {
		mappings, def, err := loadConfiguredMappings(cfg.MappingFile)
		cfg.Mappings = mappings
		cfg.DefaultMap = def
		if err != nil {
			client := cfg.HTTPClient
			if client == nil {
				client = &http.Client{Timeout: 60 * time.Second}
			}
			return &Plugin{cfg: cfg, client: client, mappingErr: err}
		}
	}
	if cfg.Model == "" && cfg.DefaultMap == "" {
		cfg.Model = defaultModel
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &Plugin{cfg: cfg, client: client}
}

func (p *Plugin) Name() string { return Name }

func (p *Plugin) Tools() []plugins.ToolDef {
	return []plugins.ToolDef{{
		Name:        "embed",
		Description: "Create embeddings through an OpenAI-compatible /embeddings endpoint such as LiteLLM.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"input":{"type":"string","description":"Single input text."},"inputs":{"type":"array","items":{"type":"string"},"description":"Multiple input texts."},"model":{"type":"string","description":"Embedding model or alias from router embedding routes. Defaults to AGENTBRIDGE_EMBEDDINGS_MODEL, router default, or text-embedding-3-small."},"encoding_format":{"type":"string","description":"Optional OpenAI encoding_format, e.g. float or base64."},"dimensions":{"type":"integer","description":"Optional output dimensions when supported by the model."}}}`),
	}}
}

func (p *Plugin) Call(ctx context.Context, tool string, args json.RawMessage) (string, error) {
	if tool != "embed" {
		return "", fmt.Errorf("openai_embed: unknown tool %q", tool)
	}
	if p.mappingErr != nil {
		return "", p.mappingErr
	}
	var in struct {
		Input          string   `json:"input"`
		Inputs         []string `json:"inputs"`
		Model          string   `json:"model"`
		EncodingFormat string   `json:"encoding_format"`
		Dimensions     int      `json:"dimensions"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	input := any(nil)
	switch {
	case in.Input != "" && len(in.Inputs) > 0:
		return "", errors.New("openai_embed: provide either input or inputs, not both")
	case in.Input != "":
		input = in.Input
	case len(in.Inputs) > 0:
		input = in.Inputs
	default:
		return "", errors.New("openai_embed: input or inputs is required")
	}
	route := p.resolveRoute(firstNonEmpty(in.Model, p.cfg.Model, p.cfg.DefaultMap))
	payload := map[string]any{
		"model": route.model,
		"input": input,
	}
	if firstNonEmpty(in.EncodingFormat, route.encodingFormat) != "" {
		payload["encoding_format"] = firstNonEmpty(in.EncodingFormat, route.encodingFormat)
	}
	if in.Dimensions > 0 {
		payload["dimensions"] = in.Dimensions
	} else if route.dimensions > 0 {
		payload["dimensions"] = route.dimensions
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(route.baseURL, "/")+"/embeddings", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range route.headers {
		if strings.TrimSpace(k) != "" && v != "" {
			req.Header.Set(k, v)
		}
	}
	if route.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+route.apiKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("openai_embed: %s: %s", resp.Status, snippet(data))
	}
	return string(data), nil
}

type resolvedRoute struct {
	baseURL        string
	apiKey         string
	model          string
	encodingFormat string
	dimensions     int
	headers        map[string]string
}

func (p *Plugin) resolveRoute(name string) resolvedRoute {
	route := resolvedRoute{
		baseURL: p.cfg.BaseURL,
		apiKey:  p.cfg.APIKey,
		model:   firstNonEmpty(name, p.cfg.Model),
		headers: map[string]string{},
	}
	if p.cfg.Mappings == nil {
		return route
	}
	if name == "" && p.cfg.DefaultMap != "" {
		name = p.cfg.DefaultMap
	}
	m, ok := p.cfg.Mappings[name]
	if !ok {
		return route
	}
	if m.BaseURL != "" {
		route.baseURL = expand(m.BaseURL)
	}
	if m.Model != "" {
		route.model = expand(m.Model)
	}
	if m.APIKeyEnv != "" {
		route.apiKey = os.Getenv(expand(m.APIKeyEnv))
	}
	if route.apiKey == "" && m.APIKey != "" {
		route.apiKey = expand(m.APIKey)
	}
	if m.EncodingFormat != "" {
		route.encodingFormat = expand(m.EncodingFormat)
	}
	if m.Dimensions > 0 {
		route.dimensions = m.Dimensions
	}
	for k, v := range m.Headers {
		route.headers[expand(k)] = expand(v)
	}
	return route
}

func loadMappings(path string) (map[string]ModelMapping, string, error) {
	if path == "" {
		return nil, "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("openai_embed: read embeddings mapping %s: %w", path, err)
	}
	var mf mappingFile
	if err := json.Unmarshal(data, &mf); err != nil {
		return nil, "", fmt.Errorf("openai_embed: parse embeddings mapping %s: %w", path, err)
	}
	return mf.Models, mf.Default, nil
}

func loadConfiguredMappings(path string) (map[string]ModelMapping, string, error) {
	if path != "" {
		return loadMappings(path)
	}
	if mappings, def, ok := loadRouterMappings(); ok {
		return mappings, def, nil
	}
	return nil, "", nil
}

func loadRouterMappings() (map[string]ModelMapping, string, bool) {
	manifest, err := config.Load()
	if err != nil {
		return nil, "", false
	}
	router, err := manifest.Resolve("router")
	if err != nil || router.Extra == nil {
		return nil, "", false
	}
	raw, ok := router.Extra["embeddings"]
	if !ok {
		raw, ok = router.Extra["embedding_routes"]
	}
	if !ok || raw == nil {
		return nil, "", false
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, "", false
	}
	var mf mappingFile
	if err := json.Unmarshal(data, &mf); err != nil || len(mf.Models) == 0 {
		return nil, "", false
	}
	return mf.Models, mf.Default, true
}

func envFirst(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func expand(s string) string {
	return os.ExpandEnv(strings.TrimSpace(s))
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 512 {
		return s[:512]
	}
	return s
}
