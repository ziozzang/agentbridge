// Package zaimcp is an HTTP+SSE client for Z.AI Coding Plan MCP endpoints
// (web_search_prime and web_reader). It performs the MCP initialize handshake,
// caches the session id, and exposes a thin tools/call wrapper.
package zaimcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
)

// Endpoints exposed by Z.AI Coding Plan.
const (
	WebSearchEndpoint = "https://api.z.ai/api/mcp/web_search_prime/mcp"
	WebReaderEndpoint = "https://api.z.ai/api/mcp/web_reader/mcp"
)

const protocolVersion = "2025-06-18"

// Client is an MCP-over-HTTP+SSE client.
type Client struct {
	HTTP    *http.Client
	mu      sync.Mutex
	cache   map[string]*sessionState
	nextID  atomic.Int64
}

type sessionState struct {
	SessionID string
	Tools     []toolSchema
}

type toolSchema struct {
	Name       string
	Properties []string
}

// New constructs a client using http.DefaultClient.
func New() *Client {
	return &Client{HTTP: http.DefaultClient, cache: map[string]*sessionState{}}
}

// CallToolInput is the parameter object for CallTool.
type CallToolInput struct {
	Endpoint  string
	APIKey    string
	ToolName  string
	Arguments map[string]any
}

// CallTool runs a single tool/call against the MCP endpoint, performing the
// initialize handshake the first time. Errors propagate up.
func (c *Client) CallTool(ctx context.Context, in CallToolInput) (json.RawMessage, error) {
	res, err := c.callOnce(ctx, in)
	if err == nil {
		return res, nil
	}
	if !isRetryable(err) {
		return nil, err
	}
	c.invalidate(cacheKey(in))
	return c.callOnce(ctx, in)
}

func (c *Client) callOnce(ctx context.Context, in CallToolInput) (json.RawMessage, error) {
	sess, err := c.ensureSession(ctx, in)
	if err != nil {
		return nil, err
	}
	resolvedName := in.ToolName
	if len(sess.Tools) > 0 {
		resolvedName = resolveToolName(in.ToolName, sess.Tools)
	}
	body := jsonObject{
		"jsonrpc": "2.0",
		"id":      c.nextID.Add(1),
		"method":  "tools/call",
		"params": jsonObject{
			"name":      resolvedName,
			"arguments": in.Arguments,
		},
	}
	resp, _, err := c.fetchJSONRPC(ctx, in.Endpoint, in.APIKey, "tools/call", body, sess.SessionID, resolvedName)
	if err != nil {
		return nil, err
	}
	return resp.Result, nil
}

func (c *Client) ensureSession(ctx context.Context, in CallToolInput) (*sessionState, error) {
	key := cacheKey(in)
	c.mu.Lock()
	if s, ok := c.cache[key]; ok {
		c.mu.Unlock()
		return s, nil
	}
	c.mu.Unlock()

	initBody := jsonObject{
		"jsonrpc": "2.0",
		"id":      c.nextID.Add(1),
		"method":  "initialize",
		"params": jsonObject{
			"protocolVersion": protocolVersion,
			"capabilities":    jsonObject{},
			"clientInfo":      jsonObject{"name": "glm-acp-agent", "version": "1.0.0"},
		},
	}
	_, sessionID, err := c.fetchJSONRPC(ctx, in.Endpoint, in.APIKey, "initialize", initBody, "", "")
	if err != nil {
		return nil, err
	}
	if err := c.notify(ctx, in.Endpoint, in.APIKey, sessionID); err != nil {
		return nil, err
	}
	tools, err := c.discoverTools(ctx, in.Endpoint, in.APIKey, sessionID)
	if err != nil {
		return nil, err
	}
	s := &sessionState{SessionID: sessionID, Tools: tools}
	c.mu.Lock()
	c.cache[key] = s
	c.mu.Unlock()
	return s, nil
}

