// Package ollama implements the Ollama native /api/chat provider.
//
// Endpoint:   POST <BaseURL>/api/chat
// Streaming:  newline-delimited JSON (one chat-response object per line, not
//
//	SSE). Each object has the next delta in `message.content`;
//	when `done` is true, totals and a final reason are attached.
//
// Ollama also serves an OpenAI-compatible endpoint under `/v1/`. Users who
// prefer that should configure an `openai-chat` provider with
// BaseURL=`http://host:11434/v1` instead.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ziozzang/agentbridge/internal/logger"
	"github.com/ziozzang/agentbridge/internal/provider"
	"github.com/ziozzang/agentbridge/internal/tools/definitions"
)

// Kind is the registry key for this provider.
const Kind = "ollama"

func init() {
	provider.Register(Kind, func(cfg provider.Config) (provider.Provider, error) {
		return New(cfg), nil
	})
}

// Client is an Ollama provider.
type Client struct {
	cfg        provider.Config
	HTTPClient *http.Client
}

// New constructs an Ollama client. The default base URL is
// http://localhost:11434.
func New(cfg provider.Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:11434"
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 4096
	}
	return &Client{cfg: cfg, HTTPClient: &http.Client{}}
}

func (c *Client) Name() string { return firstNonEmpty(c.cfg.Name, Kind) }
func (c *Client) Kind() string { return Kind }

func (c *Client) AvailableModels() []provider.ModelInfo {
	out := make([]provider.ModelInfo, len(c.cfg.Models))
	copy(out, c.cfg.Models)
	return out
}

func (c *Client) DefaultModel() string {
	if c.cfg.DefaultModel != "" {
		return c.cfg.DefaultModel
	}
	if len(c.cfg.Models) > 0 {
		return c.cfg.Models[0].ModelID
	}
	return ""
}

func (c *Client) ContextWindow(model string) int {
	_ = model
	if c.cfg.ContextWindow > 0 {
		return c.cfg.ContextWindow
	}
	return 32_000
}

// Config exposes the resolved provider configuration (read-only).
func (c *Client) Config() provider.Config { return c.cfg }

// ----- Request shape ------------------------------------------------------

type chatReq struct {
	Model    string         `json:"model"`
	Messages []ollamaMsg    `json:"messages"`
	Tools    []ollamaTool   `json:"tools,omitempty"`
	Stream   bool           `json:"stream"`
	Options  map[string]any `json:"options,omitempty"`
}

type ollamaMsg struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	Images    []string         `json:"images,omitempty"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaToolCall struct {
	Function ollamaToolCallFn `json:"function"`
}

type ollamaToolCallFn struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type ollamaTool struct {
	Type     string        `json:"type"`
	Function ollamaToolDef `json:"function"`
}

type ollamaToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// chatRes is one streamed response object.
type chatRes struct {
	Model   string `json:"model"`
	Message *struct {
		Role      string           `json:"role"`
		Content   string           `json:"content"`
		ToolCalls []ollamaToolCall `json:"tool_calls"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
}

