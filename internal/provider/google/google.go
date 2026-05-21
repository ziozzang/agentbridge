// Package google implements the native Gemini Generative Language API.
//
// Endpoint: POST <BaseURL>/v1beta/models/<model>:streamGenerateContent?alt=sse
//
// It also supports Google's cachedContents prompt-cache resource for Gemini
// 2.5/3 models. When enabled, the system prompt is cached separately and the
// streaming request references the cachedContent id.
package google

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ziozzang/agentbridge/internal/logger"
	"github.com/ziozzang/agentbridge/internal/provider"
	"github.com/ziozzang/agentbridge/internal/tools/definitions"
)

const Kind = "google"

func init() {
	provider.Register(Kind, func(cfg provider.Config) (provider.Provider, error) {
		return New(cfg), nil
	})
}

type Client struct {
	cfg        provider.Config
	HTTPClient *http.Client
}

func New(cfg provider.Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://generativelanguage.googleapis.com"
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 8192
	}
	return &Client{cfg: cfg, HTTPClient: &http.Client{Timeout: 120 * time.Second}}
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
	return 1_000_000
}

type genRequest struct {
	Contents          []genContent `json:"contents"`
	SystemInstruction *genContent  `json:"systemInstruction,omitempty"`
	Tools             []genTool    `json:"tools,omitempty"`
	GenerationConfig  *genConfig   `json:"generationConfig,omitempty"`
	CachedContent     string       `json:"cachedContent,omitempty"`
}

type genConfig struct {
	MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
}

type genContent struct {
	Role  string    `json:"role,omitempty"`
	Parts []genPart `json:"parts"`
}

type genPart struct {
	Text             string               `json:"text,omitempty"`
	FunctionCall     *genFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *genFunctionResponse `json:"functionResponse,omitempty"`
}

type genFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type genFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type genTool struct {
	FunctionDeclarations []genFunctionDecl `json:"functionDeclarations,omitempty"`
}

type genFunctionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type genResponse struct {
	Candidates []struct {
		Content genContent `json:"content"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		TotalTokenCount         int `json:"totalTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
		ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

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
		system, contents := translateMessages(messages)
		cachedContent := c.extraString("cached_content")
		if cachedContent == "" {
			cachedContent = c.ensureCachedContent(ctx, model, opts.SessionID, system)
		}
		req := genRequest{
			Contents:         contents,
			Tools:            translateTools(toolList),
			GenerationConfig: &genConfig{MaxOutputTokens: c.cfg.MaxTokens},
			CachedContent:    cachedContent,
		}
		if cachedContent == "" && system != "" {
			req.SystemInstruction = &genContent{Parts: []genPart{{Text: system}}}
		}
		body, err := json.Marshal(req)
		if err != nil {
			errs <- err
			return
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.streamURL(model), bytes.NewReader(body))
		if err != nil {
			errs <- err
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		c.authorize(httpReq)
		c.applyHeaders(httpReq)
		client := c.HTTPClient
		if client == nil {
			client = http.DefaultClient
		}
		resp, err := client.Do(httpReq)
		if err != nil {
			errs <- err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			errs <- parseGoogleError(c.Name(), model, resp.StatusCode, b)
			return
		}
		pending := map[string]provider.ToolCall{}
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "event:") {
				continue
			}
			if strings.HasPrefix(line, "data:") {
				line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
			if line == "" || line == "[DONE]" {
				continue
			}
			var ev genResponse
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				logger.Warnf("%s.streamChat: bad Gemini chunk: %v data=%s", c.Name(), err, line)
				continue
			}
			if ev.Error != nil {
				errs <- fmt.Errorf("google HTTP stream error: %s: %s", ev.Error.Status, ev.Error.Message)
				return
			}
			for _, cand := range ev.Candidates {
				for _, part := range cand.Content.Parts {
					if part.Text != "" {
						chunks <- provider.Chunk{Text: part.Text}
					}
					if part.FunctionCall != nil && part.FunctionCall.Name != "" {
						args, _ := json.Marshal(part.FunctionCall.Args)
						if len(args) == 0 {
							args = []byte(`{}`)
						}
						id := "google_call_" + part.FunctionCall.Name
						pending[id] = provider.ToolCall{ID: id, Name: part.FunctionCall.Name, Arguments: string(args)}
					}
				}
			}
			if ev.UsageMetadata != nil {
				u := &provider.Usage{
					InputTokens:      ev.UsageMetadata.PromptTokenCount,
					OutputTokens:     ev.UsageMetadata.CandidatesTokenCount,
					TotalTokens:      ev.UsageMetadata.TotalTokenCount,
					CachedReadTokens: ev.UsageMetadata.CachedContentTokenCount,
					ThoughtTokens:    ev.UsageMetadata.ThoughtsTokenCount,
				}
				chunks <- provider.Chunk{Usage: u}
			}
		}
		if err := scanner.Err(); err != nil {
			errs <- err
			return
		}
		for _, tc := range pending {
			cp := tc
			chunks <- provider.Chunk{ToolCall: &cp}
		}
		stop := "end_turn"
		if len(pending) > 0 {
			stop = "tool_calls"
		}
		chunks <- provider.Chunk{Done: true, StopReason: stop}
	}()
	return chunks, errs
}

