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
	APIKey     string
	BaseURL    string
	Model      string
	HTTPClient *http.Client
}

// ConfigFromEnv builds Config from AgentBridge and common OpenAI/LiteLLM vars.
func ConfigFromEnv() Config {
	return Config{
		APIKey:  envFirst("AGENTBRIDGE_EMBEDDINGS_API_KEY", "LITELLM_API_KEY", "OPENAI_API_KEY", "AGENTBRIDGE_API_KEY"),
		BaseURL: envFirst("AGENTBRIDGE_EMBEDDINGS_BASE_URL", "LITELLM_BASE_URL", "OPENAI_BASE_URL"),
		Model:   envFirst("AGENTBRIDGE_EMBEDDINGS_MODEL", "LITELLM_EMBEDDINGS_MODEL", "OPENAI_EMBEDDINGS_MODEL"),
	}
}

// Plugin is the concrete OpenAI-compatible embeddings plugin.
type Plugin struct {
	cfg    Config
	client *http.Client
}

// New constructs a plugin.
func New(cfg Config) *Plugin {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.Model == "" {
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
		Parameters:  json.RawMessage(`{"type":"object","properties":{"input":{"type":"string","description":"Single input text."},"inputs":{"type":"array","items":{"type":"string"},"description":"Multiple input texts."},"model":{"type":"string","description":"Embedding model. Defaults to AGENTBRIDGE_EMBEDDINGS_MODEL or text-embedding-3-small."},"encoding_format":{"type":"string","description":"Optional OpenAI encoding_format, e.g. float or base64."},"dimensions":{"type":"integer","description":"Optional output dimensions when supported by the model."}}}`),
	}}
}

func (p *Plugin) Call(ctx context.Context, tool string, args json.RawMessage) (string, error) {
	if tool != "embed" {
		return "", fmt.Errorf("openai_embed: unknown tool %q", tool)
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
	payload := map[string]any{
		"model": firstNonEmpty(in.Model, p.cfg.Model),
		"input": input,
	}
	if in.EncodingFormat != "" {
		payload["encoding_format"] = in.EncodingFormat
	}
	if in.Dimensions > 0 {
		payload["dimensions"] = in.Dimensions
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.cfg.BaseURL, "/")+"/embeddings", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
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
