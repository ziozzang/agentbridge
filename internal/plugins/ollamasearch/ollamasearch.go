// Package ollamasearchplugin exposes Ollama Cloud web search and fetch APIs
// as optional AgentBridge tools.
//
// Activation: add `ollama_search` to AGENTBRIDGE_PLUGINS.
//
// Tools exposed via plugin__ollama_search__<name>:
//   - ollama_search: {query: string, max_results?: int}
//   - ollama_fetch:  {url: string}
package ollamasearchplugin

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

	"github.com/ziozzang/agentbridge/internal/plugins"
)

const (
	// Name is the plugin identifier.
	Name = "ollama_search"

	defaultBaseURL  = "https://ollama.com"
	maxResultsLimit = 10
	maxBodyBytes    = 8 << 20
)

func init() {
	plugins.Register(Name, func() plugins.Plugin { return New(ConfigFromEnv()) })
}

// Config controls the Ollama search plugin.
type Config struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// ConfigFromEnv builds a Config from AgentBridge-specific variables and the
// upstream OLLAMA_API_KEY alias.
func ConfigFromEnv() Config {
	return Config{
		APIKey:  envFirst("AGENTBRIDGE_OLLAMA_SEARCH_API_KEY", "OLLAMA_API_KEY"),
		BaseURL: envFirst("AGENTBRIDGE_OLLAMA_SEARCH_BASE_URL"),
	}
}

// Plugin is the concrete Ollama search plugin.
type Plugin struct {
	cfg    Config
	client *http.Client
}

// New constructs an Ollama search plugin.
func New(cfg Config) *Plugin {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Plugin{cfg: cfg, client: client}
}

func (p *Plugin) Name() string { return Name }

func (p *Plugin) Tools() []plugins.ToolDef {
	return []plugins.ToolDef{
		{
			Name:        "ollama_search",
			Description: "Search the web through Ollama Cloud's official web_search API.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Search query."},"max_results":{"type":"integer","description":"Maximum results to return. Default 5, max 10."}},"required":["query"]}`),
		},
		{
			Name:        "ollama_fetch",
			Description: "Fetch one web page through Ollama Cloud's official web_fetch API.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"URL to fetch."}},"required":["url"]}`),
		},
	}
}

func (p *Plugin) Call(ctx context.Context, tool string, args json.RawMessage) (string, error) {
	switch tool {
	case "ollama_search":
		return p.handleSearch(ctx, args)
	case "ollama_fetch":
		return p.handleFetch(ctx, args)
	default:
		return "", fmt.Errorf("ollama_search: unknown tool %q", tool)
	}
}

type searchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

type fetchArgs struct {
	URL string `json:"url"`
}

func (p *Plugin) handleSearch(ctx context.Context, raw json.RawMessage) (string, error) {
	var args searchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", err
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return "", errors.New("ollama_search: 'query' is required")
	}
	payload := map[string]any{"query": query}
	if args.MaxResults > 0 {
		if args.MaxResults > maxResultsLimit {
			return "", fmt.Errorf("ollama_search: 'max_results' must be <= %d", maxResultsLimit)
		}
		payload["max_results"] = args.MaxResults
	}
	return p.postJSON(ctx, "/api/web_search", payload)
}

func (p *Plugin) handleFetch(ctx context.Context, raw json.RawMessage) (string, error) {
	var args fetchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", err
	}
	u := strings.TrimSpace(args.URL)
	if u == "" {
		return "", errors.New("ollama_search: 'url' is required")
	}
	return p.postJSON(ctx, "/api/web_fetch", map[string]any{"url": u})
}

func (p *Plugin) postJSON(ctx context.Context, path string, payload map[string]any) (string, error) {
	if p.cfg.APIKey == "" {
		return "", errors.New("ollama_search: API key is required; set AGENTBRIDGE_OLLAMA_SEARCH_API_KEY or OLLAMA_API_KEY")
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return "", err
	}
	endpoint := strings.TrimRight(p.cfg.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
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
		return "", fmt.Errorf("ollama_search: %s: %s", resp.Status, snippet(data))
	}
	return string(data), nil
}

func envFirst(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 512 {
		return s[:512]
	}
	return s
}