func (c *Client) streamURL(model string) string {
	if project := c.extraString("vertex_project_id"); project != "" {
		base := strings.TrimRight(c.cfg.BaseURL, "/")
		if base == "" {
			base = "https://" + firstNonEmpty(c.extraString("vertex_region"), "global") + "-aiplatform.googleapis.com"
		}
		if !strings.HasSuffix(base, "/v1") {
			base += "/v1"
		}
		region := firstNonEmpty(c.extraString("vertex_region"), "global")
		return base + "/projects/" + url.PathEscape(project) + "/locations/" + url.PathEscape(region) + "/publishers/google/models/" + url.PathEscape(model) + ":streamGenerateContent?alt=sse"
	}
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	if !strings.HasSuffix(base, "/v1beta") && !strings.HasSuffix(base, "/v1") {
		base += "/v1beta"
	}
	return base + "/models/" + url.PathEscape(model) + ":streamGenerateContent?alt=sse"
}

func (c *Client) cachedContentsURL(name string) string {
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	if !strings.HasSuffix(base, "/v1beta") && !strings.HasSuffix(base, "/v1") {
		base += "/v1beta"
	}
	if name != "" {
		return base + "/" + strings.TrimPrefix(name, "/")
	}
	return base + "/cachedContents"
}

func (c *Client) authorize(req *http.Request) {
	if c.cfg.APIKey == "" {
		return
	}
	header := c.cfg.AuthHeader
	if header == "" {
		header = "x-goog-api-key"
	}
	if header == "Authorization" {
		prefix := c.cfg.AuthPrefix
		if prefix == "" {
			prefix = "Bearer "
		}
		req.Header.Set(header, prefix+c.cfg.APIKey)
		return
	}
	req.Header.Set(header, c.cfg.APIKey)
}

type cacheEntry struct {
	Name      string
	ExpireAt  time.Time
	FailedTil time.Time
}

var googleCache sync.Map

func (c *Client) ensureCachedContent(ctx context.Context, model, sessionID, system string) string {
	retention := c.cacheRetention()
	if retention == "" || system == "" || c.cfg.APIKey == "" || !googleCacheEligible(model) {
		return ""
	}
	key := c.cacheKey(model, sessionID, system)
	now := time.Now()
	if raw, ok := googleCache.Load(key); ok {
		entry := raw.(cacheEntry)
		if !entry.FailedTil.IsZero() && entry.FailedTil.After(now) {
			return ""
		}
		if entry.Name != "" && (entry.ExpireAt.IsZero() || entry.ExpireAt.After(now)) {
			if entry.ExpireAt.Sub(now) < c.cacheRefreshWindow(retention) {
				c.refreshCachedContent(ctx, entry.Name, retention)
			}
			return entry.Name
		}
	}
	name, expireAt, ok := c.createCachedContent(ctx, model, system, retention)
	if !ok {
		googleCache.Store(key, cacheEntry{FailedTil: now.Add(10 * time.Minute)})
		return ""
	}
	googleCache.Store(key, cacheEntry{Name: name, ExpireAt: expireAt})
	return name
}

func (c *Client) cacheRetention() string {
	switch strings.ToLower(firstNonEmpty(c.extraString("cache_retention"), c.extraString("prompt_cache_ttl"))) {
	case "short", "5m", "on", "true", "1":
		return "short"
	case "long", "1h":
		return "long"
	default:
		return ""
	}
}

func (c *Client) cacheTTL(retention string) string {
	if retention == "long" {
		return "3600s"
	}
	return "300s"
}

func (c *Client) cacheRefreshWindow(retention string) time.Duration {
	if retention == "long" {
		return 5 * time.Minute
	}
	return 30 * time.Second
}

func (c *Client) cacheKey(model, sessionID, system string) string {
	sum := sha256.Sum256([]byte(system))
	return c.Name() + "|" + strings.TrimRight(c.cfg.BaseURL, "/") + "|" + model + "|" + sessionID + "|" + hex.EncodeToString(sum[:])
}