func (c *Client) discoverTools(ctx context.Context, endpoint, apiKey, sessionID string) ([]toolSchema, error) {
	req := jsonObject{
		"jsonrpc": "2.0",
		"id":      c.nextID.Add(1),
		"method":  "tools/list",
	}
	resp, _, err := c.fetchJSONRPC(ctx, endpoint, apiKey, "tools/list", req, sessionID, "")
	if err != nil {
		return nil, err
	}
	var listResult struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]any `json:"properties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &listResult); err != nil {
		return nil, fmt.Errorf("decode tools/list: %w", err)
	}
	out := make([]toolSchema, 0, len(listResult.Tools))
	for _, t := range listResult.Tools {
		props := make([]string, 0, len(t.InputSchema.Properties))
		for k := range t.InputSchema.Properties {
			props = append(props, k)
		}
		out = append(out, toolSchema{Name: t.Name, Properties: props})
	}
	return out, nil
}

func (c *Client) notify(ctx context.Context, endpoint, apiKey, sessionID string) error {
	body := jsonObject{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	c.applyHeaders(req, apiKey, "notifications/initialized", sessionID, "")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("notifications/initialized: HTTP %d", resp.StatusCode)
	}
	return nil
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message)
}

func (c *Client) fetchJSONRPC(ctx context.Context, endpoint, apiKey, method string, body any, sessionID, mcpName string) (*rpcResponse, string, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, "", err
	}
	c.applyHeaders(req, apiKey, method, sessionID, mcpName)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	text, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("MCP %s failed: HTTP %d: %s", method, resp.StatusCode, string(text))
	}
	parsed, err := parseMcpResponse(text, resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, "", err
	}
	if parsed.Error != nil {
		return nil, "", fmt.Errorf("MCP %s failed: %s", method, parsed.Error.Error())
	}
	return parsed, resp.Header.Get("MCP-Session-Id"), nil
}

func (c *Client) applyHeaders(req *http.Request, apiKey, method, sessionID, mcpName string) {
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	req.Header.Set("Mcp-Method", method)
	if sessionID != "" {
		req.Header.Set("MCP-Session-Id", sessionID)
	}
	if mcpName != "" {
		req.Header.Set("Mcp-Name", mcpName)
	}
}

func (c *Client) invalidate(key string) {
	c.mu.Lock()
	delete(c.cache, key)
	c.mu.Unlock()
}

func cacheKey(in CallToolInput) string { return in.Endpoint + "\n" + in.APIKey }

func parseMcpResponse(text []byte, contentType string) (*rpcResponse, error) {
	if len(bytes.TrimSpace(text)) == 0 {
		return nil, errors.New("MCP response was empty")
	}
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return parseSseJSONRPC(text)
	}
	var resp rpcResponse
	if err := json.Unmarshal(text, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func parseSseJSONRPC(text []byte) (*rpcResponse, error) {
	scanner := bufio.NewScanner(bytes.NewReader(text))
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
		var resp rpcResponse
		if err := json.Unmarshal([]byte(data), &resp); err != nil {
			continue
		}
		if resp.Result != nil || resp.Error != nil {
			return &resp, nil
		}
	}
	return nil, errors.New("MCP SSE response did not contain a JSON-RPC result")
}

var rpcCodePattern = regexp.MustCompile(`-326\d\d`)

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if rpcCodePattern.MatchString(msg) {
		return true
	}
	return strings.Contains(msg, "tool not found") || strings.Contains(msg, "unknown tool") || strings.Contains(msg, "not found tool")
}

// resolveToolName returns the schema name closest to want. We accept exact
// matches, snake_case→camelCase fixes, and alias overrides similar to the
// TypeScript helper.
func resolveToolName(want string, schemas []toolSchema) string {
	for _, s := range schemas {
		if s.Name == want {
			return s.Name
		}
	}
	camel := snakeToCamel(want)
	for _, s := range schemas {
		if s.Name == camel {
			return s.Name
		}
	}
	if len(schemas) == 1 {
		return schemas[0].Name
	}
	return want
}

func snakeToCamel(s string) string {
	parts := strings.Split(s, "_")
	if len(parts) == 1 {
		return s
	}
	out := parts[0]
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		out += strings.ToUpper(p[:1]) + p[1:]
	}
	return out
}

type jsonObject = map[string]any
