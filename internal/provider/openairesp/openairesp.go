// Package openairesp implements the OpenAI Responses API as a harness
// provider.
//
// Endpoint:   POST <BaseURL>/v1/responses
// Streaming:  text/event-stream with `event:` + `data:` pairs. Relevant
//
//	event types we honour:
//	  - response.output_text.delta              (assistant text)
//	  - response.reasoning_summary_text.delta   (thinking)
//	  - response.function_call_arguments.delta  (tool arg stream)
//	  - response.output_item.added              (tool item start)
//	  - response.output_item.done               (tool item complete)
//	  - response.completed                      (final usage + status)
//	  - response.failed / response.error        (error envelopes)
//
// The harness's neutral message format is translated into the Responses
// API's `input` array shape. Function-calling tools are translated as
// {"type":"function", "name":..., "description":..., "parameters":...}
// without the wrapping `function` block used by Chat Completions.
package openairesp

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
const Kind = "openai-responses"

func init() {
	provider.Register(Kind, func(cfg provider.Config) (provider.Provider, error) {
		return New(cfg), nil
	})
}

// Client is an OpenAI Responses API provider.
type Client struct {
	cfg        provider.Config
	HTTPClient *http.Client
}

// New constructs a Responses API client.
func New(cfg provider.Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com"
	}
	if cfg.AuthHeader == "" {
		cfg.AuthHeader = "Authorization"
	}
	if cfg.AuthPrefix == "" && cfg.AuthHeader == "Authorization" {
		cfg.AuthPrefix = "Bearer "
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
	return 128_000
}

// Config exposes the resolved provider configuration (read-only).
func (c *Client) Config() provider.Config { return c.cfg }

// ----- Request shape ------------------------------------------------------