func googleCacheEligible(model string) bool {
	m := strings.ToLower(model)
	return strings.HasPrefix(m, "gemini-2.5") || strings.HasPrefix(m, "gemini-3")
}

func (c *Client) createCachedContent(ctx context.Context, model, system, retention string) (string, time.Time, bool) {
	body, _ := json.Marshal(map[string]any{
		"model": "models/" + strings.TrimPrefix(model, "models/"),
		"ttl":   c.cacheTTL(retention),
		"systemInstruction": map[string]any{
			"parts": []map[string]string{{"text": system}},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cachedContentsURL(""), bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, false
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)
	c.applyHeaders(req)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", time.Time{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", time.Time{}, false
	}
	var out struct {
		Name       string `json:"name"`
		ExpireTime string `json:"expireTime"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || out.Name == "" {
		return "", time.Time{}, false
	}
	expires, _ := time.Parse(time.RFC3339Nano, out.ExpireTime)
	return out.Name, expires, true
}

func (c *Client) refreshCachedContent(ctx context.Context, name, retention string) {
	body, _ := json.Marshal(map[string]any{"ttl": c.cacheTTL(retention)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.cachedContentsURL(name)+"?updateMask=ttl", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)
	c.applyHeaders(req)
	resp, err := c.httpClient().Do(req)
	if err == nil && resp != nil {
		resp.Body.Close()
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *Client) applyHeaders(req *http.Request) {
	for k, v := range c.cfg.Headers {
		req.Header.Set(k, v)
	}
}

func translateMessages(in []provider.Message) (string, []genContent) {
	var system []string
	var out []genContent
	toolNames := map[string]string{}
	for _, m := range in {
		switch m.Role {
		case "system":
			if s := contentToString(m.Content); s != "" {
				system = append(system, s)
			}
		case "assistant":
			parts := []genPart{}
			if s := contentToString(m.Content); s != "" {
				parts = append(parts, genPart{Text: s})
			}
			for _, tc := range m.ToolCalls {
				args := map[string]any{}
				_ = json.Unmarshal([]byte(firstNonEmpty(tc.Function.Arguments, "{}")), &args)
				parts = append(parts, genPart{FunctionCall: &genFunctionCall{Name: tc.Function.Name, Args: args}})
				if tc.ID != "" && tc.Function.Name != "" {
					toolNames[tc.ID] = tc.Function.Name
				}
			}
			if len(parts) > 0 {
				out = append(out, genContent{Role: "model", Parts: parts})
			}
		case "tool":
			resp := map[string]any{"result": contentToString(m.Content)}
			name := firstNonEmpty(m.Name, toolNames[m.ToolCallID], m.ToolCallID)
			out = append(out, genContent{Role: "user", Parts: []genPart{{FunctionResponse: &genFunctionResponse{Name: name, Response: resp}}}})
		default:
			out = append(out, genContent{Role: "user", Parts: []genPart{{Text: contentToString(m.Content)}}})
		}
	}
	return strings.Join(system, "\n\n"), out
}

func translateTools(in []definitions.Tool) []genTool {
	if len(in) == 0 {
		return nil
	}
	decls := make([]genFunctionDecl, 0, len(in))
	for _, t := range in {
		params := t.Function.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		decls = append(decls, genFunctionDecl{Name: t.Function.Name, Description: t.Function.Description, Parameters: params})
	}
	return []genTool{{FunctionDeclarations: decls}}
}

func contentToString(c any) string {
	switch v := c.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, e := range v {
			if m, ok := e.(map[string]any); ok {
				if t, _ := m["text"].(string); t != "" {
					b.WriteString(t)
				}
			}
		}
		return b.String()
	}
	b, _ := json.Marshal(c)
	return string(b)
}

func parseGoogleError(providerName, model string, status int, body []byte) error {
	var env struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	msg := string(body)
	if json.Unmarshal(body, &env) == nil && env.Error.Message != "" {
		msg = env.Error.Message
	}
	if isContextOverflowText(msg) {
		return &provider.ContextOverflowError{Provider: providerName, Model: model, Message: msg}
	}
	return fmt.Errorf("%s HTTP %d: %s", providerName, status, msg)
}

func isContextOverflowText(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "context") && (strings.Contains(m, "too long") || strings.Contains(m, "limit"))
}

func (c *Client) extraString(key string) string {
	if c.cfg.Extra == nil {
		return ""
	}
	v, _ := c.cfg.Extra[key].(string)
	return strings.TrimSpace(v)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
