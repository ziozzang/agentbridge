// Package anthropic implements the Anthropic Messages API as a harness
// provider.
//
// Endpoint:   POST <BaseURL>/v1/messages
// Streaming:  text/event-stream with named events
//
//	(`event: content_block_delta\ndata: {...}`).
//
// Auth:       x-api-key header (no "Bearer " prefix).
//
//	anthropic-version header is mandatory.
//
// The adapter translates between the harness's neutral OpenAI-Chat-shaped
// messages and Anthropic's content-block format:
//   - `system` messages → top-level `system` string.
//   - `assistant.tool_calls[]` → assistant content block `tool_use`.
//   - `tool` messages → user content block `tool_result`.
//   - function tool definitions → Anthropic `tools[]` with `input_schema`.
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ziozzang/agentbridge/internal/logger"
	"github.com/ziozzang/agentbridge/internal/provider"
	"github.com/ziozzang/agentbridge/internal/tools/definitions"
)

// Kind is the registry key for the Anthropic Messages API.
const Kind = "anthropic"

// DefaultAPIVersion is the value sent in the `anthropic-version` header
// when no override is supplied.
const DefaultAPIVersion = "2023-06-01"

func init() {
	provider.Register(Kind, func(cfg provider.Config) (provider.Provider, error) {
		return New(cfg), nil
	})
}

// Client is an Anthropic Messages provider.
type Client struct {
	cfg        provider.Config
	HTTPClient *http.Client
	APIVersion string
}

// New constructs an Anthropic client.
func New(cfg provider.Config) *Client {
	if cfg.AuthHeader == "" {
		cfg.AuthHeader = "x-api-key"
	}
	if strings.EqualFold(cfg.AuthHeader, "Authorization") {
		if cfg.AuthPrefix == "" {
			cfg.AuthPrefix = "Bearer "
		}
	} else {
		// Anthropic direct API does NOT use Bearer.
		cfg.AuthPrefix = ""
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 4096
	}
	version := DefaultAPIVersion
	if cfg.Extra != nil {
		if v, ok := cfg.Extra["anthropic_version"].(string); ok && v != "" {
			version = v
		}
	}
	return &Client{cfg: cfg, HTTPClient: &http.Client{}, APIVersion: version}
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
	return 200_000
}

// Config exposes the resolved provider configuration (read-only).
func (c *Client) Config() provider.Config { return c.cfg }

// ----- Request shape ------------------------------------------------------

type messagesRequest struct {
	Model     string       `json:"model"`
	Messages  []anthroMsg  `json:"messages"`
	System    any          `json:"system,omitempty"`
	Tools     []anthroTool `json:"tools,omitempty"`
	MaxTokens int          `json:"max_tokens"`
	Stream    bool         `json:"stream"`
}

type anthroMsg struct {
	Role    string       `json:"role"`
	Content []anthroPart `json:"content"`
}

