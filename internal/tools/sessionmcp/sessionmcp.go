// Package sessionmcp implements an HTTP MCP client for session-scoped MCP servers.
// It connects to client-provided MCP servers (via session/new or session/load),
// performs the MCP initialize handshake, discovers their tools, and exposes
// them with namespaced names (mcp__<serverName>__<toolName>) to avoid collisions.
package sessionmcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ziozzang/agentbridge/internal/acp"
	"github.com/ziozzang/agentbridge/internal/logger"
	"github.com/ziozzang/agentbridge/internal/tools/definitions"
)

const protocolVersion = "2025-06-18"

// Client is a manager for session-scoped MCP servers.
type Client struct {
	HTTP *http.Client
	mu   sync.Mutex
	// bindings maps exposed tool names (mcp__<serverName>__<toolName>) to serverState + sourceName.
	bindings map[string]*toolBinding
	servers  []*serverState
	nextID   atomic.Int64
}

type toolBinding struct {
	exposedName string
	sourceName  string
	server      *serverState
	definition  definitions.Tool
}

type serverState struct {
	name      string
	typ       string
	url       string
	command   string
	args      []string
	env       map[string]string
	cwd       string
	headers   map[string]string
	allow     []string
	deny      []string
	inject    []acp.McpInjectedTool
	sessionID string
	http      *http.Client
	nextID    atomic.Int64
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    *bufio.Reader
	stdioMu   sync.Mutex
}

// New constructs a Client from the given MCP server configs.
// HTTP and stdio servers are processed; SSE servers are logged and skipped.
func New(specs []acp.McpServer) (*Client, error) {
	return NewWithHTTP(specs, http.DefaultClient)
}

// NewWithHTTP constructs a Client with a custom HTTP client (for tests).
func NewWithHTTP(specs []acp.McpServer, httpClient *http.Client) (*Client, error) {
	c := &Client{
		HTTP:     httpClient,
		bindings: map[string]*toolBinding{},
	}
	ctx := context.Background()
	for _, spec := range specs {
		typ := strings.ToLower(spec.Type)
		if typ == "" {
			typ = "http"
		}
		if typ != "http" && typ != "stdio" {
			logger.Debugf("sessionmcp: skipping unsupported server %q (type=%s)", spec.Name, spec.Type)
			continue
		}
		srv := &serverState{
			typ:     typ,
			name:    spec.Name,
			url:     spec.URL,
			command: spec.Command,
			args:    spec.Args,
			env:     spec.Env,
			cwd:     spec.Cwd,
			headers: spec.Headers,
			allow:   spec.AllowTools,
			deny:    spec.DenyTools,
			inject:  spec.InjectTools,
			http:    httpClient,
		}
		if typ == "stdio" {
			if err := srv.startStdio(); err != nil {
				return nil, fmt.Errorf("sessionmcp: start stdio %s: %w", spec.Name, err)
			}
		}
		if err := srv.initialize(ctx); err != nil {
			srv.Dispose()
			return nil, fmt.Errorf("sessionmcp: initialize %s: %w", spec.Name, err)
		}
		tools, err := srv.listTools(ctx)
		if err != nil {
			srv.Dispose()
			return nil, fmt.Errorf("sessionmcp: list tools %s: %w", spec.Name, err)
		}
		c.servers = append(c.servers, srv)
		c.registerTools(srv, tools)
		c.registerInjectedTools(srv)
	}
	return c, nil
}

func (c *Client) registerTools(srv *serverState, upstreamTools []mcpTool) {
	usedNames := make(map[string]struct{})
	for _, t := range c.bindings {
		usedNames[t.exposedName] = struct{}{}
	}
	for _, tool := range upstreamTools {
		if !toolAllowed(tool.Name, srv.allow, srv.deny) {
			logger.Debugf("sessionmcp: filtered tool %q from server %q", tool.Name, srv.name)
			continue
		}
		exposedName := chooseToolName(tool.Name, srv.name, usedNames)
		usedNames[exposedName] = struct{}{}
		desc := tool.Description
		if desc == "" {
			desc = fmt.Sprintf("Call %s on the %s MCP server.", tool.Name, srv.name)
		}
		params := normalizeSchema(tool.InputSchema)
		c.bindings[exposedName] = &toolBinding{
			exposedName: exposedName,
			sourceName:  tool.Name,
			server:      srv,
			definition: definitions.Tool{
				Type: "function",
				Function: definitions.ToolFunction{
					Name:        exposedName,
					Description: desc,
					Parameters:  params,
				},
			},
		}
	}
}

