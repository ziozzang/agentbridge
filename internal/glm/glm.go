// Package glm wraps the OpenAI-compatible Z.AI Coding Plan API for the
// glm-acp-agent. It exposes a streaming chat-completions interface with the
// GLM-specific "thinking" extension.
package glm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/ziozzang/glm-acp/internal/credentials"
	"github.com/ziozzang/glm-acp/internal/logger"
	"github.com/ziozzang/glm-acp/internal/tools/definitions"
)

// DefaultBaseURL is the default Z.AI Coding Plan endpoint.
const DefaultBaseURL = "https://api.z.ai/api/coding/paas/v4"

// DefaultModel is the model used when no override is provided.
const DefaultModel = "glm-5.1"

// DefaultMaxTokens caps the per-call output tokens unless overridden.
const DefaultMaxTokens = 8192

// ModelInfo describes a model the agent advertises to ACP clients.
type ModelInfo struct {
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// BuiltinAvailableModels mirrors the curated TypeScript list.
var BuiltinAvailableModels = []ModelInfo{
	{ModelID: "glm-5.1", Name: "GLM-5.1", Description: "Latest GLM reasoning model with thinking mode"},
	{ModelID: "glm-5-turbo", Name: "GLM-5 Turbo", Description: "Faster Coding Plan reasoning model"},
	{ModelID: "glm-4.7", Name: "GLM-4.7", Description: "200K-context reasoning model"},
	{ModelID: "glm-4.5-air", Name: "GLM-4.5 Air", Description: "Lightweight, lower-latency model"},
}

// AvailableModels resolves the list of models advertised to clients, honouring
// the ACP_GLM_AVAILABLE_MODELS override.
func AvailableModels() []ModelInfo {
	override := os.Getenv("ACP_GLM_AVAILABLE_MODELS")
	if override == "" {
		return append([]ModelInfo(nil), BuiltinAvailableModels...)
	}
	var ids []string
	for _, p := range strings.Split(override, ",") {
		if s := strings.TrimSpace(p); s != "" {
			ids = append(ids, s)
		}
	}
	if len(ids) == 0 {
		return append([]ModelInfo(nil), BuiltinAvailableModels...)
	}
	out := make([]ModelInfo, 0, len(ids))
	for _, id := range ids {
		found := false
		for _, b := range BuiltinAvailableModels {
			if b.ModelID == id {
				out = append(out, b)
				found = true
				break
			}
		}
		if !found {
			out = append(out, ModelInfo{ModelID: id, Name: id})
		}
	}
	return out
}

// DefaultModelEnv returns the default model id, applying ACP_GLM_MODEL.
func DefaultModelEnv() string {
	if v := os.Getenv("ACP_GLM_MODEL"); v != "" {
		return v
	}
	return DefaultModel
}

var thinkingPattern = regexp.MustCompile(`(?i)^glm-(?:4\.[567]|5)`)

// ThinkingEnabled reports whether GLM "thinking" mode should be enabled for a
// given model. The user can force on/off via ACP_GLM_THINKING.
func ThinkingEnabled(model string) bool {
	if v, ok := os.LookupEnv("ACP_GLM_THINKING"); ok {
		lv := strings.ToLower(strings.TrimSpace(v))
		return lv == "true" || lv == "1"
	}
	return thinkingPattern.MatchString(model)
}

// MaxTokensEnv returns the configured per-call max output tokens.
func MaxTokensEnv() int {
	if v := os.Getenv("ACP_GLM_MAX_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return DefaultMaxTokens
}

// BaseURLEnv returns the API base URL, applying ACP_GLM_BASE_URL.
func BaseURLEnv() string {
	if v := os.Getenv("ACP_GLM_BASE_URL"); v != "" {
		return v
	}
	return DefaultBaseURL
}

// ToolCall is an assembled GLM function-call arriving from the stream.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Usage mirrors the ACP token-usage shape.
type Usage struct {
	InputTokens     int `json:"inputTokens"`
	OutputTokens    int `json:"outputTokens"`
	TotalTokens     int `json:"totalTokens"`
	CachedReadTokens int `json:"cachedReadTokens,omitempty"`
	ThoughtTokens   int `json:"thoughtTokens,omitempty"`
}

// Chunk is a single yielded streaming event.
type Chunk struct {
	Text     string
	Thinking string
	ToolCall *ToolCall
	Usage    *Usage
	Done     bool
	StopReason string
}

// Message is the OpenAI chat-completion message shape (keeps unknown fields
// via json.RawMessage so we can faithfully echo persisted history).
type Message struct {
	Role       string          `json:"role"`
	Content    any             `json:"content,omitempty"`
	Name       string          `json:"name,omitempty"`
	ToolCalls  []ToolCallMsg   `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

// ToolCallMsg is the message-shape of a recorded tool call.
type ToolCallMsg struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function ToolCallMsgFunction `json:"function"`
}

// ToolCallMsgFunction is the function payload for ToolCallMsg.
type ToolCallMsgFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// StreamOptions tunes a single StreamChat invocation.
type StreamOptions struct {
	Model string
	Tools []definitions.Tool
}

// Client wraps the HTTP-level chat-completions API.
type Client struct {
	APIKey     string
	BaseURL    string
	MaxTokens  int
	HTTPClient *http.Client
}

// New constructs a Client from environment configuration. Returns an error
// when no API key is available.
func New() (*Client, error) {
	key := credentials.Resolve()
	if key == "" {
		return nil, errors.New("No API key found. Set the Z_AI_API_KEY environment variable, or run `glm-acp-agent --setup` to store one.")
	}
	return &Client{
		APIKey:     key,
		BaseURL:    BaseURLEnv(),
		MaxTokens:  MaxTokensEnv(),
		HTTPClient: &http.Client{},
	}, nil
}

// chatRequest is the JSON body sent to /chat/completions.
type chatRequest struct {
	Model         string             `json:"model"`
	Messages      []Message          `json:"messages"`
	Tools         []definitions.Tool `json:"tools,omitempty"`
	ToolChoice    string             `json:"tool_choice,omitempty"`
	Stream        bool               `json:"stream"`
	StreamOptions *streamOptions     `json:"stream_options,omitempty"`
	MaxTokens     int                `json:"max_tokens,omitempty"`
	Thinking      *thinkingObj       `json:"thinking,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type thinkingObj struct {
	Type string `json:"type"`
}

// streamDelta mirrors the OpenAI streaming delta shape.
type streamChunk struct {
	Choices []struct {
		Index        int          `json:"index"`
		Delta        deltaPayload `json:"delta"`
		FinishReason *string      `json:"finish_reason"`
	} `json:"choices"`
	Usage *rawUsage `json:"usage"`
}

type deltaPayload struct {
	Content          string             `json:"content,omitempty"`
	ReasoningContent string             `json:"reasoning_content,omitempty"`
	ToolCalls        []deltaToolCall    `json:"tool_calls,omitempty"`
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
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details,omitempty"`
}

// StreamChat sends a streaming chat completion. Chunks are sent on the
// returned channel; the channel is closed when the stream completes or fails.
// The caller should drain it. The first error is sent on errCh.
func (c *Client) StreamChat(ctx context.Context, messages []Message, opts StreamOptions) (<-chan Chunk, <-chan error) {
	chunks := make(chan Chunk, 32)
	errs := make(chan error, 1)

	go func() {
		defer close(chunks)
		defer close(errs)

		model := opts.Model
		if model == "" {
			model = DefaultModelEnv()
		}
		toolList := opts.Tools
		if toolList == nil {
			toolList = definitions.All()
		}
		thinking := ThinkingEnabled(model)

		logger.Debugf("streamChat: model=%s baseURL=%s messages=%d tools=%d thinking=%v",
			model, c.BaseURL, len(messages), len(toolList), thinking)

		req := chatRequest{
			Model:         model,
			Messages:      messages,
			Tools:         toolList,
			ToolChoice:    "auto",
			Stream:        true,
			StreamOptions: &streamOptions{IncludeUsage: true},
			MaxTokens:     c.MaxTokens,
		}
		if thinking {
			req.Thinking = &thinkingObj{Type: "enabled"}
		}

		body, err := json.Marshal(req)
		if err != nil {
			errs <- err
			return
		}
		url := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			errs <- err
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)

		client := c.HTTPClient
		if client == nil {
			client = http.DefaultClient
		}
		resp, err := client.Do(httpReq)
		if err != nil {
			logger.Errorf("streamChat request failed: %v", err)
			errs <- err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			logger.Errorf("streamChat request failed: status=%d body=%s", resp.StatusCode, string(b))
			errs <- parseAPIError(resp.StatusCode, b)
			return
		}

		pendingTC := map[int]*ToolCall{}
		var lastFinish string

		// Parse SSE: lines starting with "data:". Each value is a JSON object
		// (or "[DONE]"). Aggregate tool-call deltas by index.
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
				logger.Warnf("streamChat: bad chunk: %v data=%s", err, data)
				continue
			}
			if len(chunk.Choices) > 0 {
				ch := chunk.Choices[0]
				if ch.Delta.ReasoningContent != "" {
					chunks <- Chunk{Thinking: ch.Delta.ReasoningContent}
				}
				if ch.Delta.Content != "" {
					chunks <- Chunk{Text: ch.Delta.Content}
				}
				for _, tc := range ch.Delta.ToolCalls {
					p, ok := pendingTC[tc.Index]
					if !ok {
						p = &ToolCall{}
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
				u := &Usage{
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
				logger.Debugf("streamChat usage: input=%d output=%d total=%d",
					u.InputTokens, u.OutputTokens, u.TotalTokens)
				chunks <- Chunk{Usage: u}
			}
		}
		if err := scanner.Err(); err != nil {
			errs <- err
			return
		}
		// Flush assembled tool-calls in stable index order.
		// Collect indices, sort, and emit.
		indices := make([]int, 0, len(pendingTC))
		for i := range pendingTC {
			indices = append(indices, i)
		}
		// Simple insertion sort (small)
		for i := 1; i < len(indices); i++ {
			for j := i; j > 0 && indices[j-1] > indices[j]; j-- {
				indices[j-1], indices[j] = indices[j], indices[j-1]
			}
		}
		for _, i := range indices {
			tc := pendingTC[i]
			if tc.ID != "" && tc.Name != "" {
				cp := *tc
				chunks <- Chunk{ToolCall: &cp}
			}
		}
		chunks <- Chunk{Done: true, StopReason: lastFinish}
	}()

	return chunks, errs
}

// parseAPIError extracts the Z.AI / OpenAI-style error envelope from a non-2xx
// response body so callers can distinguish business errors (e.g. context
// overflow, code 1261) from generic HTTP failures.
func parseAPIError(status int, body []byte) error {
var envelope struct {
Error struct {
Code    any    `json:"code"`
Message string `json:"message"`
} `json:"error"`
}
apiErr := &APIError{HTTPStatus: status, RawBody: string(body)}
if err := json.Unmarshal(body, &envelope); err == nil {
apiErr.Code = envelope.Error.Code
apiErr.Message = envelope.Error.Message
}
if apiErr.Message == "" {
apiErr.Message = string(body)
}
return apiErr
}