type respRequest struct {
	Model           string          `json:"model"`
	Input           []respInputItem `json:"input"`
	Instructions    string          `json:"instructions,omitempty"`
	Tools           []respTool      `json:"tools,omitempty"`
	ToolChoice      string          `json:"tool_choice,omitempty"`
	ParallelTools   bool            `json:"parallel_tool_calls"`
	Reasoning       *respReasoning  `json:"reasoning,omitempty"`
	Store           bool            `json:"store"`
	Stream          bool            `json:"stream"`
	Include         []string        `json:"include,omitempty"`
	ServiceTier     string          `json:"service_tier,omitempty"`
	PromptCacheKey  string          `json:"prompt_cache_key,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
}

type respReasoning struct {
	Effort string `json:"effort,omitempty"`
}

// respInputItem covers the four "input" shapes we emit. The unused fields
// stay zero and omitempty keeps the JSON tight.
type respInputItem struct {
	Type    string          `json:"type,omitempty"`
	Role    string          `json:"role,omitempty"`
	Content []respPart      `json:"content,omitempty"`
	CallID  string          `json:"call_id,omitempty"`
	Name    string          `json:"name,omitempty"`
	Args    json.RawMessage `json:"arguments,omitempty"`
	Output  string          `json:"output,omitempty"`
}

type respPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type respTool struct {
	Type               string          `json:"type"`
	Name               string          `json:"name,omitempty"`
	Description        string          `json:"description,omitempty"`
	Parameters         json.RawMessage `json:"parameters,omitempty"`
	ExternalWebAccess  *bool           `json:"external_web_access,omitempty"`
	Filters            map[string]any  `json:"filters,omitempty"`
	UserLocation       map[string]any  `json:"user_location,omitempty"`
	SearchContextSize  string          `json:"search_context_size,omitempty"`
	SearchContentTypes []string        `json:"search_content_types,omitempty"`
}

// ----- Stream shape -------------------------------------------------------

type respEvent struct {
	Type   string          `json:"type"`
	Delta  json.RawMessage `json:"delta"`
	Item   json.RawMessage `json:"item"`
	Output json.RawMessage `json:"output"`
	Usage  *respUsage      `json:"usage"`
	Error  *respError      `json:"error"`
	// For function_call_arguments.delta:
	ItemID    string `json:"item_id"`
	OutputIdx int    `json:"output_index"`
}

type respUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

type respError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type respOutputItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Status    string          `json:"status"`
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

		instructions, input := translateMessages(messages)
		if instructions == "" {
			instructions = c.extraString("instructions")
		}

		logger.Debugf("%s.streamChat: model=%s input_items=%d tools=%d",
			c.Name(), model, len(input), len(toolList))

		req := respRequest{
			Model:           model,
			Input:           input,
			Instructions:    instructions,
			Tools:           c.requestTools(toolList),
			ToolChoice:      "auto",
			ParallelTools:   c.extraBool("parallel_tool_calls", false),
			Reasoning:       c.reasoning(),
			Store:           c.extraBool("store", false),
			Stream:          true,
			Include:         c.includeFields(),
			ServiceTier:     c.extraString("service_tier"),
			PromptCacheKey:  c.extraString("prompt_cache_key"),
			MaxOutputTokens: c.maxOutputTokens(),
		}
		body, err := json.Marshal(req)
		if err != nil {
			errs <- err
			return
		}
		url := strings.TrimRight(c.cfg.BaseURL, "/") + c.responsesPath()
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

		// Per-call tool-call buffering. Streamed function_call_arguments
		// deltas arrive keyed by item_id; the output_item event provides
		// id, name, and call_id.
		type pendingTool struct {
			CallID string
			Name   string
			Args   strings.Builder
		}
		pending := map[string]*pendingTool{}
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
			var ev respEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				logger.Warnf("%s.streamChat: bad chunk: %v data=%s", c.Name(), err, data)
				continue
			}
			switch ev.Type {
			case "response.output_text.delta":
				var d struct {
					Delta string `json:"delta"`
				}
				_ = json.Unmarshal([]byte(data), &d)
				if d.Delta != "" {
					chunks <- provider.Chunk{Text: d.Delta}
				}
			case "response.reasoning_summary_text.delta",
				"response.reasoning.delta":
				var d struct {
					Delta string `json:"delta"`
				}
				_ = json.Unmarshal([]byte(data), &d)
				if d.Delta != "" {
					chunks <- provider.Chunk{Thinking: d.Delta}
				}
			case "response.output_item.added":
				var w struct {
					Item respOutputItem `json:"item"`
				}
				_ = json.Unmarshal([]byte(data), &w)
				if w.Item.Type == "function_call" {
					pending[w.Item.ID] = &pendingTool{
						CallID: firstNonEmpty(w.Item.CallID, w.Item.ID),
						Name:   w.Item.Name,
					}
				}
			case "response.function_call_arguments.delta":
				var d struct {
					ItemID string `json:"item_id"`
					Delta  string `json:"delta"`
				}
				_ = json.Unmarshal([]byte(data), &d)
				if p, ok := pending[d.ItemID]; ok {
					p.Args.WriteString(d.Delta)
				}
			case "response.output_item.done":
				var w struct {
					Item respOutputItem `json:"item"`
				}
				_ = json.Unmarshal([]byte(data), &w)
				if w.Item.Type == "function_call" {
					p, ok := pending[w.Item.ID]
					if !ok {
						p = &pendingTool{
							CallID: firstNonEmpty(w.Item.CallID, w.Item.ID),
							Name:   w.Item.Name,
						}
					}
					args := p.Args.String()
					if args == "" && len(w.Item.Arguments) > 0 {
						args = string(w.Item.Arguments)
					}
					if args == "" {
						args = "{}"
					}
					if p.CallID != "" && p.Name != "" {
						tc := provider.ToolCall{ID: p.CallID, Name: p.Name, Arguments: args}
						chunks <- provider.Chunk{ToolCall: &tc}
					}
					delete(pending, w.Item.ID)
				}
			case "response.completed":
				var w struct {
					Response struct {
						Status string     `json:"status"`
						Usage  *respUsage `json:"usage"`
					} `json:"response"`
				}
				_ = json.Unmarshal([]byte(data), &w)
				if w.Response.Usage != nil {
					u := &provider.Usage{
						InputTokens:  w.Response.Usage.InputTokens,
						OutputTokens: w.Response.Usage.OutputTokens,
						TotalTokens:  w.Response.Usage.TotalTokens,
					}
					if w.Response.Usage.InputTokensDetails != nil {
						u.CachedReadTokens = w.Response.Usage.InputTokensDetails.CachedTokens
					}
					if w.Response.Usage.OutputTokensDetails != nil {
						u.ThoughtTokens = w.Response.Usage.OutputTokensDetails.ReasoningTokens
					}
					chunks <- provider.Chunk{Usage: u}
				}
				stopReason = "end_turn"
				if len(pending) > 0 {
					stopReason = "tool_calls"
				}
			case "response.incomplete":
				stopReason = "max_tokens"
			case "response.failed", "response.error":
				if ev.Error != nil {
					if isContextOverflowText(ev.Error.Message) || ev.Error.Code == "context_length_exceeded" {
						errs <- &provider.ContextOverflowError{
							Provider: c.Name(), Model: model,
							Message: ev.Error.Message,
						}
						return
					}
					errs <- fmt.Errorf("openai-responses stream error: %s: %s", ev.Error.Code, ev.Error.Message)
					return
				}
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

func (c *Client) responsesPath() string {
	path := c.extraString("responses_path")
	if path == "" {
		return "/v1/responses"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

func (c *Client) maxOutputTokens() int {
	if c.extraBool("omit_max_output_tokens", false) {
		return 0
	}
	return c.cfg.MaxTokens
}

func (c *Client) reasoning() *respReasoning {
	effort := c.extraString("reasoning_effort")
	if effort == "" {
		return nil
	}
	return &respReasoning{Effort: effort}
}

func (c *Client) includeFields() []string {
	var include []string
	if c.extraBool("include_reasoning_encrypted", false) {
		include = append(include, "reasoning.encrypted_content")
	}
	return include
}

func (c *Client) requestTools(in []definitions.Tool) []respTool {
	if t, ok := c.webSearchTool(); ok {
		out := translateTools(filterFunctionTool(in, "web_search"))
		out = append([]respTool{t}, out...)
		return out
	}
	return translateTools(in)
}

func (c *Client) webSearchTool() (respTool, bool) {
	mode := strings.ToLower(c.extraString("web_search"))
	switch mode {
	case "", "disabled", "off", "false", "0", "none":
		return respTool{}, false
	case "live", "true", "1", "on":
		live := true
		return c.webSearchToolWithAccess(live), true
	case "cached":
		live := false
		return c.webSearchToolWithAccess(live), true
	default:
		logger.Warnf("%s: ignoring invalid web_search mode %q", c.Name(), mode)
		return respTool{}, false
	}
}

func (c *Client) webSearchToolWithAccess(live bool) respTool {
	nested := c.extraWebSearchConfig()
	t := respTool{Type: "web_search", ExternalWebAccess: &live}
	if contextSize := firstNonEmpty(c.extraString("web_search_context_size"), mapString(nested, "context_size")); contextSize != "" {
		t.SearchContextSize = strings.ToLower(contextSize)
	}
	if domains := c.extraStringSlice("web_search_allowed_domains"); len(domains) > 0 {
		t.Filters = map[string]any{"allowed_domains": domains}
	} else if domains := mapStringSlice(nested, "allowed_domains"); len(domains) > 0 {
		t.Filters = map[string]any{"allowed_domains": domains}
	}
	if loc := c.extraMap("web_search_location"); len(loc) > 0 {
		t.UserLocation = normalizeWebSearchLocation(loc)
	} else if loc := mapValue(nested, "location"); len(loc) > 0 {
		t.UserLocation = normalizeWebSearchLocation(loc)
	}
	if types := c.extraStringSlice("web_search_content_types"); len(types) > 0 {
		t.SearchContentTypes = types
	} else if types := mapStringSlice(nested, "search_content_types"); len(types) > 0 {
		t.SearchContentTypes = types
	}
	return t
}

func (c *Client) extraWebSearchConfig() map[string]any {
	tools := c.extraMap("tools")
	if len(tools) == 0 {
		return nil
	}
	return mapValue(tools, "web_search")
}

func (c *Client) extraString(key string) string {
	if c.cfg.Extra == nil {
		return ""
	}
	v, _ := c.cfg.Extra[key].(string)
	return strings.TrimSpace(v)
}

func (c *Client) extraStringSlice(key string) []string {
	if c.cfg.Extra == nil {
		return nil
	}
	return anyStringSlice(c.cfg.Extra[key])
}

func (c *Client) extraMap(key string) map[string]any {
	if c.cfg.Extra == nil {
		return nil
	}
	return anyMap(c.cfg.Extra[key])
}

func (c *Client) extraBool(key string, def bool) bool {
	if c.cfg.Extra == nil {
		return def
	}
	switch v := c.cfg.Extra[key].(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		default:
			return def
		}
	default:
		return def
	}
}

func anyStringSlice(v any) []string {
	switch x := v.(type) {
	case []string:
		out := make([]string, 0, len(x))
		for _, s := range x {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	case string:
		var out []string
		for _, part := range strings.Split(x, ",") {
			if s := strings.TrimSpace(part); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func anyMap(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		return x
	case map[any]any:
		out := map[string]any{}
		for k, v := range x {
			if s, ok := k.(string); ok {
				out[s] = v
			}
		}
		return out
	default:
		return nil
	}
}

func mapString(m map[string]any, key string) string {
	if len(m) == 0 {
		return ""
	}
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

func mapStringSlice(m map[string]any, key string) []string {
	if len(m) == 0 {
		return nil
	}
	return anyStringSlice(m[key])
}

func mapValue(m map[string]any, key string) map[string]any {
	if len(m) == 0 {
		return nil
	}
	return anyMap(m[key])
}

func normalizeWebSearchLocation(in map[string]any) map[string]any {
	out := map[string]any{"type": "approximate"}
	hasLocation := false
	for _, key := range []string{"country", "region", "city", "timezone"} {
		if s := mapString(in, key); s != "" {
			out[key] = s
			hasLocation = true
		}
	}
	if s := mapString(in, "type"); s != "" {
		out["type"] = s
		hasLocation = true
	}
	if !hasLocation {
		return nil
	}
	return out
}

// translateMessages converts harness-neutral messages into the Responses
// API's input-array shape, lifting system messages into `instructions`.
func translateMessages(in []provider.Message) (instructions string, items []respInputItem) {
	var sysParts []string
	for _, m := range in {
		switch m.Role {
		case "system":
			if s := contentToString(m.Content); s != "" {
				sysParts = append(sysParts, s)
			}
		case "user":
			items = append(items, respInputItem{
				Type: "message", Role: "user",
				Content: []respPart{{Type: "input_text", Text: contentToString(m.Content)}},
			})
		case "assistant":
			if s := contentToString(m.Content); s != "" {
				items = append(items, respInputItem{
					Type: "message", Role: "assistant",
					Content: []respPart{{Type: "output_text", Text: s}},
				})
			}
			for _, tc := range m.ToolCalls {
				args := tc.Function.Arguments
				if args == "" {
					args = "{}"
				}
				items = append(items, respInputItem{
					Type:   "function_call",
					CallID: tc.ID,
					Name:   tc.Function.Name,
					Args:   json.RawMessage(args),
				})
			}
		case "tool":
			items = append(items, respInputItem{
				Type:   "function_call_output",
				CallID: m.ToolCallID,
				Output: contentToString(m.Content),
			})
		}
	}
	return strings.Join(sysParts, "\n\n"), items
}

func translateTools(in []definitions.Tool) []respTool {
	out := make([]respTool, 0, len(in))
	for _, t := range in {
		schema := t.Function.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, respTool{
			Type:        "function",
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  schema,
		})
	}
	return out
}

func filterFunctionTool(in []definitions.Tool, name string) []definitions.Tool {
	out := make([]definitions.Tool, 0, len(in))
	for _, t := range in {
		if t.Function.Name == name {
			continue
		}
		out = append(out, t)
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

// APIError is the parsed envelope of a non-2xx Responses API response.
type APIError struct {
	Provider   string
	Model      string
	HTTPStatus int
	Code       string
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
	return fmt.Sprintf("%s HTTP %d (%s): %s", e.Provider, e.HTTPStatus, e.Code, e.Message)
}

func parseAPIError(providerName, model string, status int, body []byte) error {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	apiErr := &APIError{Provider: providerName, Model: model, HTTPStatus: status, RawBody: string(body)}
	if err := json.Unmarshal(body, &env); err == nil {
		apiErr.Code = env.Error.Code
		apiErr.Message = env.Error.Message
	}
	if apiErr.Message == "" {
		apiErr.Message = string(body)
	}
	if apiErr.Code == "context_length_exceeded" || isContextOverflowText(apiErr.Message) {
		return &provider.ContextOverflowError{
			Provider: providerName, Model: model,
			Message: apiErr.Message, Cause: apiErr,
		}
	}
	return apiErr
}

func isContextOverflowText(msg string) bool {
	m := strings.ToLower(msg)
	for _, needle := range []string{"context length", "context window", "too many tokens", "maximum context"} {
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
