// Package jinaplugin exposes Jina AI Reader, Search, and Embeddings APIs as
// optional AgentBridge tools.
//
// Activation: add `jina` to AGENTBRIDGE_PLUGINS.
//
// Tools exposed via plugin__jina__<name>:
//   - jina_reader: {url: string} -> LLM-friendly page text
//   - jina_search: {query: string} -> LLM-friendly search results
//   - jina_embed:   {input?: string, inputs?: []string, model?: string} -> embeddings
//   - jina_rerank:  {query: string, documents: []string, model?: string} -> ranked documents
package jinaplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ziozzang/agentbridge/internal/plugins"
)

const (
	// Name is the plugin identifier.
	Name = "jina"

	defaultReaderBaseURL     = "https://r.jina.ai"
	defaultSearchBaseURL     = "https://s.jina.ai"
	defaultEmbeddingsBaseURL = "https://api.jina.ai/v1"
	defaultEmbeddingsModel   = "jina-embeddings-v3"
	defaultRerankBaseURL     = "https://api.jina.ai/v1"
	defaultRerankModel       = "jina-reranker-v3"
	maxResponseBytes         = 8 << 20
)

func init() {
	plugins.Register(Name, func() plugins.Plugin { return New(ConfigFromEnv()) })
}

// Config controls the Jina plugin. Empty fields are filled with official
// defaults.
type Config struct {
	APIKey            string
	ReaderBaseURL     string
	SearchBaseURL     string
	EmbeddingsBaseURL string
	EmbeddingsModel   string
	RerankBaseURL     string
	RerankModel       string
	HTTPClient        *http.Client
}

// ConfigFromEnv builds a Config from AgentBridge-specific variables and
// common upstream aliases.
func ConfigFromEnv() Config {
	return Config{
		APIKey:            envFirst("AGENTBRIDGE_JINA_API_KEY", "JINA_API_KEY"),
		ReaderBaseURL:     envFirst("AGENTBRIDGE_JINA_READER_BASE_URL"),
		SearchBaseURL:     envFirst("AGENTBRIDGE_JINA_SEARCH_BASE_URL"),
		EmbeddingsBaseURL: envFirst("AGENTBRIDGE_JINA_EMBEDDINGS_BASE_URL"),
		EmbeddingsModel:   envFirst("AGENTBRIDGE_JINA_EMBEDDINGS_MODEL"),
		RerankBaseURL:     envFirst("AGENTBRIDGE_JINA_RERANK_BASE_URL"),
		RerankModel:       envFirst("AGENTBRIDGE_JINA_RERANK_MODEL"),
	}
}

// Plugin is the concrete Jina plugin.
type Plugin struct {
	cfg    Config
	client *http.Client
}