// anthroPart covers the four content-block shapes we emit. The unused
// fields stay zero and `omitempty` keeps the JSON tight.
type anthroPart struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      string          `json:"content,omitempty"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

type anthroTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ----- Stream shape -------------------------------------------------------

// anthroStreamEvent is the union of SSE payloads we care about. Anthropic
// sends one JSON document per `data:` line, identified by `type`.
type anthroStreamEvent struct {
	Type         string          `json:"type"`
	Index        int             `json:"index"`
	Delta        json.RawMessage `json:"delta"`
	ContentBlock json.RawMessage `json:"content_block"`
	Message      json.RawMessage `json:"message"`
	Usage        *anthroUsage    `json:"usage,omitempty"`
}

type anthroUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// blockStart captures `content_block_start.content_block` so we know each
// block's per-index type, id, and (for tool_use) name.
type blockStart struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Text  string `json:"text"`
	Input any    `json:"input"`
}

type textDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type inputDelta struct {
	Type        string `json:"type"`
	PartialJSON string `json:"partial_json"`
}

type messageDeltaWrap struct {
	StopReason string       `json:"stop_reason"`
	Usage      *anthroUsage `json:"usage"`
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

		system, msgs := translateMessages(messages)
		systemPayload, msgs := c.applyPromptCache(system, msgs)
		logger.Debugf("%s.streamChat: model=%s msgs=%d tools=%d system=%d",
			c.Name(), model, len(msgs), len(toolList), len(system))

		req := messagesRequest{
			Model:     model,
			Messages:  msgs,
			System:    systemPayload,
			Tools:     translateTools(toolList),
			MaxTokens: c.cfg.MaxTokens,
			Stream:    true,
		}
		body, err := json.Marshal(req)
		if err != nil {
			errs <- err
			return
		}
		endpoint := c.messagesURL(model)
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			errs <- err
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("anthropic-version", c.APIVersion)
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

		// Track per-index block state so partial_json deltas land in the
		// right tool_use buffer.
		type pendingBlock struct {
			Type    string
			ID      string
			Name    string
			JSONBuf strings.Builder
		}
		blocks := map[int]*pendingBlock{}
		var stopReason string

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
			var ev anthroStreamEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				logger.Warnf("%s.streamChat: bad chunk: %v data=%s", c.Name(), err, data)
				continue
			}
			switch ev.Type {
			case "content_block_start":
				var bs blockStart
				_ = json.Unmarshal(ev.ContentBlock, &bs)
				blocks[ev.Index] = &pendingBlock{Type: bs.Type, ID: bs.ID, Name: bs.Name}
			case "content_block_delta":
				blk, ok := blocks[ev.Index]
				if !ok {
					blk = &pendingBlock{}
					blocks[ev.Index] = blk
				}
				// Inspect delta.type to decide which sub-shape to use.
				var probe struct {
					Type string `json:"type"`
				}
				_ = json.Unmarshal(ev.Delta, &probe)
				switch probe.Type {
				case "text_delta":
					var td textDelta
					_ = json.Unmarshal(ev.Delta, &td)
					if td.Text != "" {
						chunks <- provider.Chunk{Text: td.Text}
					}
				case "input_json_delta":
					var id inputDelta
					_ = json.Unmarshal(ev.Delta, &id)
					if id.PartialJSON != "" {
						blk.JSONBuf.WriteString(id.PartialJSON)
					}
				case "thinking_delta":
					// Anthropic extended-thinking models surface reasoning
					// via thinking_delta.thinking.
					var td struct {
						Thinking string `json:"thinking"`
					}
					_ = json.Unmarshal(ev.Delta, &td)
					if td.Thinking != "" {
						chunks <- provider.Chunk{Thinking: td.Thinking}
					}
				}
			case "content_block_stop":
				blk, ok := blocks[ev.Index]
				if !ok {
					continue
				}
				if blk.Type == "tool_use" && blk.ID != "" && blk.Name != "" {
					args := blk.JSONBuf.String()
					if args == "" {
						args = "{}"
					}
					tcCopy := provider.ToolCall{ID: blk.ID, Name: blk.Name, Arguments: args}
					chunks <- provider.Chunk{ToolCall: &tcCopy}
				}
			case "message_delta":
				var md messageDeltaWrap
				_ = json.Unmarshal(ev.Delta, &md)
				if md.StopReason != "" {
					stopReason = mapStopReason(md.StopReason)
				}
				if md.Usage != nil {
					chunks <- provider.Chunk{Usage: &provider.Usage{
						InputTokens:      md.Usage.InputTokens,
						OutputTokens:     md.Usage.OutputTokens,
						TotalTokens:      md.Usage.InputTokens + md.Usage.OutputTokens,
						CachedReadTokens: md.Usage.CacheReadInputTokens,
					}}
				}
			case "message_stop":
				// nothing extra to do – stop_reason already captured in
				// message_delta above.
			case "error":
				// Anthropic streams an error event for in-flight failures.
				var e struct {
					Error struct {
						Type    string `json:"type"`
						Message string `json:"message"`
					} `json:"error"`
				}
				_ = json.Unmarshal([]byte(data), &e)
				if e.Error.Type == "overloaded_error" || isContextOverflowText(e.Error.Message) {
					errs <- &provider.ContextOverflowError{
						Provider: c.Name(), Model: model,
						Message: e.Error.Message,
					}
					return
				}
				errs <- fmt.Errorf("anthropic stream error: %s: %s", e.Error.Type, e.Error.Message)
				return
			}
		}
		if err := scanner.Err(); err != nil {
			errs <- err
			return
		}
		chunks <- provider.Chunk{Done: true, StopReason: stopReason}
	}()

	return chunks, errs
}

func (c *Client) messagesURL(model string) string {
	if project := c.extraString("vertex_project_id"); project != "" {
		base := strings.TrimRight(c.cfg.BaseURL, "/")
		if base == "" {
			base = "https://aiplatform.googleapis.com"
		}
		if !strings.HasSuffix(base, "/v1") {
			base += "/v1"
		}
		region := firstNonEmpty(c.extraString("vertex_region"), "global")
		return base + "/projects/" + url.PathEscape(project) + "/locations/" + url.PathEscape(region) + "/publishers/anthropic/models/" + url.PathEscape(model) + ":streamRawPredict"
	}
	return strings.TrimRight(c.cfg.BaseURL, "/") + "/v1/messages"
}

// translateMessages converts harness-neutral OpenAI-shaped messages into
// Anthropic's `system` string + content-block messages.
func translateMessages(in []provider.Message) (system string, out []anthroMsg) {
	var sysParts []string
	for _, m := range in {
		switch m.Role {
		case "system":
			if s := contentToString(m.Content); s != "" {
				sysParts = append(sysParts, s)
			}
		case "user":
			out = append(out, anthroMsg{Role: "user", Content: []anthroPart{
				{Type: "text", Text: contentToString(m.Content)},
			}})
		case "assistant":
			parts := []anthroPart{}
			if s := contentToString(m.Content); s != "" {
				parts = append(parts, anthroPart{Type: "text", Text: s})
			}
			for _, tc := range m.ToolCalls {
				args := tc.Function.Arguments
				if args == "" {
					args = "{}"
				}
				parts = append(parts, anthroPart{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: json.RawMessage(args),
				})
			}
			if len(parts) == 0 {
				parts = append(parts, anthroPart{Type: "text", Text: ""})
			}
			out = append(out, anthroMsg{Role: "assistant", Content: parts})
		case "tool":
			out = append(out, anthroMsg{Role: "user", Content: []anthroPart{{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   contentToString(m.Content),
			}}})
		}
	}
	return strings.Join(sysParts, "\n\n"), out
}

func translateTools(in []definitions.Tool) []anthroTool {
	out := make([]anthroTool, 0, len(in))
	for _, t := range in {
		schema := t.Function.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, anthroTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: schema,
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
		// OpenAI-style content arrays; concatenate text parts.
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

// mapStopReason translates Anthropic's stop_reason values into the harness's
// neutral set (which mirrors OpenAI Chat: end_turn / max_tokens / refusal /
// stop / tool_calls). Unknown values pass through unchanged.
func mapStopReason(in string) string {
	switch in {
	case "end_turn":
		return "end_turn"
	case "max_tokens":
		return "max_tokens"
	case "stop_sequence":
		return "end_turn"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "refusal"
	default:
		return in
	}
}

// APIError is the parsed envelope of a non-2xx Anthropic response.
type APIError struct {
	Provider   string
	Model      string
	HTTPStatus int
	Type       string
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
	return fmt.Sprintf("%s HTTP %d (%s): %s", e.Provider, e.HTTPStatus, e.Type, e.Message)
}

func parseAPIError(providerName, model string, status int, body []byte) error {
	var env struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	apiErr := &APIError{Provider: providerName, Model: model, HTTPStatus: status, RawBody: string(body)}
	if err := json.Unmarshal(body, &env); err == nil {
		apiErr.Type = env.Error.Type
		apiErr.Message = env.Error.Message
	}
	if apiErr.Message == "" {
		apiErr.Message = string(body)
	}
	// Anthropic returns 400 with type "invalid_request_error" and a
	// "prompt is too long" message when the input overflows the window.
	if isContextOverflowText(apiErr.Message) {
		return &provider.ContextOverflowError{
			Provider: providerName, Model: model,
			Message: apiErr.Message, Cause: apiErr,
		}
	}
	return apiErr
}

func isContextOverflowText(msg string) bool {
	m := strings.ToLower(msg)
	for _, needle := range []string{
		"prompt is too long",
		"context length",
		"context window",
		"too many tokens",
		"maximum context",
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

func (c *Client) applyPromptCache(system string, messages []anthroMsg) (any, []anthroMsg) {
	mode := strings.ToLower(c.extraString("prompt_cache"))
	if mode == "" {
		mode = strings.ToLower(c.extraString("cache_control"))
	}
	if mode == "" || mode == "off" || mode == "false" || mode == "0" || mode == "disabled" {
		if system == "" {
			return nil, messages
		}
		return system, messages
	}
	marker := cacheControl{Type: "ephemeral"}
	if c.extraString("prompt_cache_ttl") == "1h" || c.extraString("cache_control_ttl") == "1h" {
		marker.TTL = "1h"
	}
	out := make([]anthroMsg, len(messages))
	copy(out, messages)
	breakpoints := 0
	var systemPayload any
	if system != "" {
		systemPayload = []anthroPart{{Type: "text", Text: system, CacheControl: &marker}}
		breakpoints++
	}
	remaining := 4 - breakpoints
	for i := len(out) - 1; i >= 0 && remaining > 0; i-- {
		if len(out[i].Content) == 0 {
			continue
		}
		content := append([]anthroPart(nil), out[i].Content...)
		content[len(content)-1].CacheControl = &marker
		out[i].Content = content
		remaining--
	}
	if systemPayload == nil && system != "" {
		systemPayload = system
	}
	return systemPayload, out
}

func (c *Client) extraString(key string) string {
	if c.cfg.Extra == nil {
		return ""
	}
	v, _ := c.cfg.Extra[key].(string)
	return strings.TrimSpace(v)
}