func (c *Client) registerInjectedTools(srv *serverState) {
	usedNames := make(map[string]struct{})
	for _, t := range c.bindings {
		usedNames[t.exposedName] = struct{}{}
	}
	for _, tool := range srv.inject {
		sourceName := tool.SourceName
		if sourceName == "" {
			sourceName = tool.Name
		}
		if !toolAllowed(sourceName, srv.allow, srv.deny) {
			logger.Debugf("sessionmcp: filtered injected tool %q from server %q", sourceName, srv.name)
			continue
		}
		exposedName := chooseToolName(tool.Name, srv.name, usedNames)
		usedNames[exposedName] = struct{}{}
		desc := tool.Description
		if desc == "" {
			desc = fmt.Sprintf("Call %s on the %s MCP server.", sourceName, srv.name)
		}
		params := tool.InputSchema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		c.bindings[exposedName] = &toolBinding{
			exposedName: exposedName,
			sourceName:  sourceName,
			server:      srv,
			definition: definitions.Tool{
				Type: "function",
				Function: definitions.ToolFunction{
					Name:        exposedName,
					Description: desc,
					Parameters:  params,
				},
			},
		}
	}
}

func toolAllowed(name string, allow, deny []string) bool {
	if len(allow) > 0 && !matchesAny(name, allow) {
		return false
	}
	if len(deny) > 0 && matchesAny(name, deny) {
		return false
	}
	return true
}

func matchesAny(name string, patterns []string) bool {
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if ok, err := filepathMatch(pattern, name); err == nil && ok {
			return true
		}
		if pattern == name {
			return true
		}
	}
	return false
}

func filepathMatch(pattern, name string) (bool, error) {
	if !strings.ContainsAny(pattern, "*?[") {
		return pattern == name, nil
	}
	return path.Match(pattern, name)
}

// ToolDefinitions returns the list of all tools exposed by the connected MCP servers.
func (c *Client) ToolDefinitions() []definitions.Tool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]definitions.Tool, 0, len(c.bindings))
	for _, b := range c.bindings {
		out = append(out, b.definition)
	}
	return out
}

// CallTool routes a namespaced tool call to the appropriate MCP server.
func (c *Client) CallTool(ctx context.Context, fullName string, args map[string]any) (string, error) {
	c.mu.Lock()
	binding, ok := c.bindings[fullName]
	c.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("unknown MCP tool: %s", fullName)
	}
	result, err := binding.server.callTool(ctx, binding.sourceName, args)
	if err != nil {
		return "", err
	}
	return unwrapMcpPayload(result), nil
}

// Dispose closes idle connections (no-op with http.DefaultClient).
func (c *Client) Dispose() {
	for _, srv := range c.servers {
		srv.Dispose()
	}
}

func (s *serverState) Dispose() {
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
	}
}

// ---------------------------------------------------------------------------
// HTTP MCP protocol
// ---------------------------------------------------------------------------

type mcpTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"inputSchema,omitempty"`
}

func (s *serverState) initialize(ctx context.Context) error {
	req := jsonObject{
		"jsonrpc": "2.0",
		"id":      s.nextID.Add(1),
		"method":  "initialize",
		"params": jsonObject{
			"protocolVersion": protocolVersion,
			"capabilities":    jsonObject{},
			"clientInfo":      jsonObject{"name": "agentbridge", "version": "1.0.0"},
		},
	}
	_, sessionID, err := s.fetchJSONRPC(ctx, "initialize", req, "")
	if err != nil {
		return err
	}
	s.sessionID = sessionID
	// Send notifications/initialized
	notifyReq := jsonObject{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}
	return s.sendNotification(ctx, notifyReq)
}

func (s *serverState) listTools(ctx context.Context) ([]mcpTool, error) {
	req := jsonObject{
		"jsonrpc": "2.0",
		"id":      s.nextID.Add(1),
		"method":  "tools/list",
	}
	resp, _, err := s.fetchJSONRPC(ctx, "tools/list", req, "")
	if err != nil {
		return nil, err
	}
	var listResult struct {
		Tools []mcpTool `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &listResult); err != nil {
		return nil, fmt.Errorf("decode tools/list: %w", err)
	}
	return listResult.Tools, nil
}

func (s *serverState) callTool(ctx context.Context, toolName string, args map[string]any) (json.RawMessage, error) {
	req := jsonObject{
		"jsonrpc": "2.0",
		"id":      s.nextID.Add(1),
		"method":  "tools/call",
		"params": jsonObject{
			"name":      toolName,
			"arguments": args,
		},
	}
	resp, _, err := s.fetchJSONRPC(ctx, "tools/call", req, toolName)
	if err != nil {
		return nil, err
	}
	return resp.Result, nil
}

