// Package xaiplugin exposes direct xAI auxiliary APIs as optional tools.
//
// Activation: add `xai` to AGENTBRIDGE_PLUGINS.
//
// Tools exposed via plugin__xai__<name>:
//   - xai_x_search:       route a query through xAI Responses with x_search
//   - xai_image_generate: POST /v1/images/generations
//   - xai_image_edit:     POST /v1/images/edits
package xaiplugin

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

	xaioauth "github.com/ziozzang/agentbridge/internal/oauth/xai"
	"github.com/ziozzang/agentbridge/internal/plugins"
)

const (
	// Name is the plugin identifier.
	Name = "xai"

	defaultBaseURL     = "https://api.x.ai/v1"
	defaultSearchModel = "grok-4.3"
	defaultImageModel  = "grok-imagine-image"
	maxBodyBytes       = 16 << 20
)

func init() {
	plugins.Register(Name, func() plugins.Plugin { return New(ConfigFromEnv()) })
}

// Config controls the xAI plugin.
type Config struct {
	APIKey      string
	BaseURL     string
	SearchModel string
	ImageModel  string
	OAuthPath   string
	HTTPClient  *http.Client
}

// ConfigFromEnv builds Config from AgentBridge and upstream xAI variables.
func ConfigFromEnv() Config {
	return Config{
		APIKey:      envFirst("AGENTBRIDGE_XAI_API_KEY", "XAI_API_KEY"),
		BaseURL:     envFirst("AGENTBRIDGE_XAI_BASE_URL", "XAI_BASE_URL"),
		SearchModel: envFirst("AGENTBRIDGE_XAI_SEARCH_MODEL", "XAI_SEARCH_MODEL", "XAI_MODEL"),
		ImageModel:  envFirst("AGENTBRIDGE_XAI_IMAGE_MODEL", "XAI_IMAGE_MODEL"),
		OAuthPath:   envFirst("AGENTBRIDGE_XAI_OAUTH_PATH", "XAI_OAUTH_PATH"),
	}
}

// Plugin is the concrete xAI plugin.
type Plugin struct {
	cfg    Config
	client *http.Client
	oauth  *xaioauth.Resolver
}

