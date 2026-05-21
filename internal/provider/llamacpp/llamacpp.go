package llamacpp

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
)

const Kind = "llama.cpp"

func init() {
	provider.Register(Kind, func(cfg provider.Config) (provider.Provider, error) {
		return New(cfg), nil
	})
	provider.Register("llamacpp", func(cfg provider.Config) (provider.Provider, error) {
		cfg.Kind = Kind
		return New(cfg), nil
	})
}

type Client struct {
	cfg        provider.Config
	HTTPClient *http.Client
}

func New(cfg provider.Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://127.0.0.1:8080"
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 4096
	}
	return &Client{cfg: cfg, HTTPClient: &http.Client{}}
}

func (c *Client) Name() string                          { return firstNonEmpty(c.cfg.Name, Kind) }
func (c *Client) Kind() string                          { return Kind }
func (c *Client) AvailableModels() []provider.ModelInfo { return c.models(false) }
func (c *Client) DefaultModel() string {
	if c.cfg.DefaultModel != "" {
		return c.cfg.DefaultModel
	}
	if models := c.models(true); len(models) > 0 {
		return models[0].ModelID
	}
	return ""
}
func (c *Client) ContextWindow(model string) int {
	_ = model
	if c.cfg.ContextWindow > 0 {
		return c.cfg.ContextWindow
	}
	return 13_312
}

func (c *Client) StreamChat(ctx context.Context, messages []provider.Message, opts provider.StreamOptions) (<-chan provider.Chunk, <-chan error) {
	chunks := make(chan provider.Chunk, 32)
	errs := make(chan error, 1)
	go func() {
		defer close(chunks)
		defer close(errs)
		model := c.upstreamModel(opts.Model)
		logger.Debugf("%s.streamChat: model=%s baseURL=%s messages=%d", c.Name(), model, c.cfg.BaseURL, len(messages))
		req := chatRequest{
			Model:         model,
			Messages:      messages,
			Stream:        true,
			MaxTokens:     c.cfg.MaxTokens,
			StreamOptions: &streamOptions{IncludeUsage: true},
		}
		body, err := json.Marshal(req)
		if err != nil {
			errs <- err
			return
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.BaseURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
		if err != nil {
			errs <- err
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		c.setAuth(httpReq)
		resp, err := c.httpClient().Do(httpReq)
		if err != nil {
			errs <- err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			errs <- fmt.Errorf("%s HTTP %d: %s", c.Name(), resp.StatusCode, strings.TrimSpace(string(b)))
			return
		}
		if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
			c.readSSE(resp.Body, chunks, errs)
			return
		}
		c.readJSON(resp.Body, chunks, errs)
	}()
	return chunks, errs
}

type chatRequest struct {
	Model         string             `json:"model,omitempty"`
	Messages      []provider.Message `json:"messages"`
	Stream        bool               `json:"stream"`
	MaxTokens     int                `json:"max_tokens,omitempty"`
	StreamOptions *streamOptions     `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta *struct {
			Content          string `json:"content,omitempty"`
			ReasoningContent string `json:"reasoning_content,omitempty"`
		} `json:"delta,omitempty"`
		Message *struct {
			Content          string `json:"content,omitempty"`
			ReasoningContent string `json:"reasoning_content,omitempty"`
		} `json:"message,omitempty"`
		FinishReason string `json:"finish_reason,omitempty"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}

func (c *Client) readSSE(body io.Reader, chunks chan<- provider.Chunk, errs chan<- error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var stop string
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		c.emitChatJSON([]byte(data), chunks, &stop)
	}
	if err := scanner.Err(); err != nil {
		errs <- err
		return
	}
	chunks <- provider.Chunk{Done: true, StopReason: stop}
	errs <- nil
}

func (c *Client) readJSON(body io.Reader, chunks chan<- provider.Chunk, errs chan<- error) {
	raw, err := io.ReadAll(io.LimitReader(body, 8<<20))
	if err != nil {
		errs <- err
		return
	}
	var stop string
	c.emitChatJSON(raw, chunks, &stop)
	chunks <- provider.Chunk{Done: true, StopReason: stop}
	errs <- nil
}

func (c *Client) emitChatJSON(raw []byte, chunks chan<- provider.Chunk, stop *string) {
	var resp chatResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		logger.Warnf("%s.streamChat: bad chunk: %v", c.Name(), err)
		return
	}
	if len(resp.Choices) > 0 {
		ch := resp.Choices[0]
		if ch.Delta != nil {
			if ch.Delta.ReasoningContent != "" {
				chunks <- provider.Chunk{Thinking: ch.Delta.ReasoningContent}
			}
			if ch.Delta.Content != "" {
				chunks <- provider.Chunk{Text: ch.Delta.Content}
			}
		}
		if ch.Message != nil {
			if ch.Message.ReasoningContent != "" {
				chunks <- provider.Chunk{Thinking: ch.Message.ReasoningContent}
			}
			if ch.Message.Content != "" {
				chunks <- provider.Chunk{Text: ch.Message.Content}
			}
		}
		if ch.FinishReason != "" && stop != nil {
			*stop = ch.FinishReason
		}
	}
	if resp.Usage != nil {
		chunks <- provider.Chunk{Usage: &provider.Usage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
			TotalTokens:  resp.Usage.TotalTokens,
		}}
	}
}

func (c *Client) upstreamModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" || model == c.cfg.DefaultModel {
		return ""
	}
	return model
}

func (c *Client) setAuth(req *http.Request) {
	if c.cfg.APIKey == "" {
		return
	}
	header := firstNonEmpty(c.cfg.AuthHeader, "Authorization")
	prefix := c.cfg.AuthPrefix
	if header == "Authorization" && prefix == "" {
		prefix = "Bearer "
	}
	req.Header.Set(header, prefix+c.cfg.APIKey)
	for k, v := range c.cfg.Headers {
		req.Header.Set(k, v)
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