// StreamChat implements provider.Provider.
func (c *Client) StreamChat(ctx context.Context, messages []provider.Message, opts provider.StreamOptions) (<-chan provider.Chunk, <-chan error) {
	chunks := make(chan provider.Chunk, 32)
	errs := make(chan error, 1)

	go func() {
		defer close(chunks)
		defer close(errs)

		model := opts.Model
		if model == "" {
			model = c.DefaultModel()
		}
		toolList := opts.Tools
		if toolList == nil {
			toolList = definitions.All()
		}

		logger.Debugf("%s.streamChat: model=%s baseURL=%s messages=%d tools=%d",
			c.Name(), model, c.cfg.BaseURL, len(messages), len(toolList))

		req := chatReq{
			Model:    model,
			Messages: translateMessages(messages),
			Tools:    translateTools(toolList),
			Stream:   true,
			Options: map[string]any{
				"num_predict": c.cfg.MaxTokens,
			},
		}
		body, err := json.Marshal(req)
		if err != nil {
			errs <- err
			return
		}
		url := strings.TrimRight(c.cfg.BaseURL, "/") + "/api/chat"
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			errs <- err
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if c.cfg.APIKey != "" {
			header := c.cfg.AuthHeader
			if header == "" {
				header = "Authorization"
			}
			prefix := c.cfg.AuthPrefix
			if prefix == "" && header == "Authorization" {
				prefix = "Bearer "
			}
			httpReq.Header.Set(header, prefix+c.cfg.APIKey)
		}
		for k, v := range c.cfg.Headers {
			httpReq.Header.Set(k, v)
		}

		client := c.HTTPClient
		if client == nil {
			client = http.DefaultClient
		}
		resp, err := client.Do(httpReq)
		if err != nil {
			logger.Errorf("%s.streamChat request failed: %v", c.Name(), err)
			errs <- err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			logger.Errorf("%s.streamChat status=%d body=%s", c.Name(), resp.StatusCode, string(b))
			errs <- parseAPIError(c.Name(), model, resp.StatusCode, b)
			return
		}

		var stopReason string
		var usage *provider.Usage
		toolCallIdx := 0

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var r chatRes
			if err := json.Unmarshal([]byte(line), &r); err != nil {
				logger.Warnf("%s.streamChat: bad chunk: %v line=%s", c.Name(), err, line)
				continue
			}
			if r.Message != nil {
				if r.Message.Content != "" {
					chunks <- provider.Chunk{Text: r.Message.Content}
				}
				for _, tc := range r.Message.ToolCalls {
					argsJSON, _ := json.Marshal(tc.Function.Arguments)
					toolCallIdx++
					chunks <- provider.Chunk{ToolCall: &provider.ToolCall{
						ID:        fmt.Sprintf("call_%d", toolCallIdx),
						Name:      tc.Function.Name,
						Arguments: string(argsJSON),
					}}
				}
			}
			if r.Done {
				usage = &provider.Usage{
					InputTokens:  r.PromptEvalCount,
					OutputTokens: r.EvalCount,
					TotalTokens:  r.PromptEvalCount + r.EvalCount,
				}
				stopReason = r.DoneReason
				if stopReason == "" {
					stopReason = "stop"
				}
				if stopReason == "length" {
					stopReason = "max_tokens"
				}
			}
		}
		if err := scanner.Err(); err != nil {
			errs <- err
			return
		}
		if usage != nil {
			chunks <- provider.Chunk{Usage: usage}
		}
		chunks <- provider.Chunk{Done: true, StopReason: stopReason}
	}()

	return chunks, errs
}

func translateMessages(in []provider.Message) []ollamaMsg {
	out := make([]ollamaMsg, 0, len(in))
	for _, m := range in {
		role := m.Role
		// Ollama accepts system/user/assistant/tool roles natively.
		out = append(out, ollamaMsg{
			Role:    role,
			Content: contentToString(m.Content),
		})
	}
	return out
}

func translateTools(in []definitions.Tool) []ollamaTool {
	out := make([]ollamaTool, 0, len(in))
	for _, t := range in {
		schema := t.Function.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, ollamaTool{
			Type: "function",
			Function: ollamaToolDef{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  schema,
			},
		})
	}
	return out
}

func contentToString(c any) string {
	switch v := c.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, e := range v {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := m["text"].(string); t != "" {
				sb.WriteString(t)
			}
		}
		return sb.String()
	}
	b, _ := json.Marshal(c)
	return string(b)
}

// APIError is the parsed envelope of a non-2xx Ollama response.
type APIError struct {
	Provider   string
	Model      string
	HTTPStatus int
	Message    string
	RawBody    string
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return fmt.Sprintf("%s HTTP %d", e.Provider, e.HTTPStatus)
	}
	return fmt.Sprintf("%s HTTP %d: %s", e.Provider, e.HTTPStatus, e.Message)
}

func parseAPIError(providerName, model string, status int, body []byte) error {
	apiErr := &APIError{Provider: providerName, Model: model, HTTPStatus: status, RawBody: string(body)}
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err == nil {
		apiErr.Message = env.Error
	}
	if apiErr.Message == "" {
		apiErr.Message = string(body)
	}
	m := strings.ToLower(apiErr.Message)
	for _, needle := range []string{"context length", "context window", "too many tokens"} {
		if strings.Contains(m, needle) {
			return &provider.ContextOverflowError{
				Provider: providerName, Model: model,
				Message: apiErr.Message, Cause: apiErr,
			}
		}
	}
	return apiErr
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