// New constructs an xAI plugin.
func New(cfg Config) *Plugin {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.SearchModel == "" {
		cfg.SearchModel = defaultSearchModel
	}
	if cfg.ImageModel == "" {
		cfg.ImageModel = defaultImageModel
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	return &Plugin{cfg: cfg, client: client, oauth: xaioauth.New(cfg.OAuthPath)}
}

func (p *Plugin) Name() string { return Name }

func (p *Plugin) Tools() []plugins.ToolDef {
	return []plugins.ToolDef{
		{
			Name:        "xai_x_search",
			Description: "Search X posts and threads through xAI's built-in Responses x_search tool. Uses xAI OAuth when available, otherwise XAI_API_KEY.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Search question or instruction."},"model":{"type":"string","description":"Grok Responses model. Defaults to AGENTBRIDGE_XAI_SEARCH_MODEL or grok-4.3."},"allowed_x_handles":{"type":"array","items":{"type":"string"},"description":"Only consider these X handles. Max 10. Mutually exclusive with excluded_x_handles."},"excluded_x_handles":{"type":"array","items":{"type":"string"},"description":"Exclude these X handles. Max 10."},"from_date":{"type":"string","description":"ISO8601 start date, e.g. 2026-05-20."},"to_date":{"type":"string","description":"ISO8601 end date."},"enable_image_understanding":{"type":"boolean"},"enable_video_understanding":{"type":"boolean"}},"required":["query"]}`),
		},
		{
			Name:        "xai_image_generate",
			Description: "Generate images with xAI Grok Imagine via /v1/images/generations.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string"},"model":{"type":"string","description":"Defaults to AGENTBRIDGE_XAI_IMAGE_MODEL or grok-imagine-image."},"n":{"type":"integer","description":"Number of images."},"aspect_ratio":{"type":"string","description":"Aspect ratio such as 1:1, 16:9, 3:2."},"response_format":{"type":"string","description":"url or b64_json."},"quality":{"type":"string"},"size":{"type":"string"}},"required":["prompt"]}`),
		},
		{
			Name:        "xai_image_edit",
			Description: "Edit one or more images with xAI Grok Imagine via /v1/images/edits.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string"},"model":{"type":"string","description":"Defaults to AGENTBRIDGE_XAI_IMAGE_MODEL or grok-imagine-image."},"image_url":{"type":"string","description":"Single source image URL."},"image_urls":{"type":"array","items":{"type":"string"},"description":"Multiple source image URLs, up to the upstream limit."},"aspect_ratio":{"type":"string"},"response_format":{"type":"string","description":"url or b64_json."}},"required":["prompt"]}`),
		},
	}
}

func (p *Plugin) Call(ctx context.Context, tool string, args json.RawMessage) (string, error) {
	switch tool {
	case "xai_x_search":
		return p.handleXSearch(ctx, args)
	case "xai_image_generate":
		return p.handleImageGenerate(ctx, args)
	case "xai_image_edit":
		return p.handleImageEdit(ctx, args)
	default:
		return "", fmt.Errorf("xai: unknown tool %q", tool)
	}
}

type xSearchArgs struct {
	Query                    string   `json:"query"`
	Model                    string   `json:"model"`
	AllowedXHandles          []string `json:"allowed_x_handles"`
	ExcludedXHandles         []string `json:"excluded_x_handles"`
	FromDate                 string   `json:"from_date"`
	ToDate                   string   `json:"to_date"`
	EnableImageUnderstanding bool     `json:"enable_image_understanding"`
	EnableVideoUnderstanding bool     `json:"enable_video_understanding"`
}

type imageGenerateArgs struct {
	Prompt         string `json:"prompt"`
	Model          string `json:"model"`
	N              int    `json:"n"`
	AspectRatio    string `json:"aspect_ratio"`
	ResponseFormat string `json:"response_format"`
	Quality        string `json:"quality"`
	Size           string `json:"size"`
}

type imageEditArgs struct {
	Prompt         string   `json:"prompt"`
	Model          string   `json:"model"`
	ImageURL       string   `json:"image_url"`
	ImageURLs      []string `json:"image_urls"`
	AspectRatio    string   `json:"aspect_ratio"`
	ResponseFormat string   `json:"response_format"`
}

func (p *Plugin) handleXSearch(ctx context.Context, raw json.RawMessage) (string, error) {
	var args xSearchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", err
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return "", errors.New("xai: 'query' is required")
	}
	if len(args.AllowedXHandles) > 0 && len(args.ExcludedXHandles) > 0 {
		return "", errors.New("xai: allowed_x_handles and excluded_x_handles are mutually exclusive")
	}
	if len(args.AllowedXHandles) > 10 || len(args.ExcludedXHandles) > 10 {
		return "", errors.New("xai: allowed_x_handles/excluded_x_handles must contain at most 10 handles")
	}
	model := firstNonEmpty(args.Model, p.cfg.SearchModel)
	tool := map[string]any{"type": "x_search"}
	if len(args.AllowedXHandles) > 0 {
		tool["allowed_x_handles"] = args.AllowedXHandles
	}
	if len(args.ExcludedXHandles) > 0 {
		tool["excluded_x_handles"] = args.ExcludedXHandles
	}
	if args.FromDate != "" {
		tool["from_date"] = args.FromDate
	}
	if args.ToDate != "" {
		tool["to_date"] = args.ToDate
	}
	if args.EnableImageUnderstanding {
		tool["enable_image_understanding"] = true
	}
	if args.EnableVideoUnderstanding {
		tool["enable_video_understanding"] = true
	}
	payload := map[string]any{
		"model": model,
		"input": []map[string]any{{
			"role":    "user",
			"content": query,
		}},
		"tools": []map[string]any{tool},
		"store": false,
	}
	return p.postJSON(ctx, "/responses", payload)
}

func (p *Plugin) handleImageGenerate(ctx context.Context, raw json.RawMessage) (string, error) {
	var args imageGenerateArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Prompt) == "" {
		return "", errors.New("xai: 'prompt' is required")
	}
	payload := map[string]any{
		"model":  firstNonEmpty(args.Model, p.cfg.ImageModel),
		"prompt": args.Prompt,
	}
	if args.N > 0 {
		payload["n"] = args.N
	}
	if args.AspectRatio != "" {
		payload["aspect_ratio"] = args.AspectRatio
	}
	if args.ResponseFormat != "" {
		payload["response_format"] = args.ResponseFormat
	}
	if args.Quality != "" {
		payload["quality"] = args.Quality
	}
	if args.Size != "" {
		payload["size"] = args.Size
	}
	return p.postJSON(ctx, "/images/generations", payload)
}

func (p *Plugin) handleImageEdit(ctx context.Context, raw json.RawMessage) (string, error) {
	var args imageEditArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Prompt) == "" {
		return "", errors.New("xai: 'prompt' is required")
	}
	payload := map[string]any{
		"model":  firstNonEmpty(args.Model, p.cfg.ImageModel),
		"prompt": args.Prompt,
	}
	switch {
	case args.ImageURL != "" && len(args.ImageURLs) > 0:
		return "", errors.New("xai: provide either image_url or image_urls, not both")
	case args.ImageURL != "":
		payload["image"] = map[string]any{"url": args.ImageURL}
	case len(args.ImageURLs) > 0:
		images := make([]map[string]any, 0, len(args.ImageURLs))
		for _, u := range args.ImageURLs {
			if strings.TrimSpace(u) == "" {
				continue
			}
			images = append(images, map[string]any{"type": "image_url", "url": u})
		}
		if len(images) == 0 {
			return "", errors.New("xai: image_urls cannot be empty")
		}
		payload["images"] = images
	default:
		return "", errors.New("xai: image_url or image_urls is required")
	}
	if args.AspectRatio != "" {
		payload["aspect_ratio"] = args.AspectRatio
	}
	if args.ResponseFormat != "" {
		payload["response_format"] = args.ResponseFormat
	}
	return p.postJSON(ctx, "/images/edits", payload)
}

func (p *Plugin) postJSON(ctx context.Context, path string, payload map[string]any) (string, error) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return "", err
	}
	endpoint := strings.TrimRight(p.cfg.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	bearer, err := p.bearer(ctx)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
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
		return "", fmt.Errorf("xai: %s: %s", resp.Status, snippet(data))
	}
	return string(data), nil
}

func (p *Plugin) bearer(ctx context.Context) (string, error) {
	if p.oauth != nil {
		if tok, err := p.oauth.Resolve(ctx); err == nil && tok != "" {
			return tok, nil
		}
	}
	if p.cfg.APIKey != "" {
		return p.cfg.APIKey, nil
	}
	return "", errors.New("xai: credentials required; set XAI_API_KEY or configure ~/.grok/auth.json")
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

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 512 {
		return s[:512]
	}
	return s
}