// New constructs a Jina plugin.
func New(cfg Config) *Plugin {
	if cfg.ReaderBaseURL == "" {
		cfg.ReaderBaseURL = defaultReaderBaseURL
	}
	if cfg.SearchBaseURL == "" {
		cfg.SearchBaseURL = defaultSearchBaseURL
	}
	if cfg.EmbeddingsBaseURL == "" {
		cfg.EmbeddingsBaseURL = defaultEmbeddingsBaseURL
	}
	if cfg.EmbeddingsModel == "" {
		cfg.EmbeddingsModel = defaultEmbeddingsModel
	}
	if cfg.RerankBaseURL == "" {
		cfg.RerankBaseURL = defaultRerankBaseURL
	}
	if cfg.RerankModel == "" {
		cfg.RerankModel = defaultRerankModel
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
			Name:        "jina_reader",
			Description: "Read a URL through Jina Reader (r.jina.ai) and return LLM-friendly text.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"Target URL to read."}},"required":["url"]}`),
		},
		{
			Name:        "jina_search",
			Description: "Search the web through Jina Search (s.jina.ai) and return LLM-friendly search results.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Search query."}},"required":["query"]}`),
		},
		{
			Name:        "jina_embed",
			Description: "Create embeddings through Jina's OpenAI-compatible embeddings API.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"input":{"type":"string","description":"Single input text."},"inputs":{"type":"array","items":{"type":"string"},"description":"Multiple input texts."},"model":{"type":"string","description":"Jina embedding model. Defaults to AGENTBRIDGE_JINA_EMBEDDINGS_MODEL or jina-embeddings-v3."},"embedding_type":{"type":"string","description":"Optional Jina embedding_type parameter."}}}`),
		},
		{
			Name:        "jina_rerank",
			Description: "Rerank documents through Jina's reranker API.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Search query."},"documents":{"type":"array","items":{"type":"string"},"description":"Documents to rerank."},"model":{"type":"string","description":"Jina reranker model. Defaults to AGENTBRIDGE_JINA_RERANK_MODEL or jina-reranker-v3."},"top_n":{"type":"integer","description":"Optional maximum number of results."},"return_documents":{"type":"boolean","description":"Whether to include document text in results."}},"required":["query","documents"]}`),
		},
	}
}

func (p *Plugin) Call(ctx context.Context, tool string, args json.RawMessage) (string, error) {
	switch tool {
	case "jina_reader":
		return p.handleReader(ctx, args)
	case "jina_search":
		return p.handleSearch(ctx, args)
	case "jina_embed":
		return p.handleEmbed(ctx, args)
	case "jina_rerank":
		return p.handleRerank(ctx, args)
	default:
		return "", fmt.Errorf("jina: unknown tool %q", tool)
	}
}

type readArgs struct {
	URL string `json:"url"`
}

type searchArgs struct {
	Query string `json:"query"`
}

type embedArgs struct {
	Input         string   `json:"input"`
	Inputs        []string `json:"inputs"`
	Model         string   `json:"model"`
	EmbeddingType string   `json:"embedding_type"`
}

type rerankArgs struct {
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	Model           string   `json:"model"`
	TopN            int      `json:"top_n"`
	ReturnDocuments *bool    `json:"return_documents"`
}

func (p *Plugin) handleReader(ctx context.Context, raw json.RawMessage) (string, error) {
	var args readArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.URL) == "" {
		return "", errors.New("jina: 'url' is required")
	}
	endpoint := strings.TrimRight(p.cfg.ReaderBaseURL, "/") + "/" + args.URL
	body, err := p.getText(ctx, endpoint)
	if err != nil {
		return "", err
	}
	return jsonOut(map[string]any{"url": args.URL, "content": body})
}

func (p *Plugin) handleSearch(ctx context.Context, raw json.RawMessage) (string, error) {
	var args searchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", err
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return "", errors.New("jina: 'query' is required")
	}
	endpoint := strings.TrimRight(p.cfg.SearchBaseURL, "/") + "/" + url.QueryEscape(query)
	body, err := p.getText(ctx, endpoint)
	if err != nil {
		return "", err
	}
	return jsonOut(map[string]any{"query": query, "content": body})
}

func (p *Plugin) handleEmbed(ctx context.Context, raw json.RawMessage) (string, error) {
	var args embedArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", err
	}
	input := any(nil)
	switch {
	case args.Input != "" && len(args.Inputs) > 0:
		return "", errors.New("jina: provide either 'input' or 'inputs', not both")
	case args.Input != "":
		input = args.Input
	case len(args.Inputs) > 0:
		input = args.Inputs
	default:
		return "", errors.New("jina: 'input' or 'inputs' is required")
	}
	model := args.Model
	if model == "" {
		model = p.cfg.EmbeddingsModel
	}
	payload := map[string]any{"model": model, "input": input}
	if args.EmbeddingType != "" {
		payload["embedding_type"] = args.EmbeddingType
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return "", err
	}
	endpoint := strings.TrimRight(p.cfg.EmbeddingsBaseURL, "/") + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	p.authorize(req)
	return p.do(req)
}

func (p *Plugin) handleRerank(ctx context.Context, raw json.RawMessage) (string, error) {
	var args rerankArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Query) == "" {
		return "", errors.New("jina: 'query' is required")
	}
	if len(args.Documents) == 0 {
		return "", errors.New("jina: 'documents' is required")
	}
	model := args.Model
	if model == "" {
		model = p.cfg.RerankModel
	}
	payload := map[string]any{
		"model":     model,
		"query":     args.Query,
		"documents": args.Documents,
	}
	if args.TopN > 0 {
		payload["top_n"] = args.TopN
	}
	if args.ReturnDocuments != nil {
		payload["return_documents"] = *args.ReturnDocuments
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return "", err
	}
	endpoint := strings.TrimRight(p.cfg.RerankBaseURL, "/") + "/rerank"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	p.authorize(req)
	return p.do(req)
}

func (p *Plugin) getText(ctx context.Context, endpoint string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/plain")
	p.authorize(req)
	return p.do(req)
}

func (p *Plugin) authorize(req *http.Request) {
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
}

func (p *Plugin) do(req *http.Request) (string, error) {
	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("jina: %s: %s", resp.Status, snippet(data))
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

func jsonOut(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 512 {
		return s[:512]
	}
	return s
}