func (s *serverState) sendNotification(ctx context.Context, body jsonObject) error {
	if s.typ == "stdio" {
		return s.writeStdio(ctx, body)
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	s.applyHeaders(req, "notifications/initialized", "")
	resp, err := s.http.Do(req)
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

func (s *serverState) fetchJSONRPC(ctx context.Context, method string, body jsonObject, mcpName string) (*rpcResponse, string, error) {
	if s.typ == "stdio" {
		resp, err := s.fetchStdio(ctx, body)
		return resp, "", err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(raw))
	if err != nil {
		return nil, "", err
	}
	s.applyHeaders(req, method, mcpName)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	text, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("MCP %s %s failed: HTTP %d: %s", s.name, method, resp.StatusCode, string(text))
	}
	parsed, err := parseMcpResponse(text, resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, "", err
	}
	if parsed.Error != nil {
		return nil, "", fmt.Errorf("MCP %s %s failed: %s", s.name, method, parsed.Error.Error())
	}
	return parsed, resp.Header.Get("MCP-Session-Id"), nil
}

func (s *serverState) startStdio() error {
	if s.command == "" {
		return errors.New("stdio MCP command is required")
	}
	cmd := exec.Command(s.command, s.args...)
	if s.cwd != "" {
		cmd.Dir = s.cwd
	}
	if len(s.env) > 0 {
		env := os.Environ()
		for k, v := range s.env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go io.Copy(io.Discard, stderr)
	s.cmd = cmd
	s.stdin = stdin
	s.stdout = bufio.NewReader(stdout)
	return nil
}

func (s *serverState) fetchStdio(ctx context.Context, body jsonObject) (*rpcResponse, error) {
	s.stdioMu.Lock()
	defer s.stdioMu.Unlock()
	if err := s.writeStdioLocked(ctx, body); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, err := s.stdout.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}
		if resp.ID == nil && resp.Result == nil && resp.Error == nil {
			continue
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return &resp, nil
	}
}

func (s *serverState) writeStdio(ctx context.Context, body jsonObject) error {
	s.stdioMu.Lock()
	defer s.stdioMu.Unlock()
	return s.writeStdioLocked(ctx, body)
}

func (s *serverState) writeStdioLocked(ctx context.Context, body jsonObject) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	_, err = s.stdin.Write(raw)
	return err
}

func (s *serverState) applyHeaders(req *http.Request, method, mcpName string) {
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	req.Header.Set("Mcp-Method", method)
	if s.sessionID != "" {
		req.Header.Set("MCP-Session-Id", s.sessionID)
	}
	if mcpName != "" {
		req.Header.Set("Mcp-Name", mcpName)
	}
}

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
	lines := bytes.Split(text, []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			continue
		}
		if resp.Result != nil || resp.Error != nil {
			return &resp, nil
		}
	}
	return nil, errors.New("MCP SSE response did not contain a JSON-RPC result")
}

// ---------------------------------------------------------------------------
// Tool name generation
// ---------------------------------------------------------------------------

func chooseToolName(sourceName, serverName string, usedNames map[string]struct{}) string {
	base := fmt.Sprintf("mcp__%s__%s", sanitizeToolName(serverName), sanitizeToolName(sourceName))
	base = truncate(base, 60)
	candidate := base
	suffix := 2
	for {
		if _, used := usedNames[candidate]; !used {
			return candidate
		}
		candidate = truncate(fmt.Sprintf("%s_%d", base, suffix), 64)
		suffix++
	}
}

func sanitizeToolName(name string) string {
	out := regexp.MustCompile(`[^a-zA-Z0-9_-]+`).ReplaceAllString(name, "_")
	out = regexp.MustCompile(`^_+|_+$`).ReplaceAllString(out, "")
	return truncate(out, 64)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func normalizeSchema(schema map[string]interface{}) json.RawMessage {
	if schema == nil {
		schema = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
	}
	raw, _ := json.Marshal(schema)
	return raw
}

// unwrapMcpPayload extracts text content from the MCP tools/call response.
// MCP wraps results in { content: [{ type: "text", text: "..." }] } shape.
func unwrapMcpPayload(raw json.RawMessage) string {
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &result); err == nil && len(result.Content) > 0 {
		var parts []string
		for _, c := range result.Content {
			if c.Type == "text" && c.Text != "" {
				parts = append(parts, c.Text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	// Fallback: return raw JSON.
	return string(raw)
}

type jsonObject = map[string]any
