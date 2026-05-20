// Package openaichat implements the OpenAI-compatible Chat Completions
// provider. It is the workhorse of the harness — GLM, OpenRouter, LiteLLM,
// Ollama (newer versions), and most self-hosted servers expose this exact
// shape, so a configurable BaseURL + Auth header is all that's needed.
//
// Endpoint: POST <BaseURL>/chat/completions
// Streaming: text/event-stream with `data: { ... }` JSON lines and a final
// `data: [DONE]` sentinel. Aggregates tool-call deltas by index.
package openaichat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/ziozzang/agentbridge/internal/logger"
	"github.com/ziozzang/agentbridge/internal/provider"
	"github.com/ziozzang/agentbridge/internal/tools/definitions"
)

// Kind is the registry key for this provider shape.
const Kind = "openai-chat"

func init() {
	provider.Register(Kind, func(cfg provider.Config) (provider.Provider, error) {
		return New(cfg), nil
	})
}

// Client is a Chat Completions provider.
type Client struct {
	cfg        provider.Config
	HTTPClient *http.Client
}

// New constructs a Client. The Config's BaseURL and APIKey are honoured; if
// AuthHeader is empty, "Authorization" with "Bearer " prefix is used.
func New(cfg provider.Config) *Client {
	if cfg.AuthHeader == "" {
		cfg.AuthHeader = "Authorization"
	}
	if cfg.AuthHeader == "Authorization" && cfg.AuthPrefix == "" {
		cfg.AuthPrefix = "Bearer "
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 8192
	}
	return &Client{cfg: cfg, HTTPClient: &http.Client{}}
}

// Name implements provider.Provider.
func (c *Client) Name() string { return firstNonEmpty(c.cfg.Name, Kind) }

// Kind implements provider.Provider.
func (c *Client) Kind() string { return Kind }

// AvailableModels implements provider.Provider.
func (c *Client) AvailableModels() []provider.ModelInfo {
	out := make([]provider.ModelInfo, len(c.cfg.Models))
	copy(out, c.cfg.Models)
	return out
}

// DefaultModel implements provider.Provider.
func (c *Client) DefaultModel() string {
	if c.cfg.DefaultModel != "" {
		return c.cfg.DefaultModel
	}
	if len(c.cfg.Models) > 0 {
		return c.cfg.Models[0].ModelID
	}
	return ""
}

// ContextWindow implements provider.Provider.
func (c *Client) ContextWindow(model string) int {
	_ = model
	if c.cfg.ContextWindow > 0 {
		return c.cfg.ContextWindow
	}
	return 128_000
}

// Config returns the resolved provider configuration (read-only view).
func (c *Client) Config() provider.Config { return c.cfg }

// chatRequest mirrors the OpenAI Chat Completions request shape.
type chatRequest struct {
	Model         string             `json:"model"`
	Messages      []provider.Message `json:"messages"`
	Tools         []definitions.Tool `json:"tools,omitempty"`
	ToolChoice    string             `json:"tool_choice,omitempty"`
	Stream        bool               `json:"stream"`
	StreamOptions *streamOptions     `json:"stream_options,omitempty"`
	MaxTokens     int                `json:"max_tokens,omitempty"`
	// Thinking is a GLM-specific extension; included on every request when
	// the provider is configured with cfg.Thinking != "".
	Thinking *thinkingObj `json:"thinking,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type thinkingObj struct {
	Type string `json:"type"`
}

type streamChunk struct {
	Choices []struct {
		Index        int          `json:"index"`
		Delta        deltaPayload `json:"delta"`
		FinishReason *string      `json:"finish_reason"`
	} `json:"choices"`
	Usage *rawUsage `json:"usage"`
}

type deltaPayload struct {
	Content          string          `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        []deltaToolCall `json:"tool_calls,omitempty"`
}

type deltaToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function *struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function,omitempty"`
}

type rawUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details,omitempty"`
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

		req := chatRequest{
			Model:         model,
			Messages:      messages,
			Tools:         toolList,
			ToolChoice:    "auto",
			Stream:        true,
			StreamOptions: &streamOptions{IncludeUsage: true},
			MaxTokens:     c.cfg.MaxTokens,
		}
		if c.cfg.Thinking != "" {
			req.Thinking = &thinkingObj{Type: c.cfg.Thinking}
		}

		body, err := json.Marshal(req)
		if err != nil {
			errs <- err
			return
		}
		url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			errs <- err
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		if c.cfg.APIKey != "" {
			httpReq.Header.Set(c.cfg.AuthHeader, c.cfg.AuthPrefix+c.cfg.APIKey)
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

		pendingTC := map[int]*provider.ToolCall{}
		var lastFinish string

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "" || data == "[DONE]" {
				continue
			}
			var chunk streamChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				logger.Warnf("%s.streamChat: bad chunk: %v data=%s", c.Name(), err, data)
				continue
			}
			if len(chunk.Choices) > 0 {
				ch := chunk.Choices[0]
				if ch.Delta.ReasoningContent != "" {
					chunks <- provider.Chunk{Thinking: ch.Delta.ReasoningContent}
				}
				if ch.Delta.Content != "" {
					chunks <- provider.Chunk{Text: ch.Delta.Content}
				}
				for _, tc := range ch.Delta.ToolCalls {
					p, ok := pendingTC[tc.Index]
					if !ok {
						p = &provider.ToolCall{}
						pendingTC[tc.Index] = p
					}
					if tc.ID != "" {
						p.ID = tc.ID
					}
					if tc.Function != nil {
						if tc.Function.Name != "" {
							p.Name = tc.Function.Name
						}
						if tc.Function.Arguments != "" {
							p.Arguments += tc.Function.Arguments
						}
					}
				}
				if ch.FinishReason != nil && *ch.FinishReason != "" {
					lastFinish = *ch.FinishReason
				}
			}
			if chunk.Usage != nil {
				u := &provider.Usage{
					InputTokens:  chunk.Usage.PromptTokens,
					OutputTokens: chunk.Usage.CompletionTokens,
					TotalTokens:  chunk.Usage.TotalTokens,
				}
				if chunk.Usage.PromptTokensDetails != nil {
					u.CachedReadTokens = chunk.Usage.PromptTokensDetails.CachedTokens
				}
				if chunk.Usage.CompletionTokensDetails != nil {
					u.ThoughtTokens = chunk.Usage.CompletionTokensDetails.ReasoningTokens
				}
				chunks <- provider.Chunk{Usage: u}
			}
		}
		if err := scanner.Err(); err != nil {
			errs <- err
			return
		}
		// Flush assembled tool-calls in stable index order.
		indices := make([]int, 0, len(pendingTC))
		for i := range pendingTC {
			indices = append(indices, i)
		}
		sort.Ints(indices)
		for _, i := range indices {
			tc := pendingTC[i]
			if tc.ID != "" && tc.Name != "" {
				cp := *tc
				chunks <- provider.Chunk{ToolCall: &cp}
			}
		}
		chunks <- provider.Chunk{Done: true, StopReason: lastFinish}
	}()

	return chunks, errs
}

// APIError is the parsed envelope of a non-2xx response.
type APIError struct {
	Provider   string
	Model      string
	HTTPStatus int
	Code       any
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

// parseAPIError extracts the OpenAI-style error envelope and surfaces a
// context-overflow as the typed provider error so the agent loop can react.
func parseAPIError(providerName, model string, status int, body []byte) error {
	var envelope struct {
		Error struct {
			Code    any    `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	apiErr := &APIError{Provider: providerName, Model: model, HTTPStatus: status, RawBody: string(body)}
	if err := json.Unmarshal(body, &envelope); err == nil {
		apiErr.Code = envelope.Error.Code
		apiErr.Message = envelope.Error.Message
	}
	if apiErr.Message == "" {
		apiErr.Message = string(body)
	}
	if isContextOverflowCode(apiErr.Code, apiErr.Message) {
		return &provider.ContextOverflowError{
			Provider: providerName,
			Model:    model,
			Message:  apiErr.Message,
			Cause:    apiErr,
		}
	}
	return apiErr
}

// isContextOverflowCode covers the codes/messages various OpenAI-compatible
// servers use for context overflow. GLM uses code "1261"; OpenAI returns
// `error.code = "context_length_exceeded"`; Ollama mentions "context window";
// Anthropic surfaces "max_tokens_to_sample" but only via that adapter.
func isContextOverflowCode(code any, message string) bool {
	switch v := code.(type) {
	case string:
		if v == "1261" || v == "context_length_exceeded" || v == "max_tokens_exceeded" {
			return true
		}
	case float64:
		if v == 1261 {
			return true
		}
	}
	m := strings.ToLower(message)
	for _, needle := range []string{
		"context length",
		"context window",
		"too many tokens",
		"maximum context",
		"context overflow",
	} {
		if strings.Contains(m, needle) {
			return true
		}
	}
	return false
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
