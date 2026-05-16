// Package visionmcp implements a stdio MCP client for Z.AI Vision MCP server.
// It spawns `npx -y @z_ai/mcp-server` as a subprocess and speaks JSON-RPC over
// stdio (newline-delimited JSON). The client lazily initializes on first use,
// retries on transport failures, and translates business errors into actionable
// messages.
package visionmcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"github.com/ziozzang/glm-acp/internal/logger"
	"github.com/ziozzang/glm-acp/internal/tools/mcparg"
)

const protocolVersion = "2025-06-18"

// Client is a stdio MCP client for Vision MCP server.
type Client struct {
	apiKey  string
	command string
	args    []string

	mu              sync.Mutex
	child           *exec.Cmd
	stdin           io.WriteCloser
	stdout          io.ReadCloser
	initialized     bool
	nextID          int
	pending         map[int]*pendingRequest
	buffer          string
	exited          bool
	exitReason      string
	discoveredTools []discoveredTool
}

type pendingRequest struct {
	resolve chan<- json.RawMessage
	reject  chan<- error
	method  string
}

type discoveredTool struct {
	Name       string
	Properties []string
}

// New constructs a stdio Vision MCP client with the given API key.
// The subprocess is spawned lazily on first CallTool.
func New(apiKey string) *Client {
	cmd := os.Getenv("ACP_GLM_VISION_MCP_CMD")
	if cmd == "" {
		cmd = "npx"
	}
	args := []string{"-y", "@z_ai/mcp-server"}
	if customArgs := os.Getenv("ACP_GLM_VISION_MCP_ARGS"); customArgs != "" {
		args = strings.Fields(customArgs)
	}
	return &Client{
		apiKey:  apiKey,
		command: cmd,
		args:    args,
		pending: map[int]*pendingRequest{},
	}
}

// CallTool invokes a tool on the Vision MCP server. The subprocess is started
// lazily on first call. Retries once on transport failure.
func (c *Client) CallTool(ctx context.Context, toolName string, args map[string]any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("Vision MCP call cancelled: %w", err)
	}
	if err := c.ensureInitialized(ctx); err != nil {
		return "", err
	}
	result, err := c.callToolInternal(ctx, toolName, args)
	if err != nil && isRetryableError(err) {
		// Retry once: reset child and rediscover tools.
		c.mu.Lock()
		c.resetLocked()
		c.mu.Unlock()
		if err := c.ensureInitialized(ctx); err != nil {
			return "", err
		}
		result, err = c.callToolInternal(ctx, toolName, args)
	}
	if err != nil {
		return "", translateBusinessError(err)
	}
	return unwrapMcpPayload(result), nil
}

func (c *Client) callToolInternal(ctx context.Context, toolName string, args map[string]any) (json.RawMessage, error) {
	toolNames := make([]string, len(c.discoveredTools))
	for i, t := range c.discoveredTools {
		toolNames[i] = t.Name
	}
	resolvedName, err := mcparg.ResolveToolName(toolName, toolNames, "@z_ai/mcp-server")
	if err != nil {
		return nil, err
	}
	var remappedArgs map[string]any
	for _, t := range c.discoveredTools {
		if t.Name == resolvedName {
			remappedArgs = mcparg.RemapArguments(args, t.Properties)
			break
		}
	}
	if remappedArgs == nil {
		remappedArgs = args
	}
	return c.request(ctx, "tools/call", map[string]any{
		"name":      resolvedName,
		"arguments": remappedArgs,
	}, fmt.Sprintf("Vision MCP %s", toolName))
}

// Dispose terminates the subprocess.
func (c *Client) Dispose() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetLocked()
}

func (c *Client) resetLocked() {
	if c.child != nil && !c.exited {
		_ = c.child.Process.Kill()
	}
	c.child = nil
	c.stdin = nil
	c.stdout = nil
	c.initialized = false
	c.buffer = ""
	c.exited = false
	c.exitReason = ""
	c.discoveredTools = nil
	for id, p := range c.pending {
		p.reject <- errors.New("Vision MCP client reset")
		delete(c.pending, id)
	}
}

// ---------------------------------------------------------------------------
// Subprocess lifecycle
// ---------------------------------------------------------------------------

func (c *Client) ensureInitialized(ctx context.Context) error {
	c.mu.Lock()
	if c.initialized {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	return c.startAndInitialize(ctx)
}

func (c *Client) startAndInitialize(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.initialized {
		return nil
	}
	cmd := exec.CommandContext(ctx, c.command, c.args...)
	cmd.Env = append(os.Environ(), "Z_AI_API_KEY="+c.apiKey, "Z_AI_MODE=ZAI")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("Vision MCP startup failed (stdin pipe): %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return fmt.Errorf("Vision MCP startup failed (stdout pipe): %w", err)
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("Vision MCP startup failed: `%s` not found on PATH. Install Node.js / npm 9+ and ensure `npx` is available.", c.command)
		}
		return fmt.Errorf("Vision MCP startup failed: %w", err)
	}
	c.child = cmd
	c.stdin = stdin
	c.stdout = stdout
	c.exited = false
	c.exitReason = ""
	go c.readLoop()
	go c.waitForExit()

	// Send initialize request.
	_, err = c.requestLocked(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "glm-acp-agent", "version": "1.0.0"},
	}, "Vision MCP initialize")
	if err != nil {
		c.resetLocked()
		return err
	}
	// Send notifications/initialized.
	c.sendLocked(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	// Discover tools.
	if err := c.rediscoverToolsLocked(ctx); err != nil {
		c.resetLocked()
		return err
	}
	c.initialized = true
	return nil
}

func (c *Client) rediscoverToolsLocked(ctx context.Context) error {
	result, err := c.requestLocked(ctx, "tools/list", map[string]any{}, "Vision MCP tools/list")
	if err != nil {
		return err
	}
	var listResult struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]any `json:"properties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &listResult); err != nil {
		return fmt.Errorf("decode tools/list: %w", err)
	}
	c.discoveredTools = nil
	for _, t := range listResult.Tools {
		props := make([]string, 0, len(t.InputSchema.Properties))
		for k := range t.InputSchema.Properties {
			props = append(props, k)
		}
		c.discoveredTools = append(c.discoveredTools, discoveredTool{
			Name:       t.Name,
			Properties: props,
		})
	}
	return nil
}

func (c *Client) readLoop() {
	scanner := bufio.NewScanner(c.stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      *int            `json:"id"`
			Result  json.RawMessage `json:"result"`
			Error   *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			logger.Debugf("visionmcp: invalid JSON line: %s", line)
			continue
		}
		if msg.ID == nil {
			continue
		}
		c.mu.Lock()
		p, ok := c.pending[*msg.ID]
		if ok {
			delete(c.pending, *msg.ID)
		}
		c.mu.Unlock()
		if !ok {
			continue
		}
		if msg.Error != nil {
			p.reject <- fmt.Errorf("%s failed: %s (code %d)", p.method, msg.Error.Message, msg.Error.Code)
		} else {
			p.resolve <- msg.Result
		}
	}
}

func (c *Client) waitForExit() {
	err := c.child.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.exited = true
	if err != nil {
		c.exitReason = err.Error()
	} else {
		c.exitReason = "exit code=0"
	}
	for id, p := range c.pending {
		p.reject <- fmt.Errorf("server exited (%s)", c.exitReason)
		delete(c.pending, id)
	}
}

func (c *Client) request(ctx context.Context, method string, params map[string]any, label string) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requestLocked(ctx, method, params, label)
}

func (c *Client) requestLocked(ctx context.Context, method string, params map[string]any, label string) (json.RawMessage, error) {
	id := c.nextID
	c.nextID++
	resolve := make(chan json.RawMessage, 1)
	reject := make(chan error, 1)
	c.pending[id] = &pendingRequest{resolve: resolve, reject: reject, method: label}

	if err := c.sendLocked(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		delete(c.pending, id)
		return nil, fmt.Errorf("%s failed: %w", label, err)
	}

	// Unlock before waiting so readLoop/waitForExit can acquire the lock
	c.mu.Unlock()
	defer c.mu.Lock() // Re-lock before returning

	select {
	case result := <-resolve:
		return result, nil
	case err := <-reject:
		return nil, err
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("%s failed: %w", label, ctx.Err())
	}
}

func (c *Client) sendLocked(msg map[string]any) error {
	if c.exited {
		return fmt.Errorf("Vision MCP server is not running (%s)", c.exitReason)
	}
	body, _ := json.Marshal(msg)
	body = append(body, '\n')
	_, err := c.stdin.Write(body)
	return err
}

// ---------------------------------------------------------------------------
// Error handling
// ---------------------------------------------------------------------------

var retryableCodePattern = regexp.MustCompile(`-32601`)

func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if retryableCodePattern.MatchString(msg) {
		return true
	}
	return strings.Contains(msg, "tool not found") ||
		strings.Contains(msg, "unknown tool") ||
		strings.Contains(msg, "not found tool")
}

func translateBusinessError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	// Error 1113: invalid API key
	if strings.Contains(msg, "1113") || strings.Contains(msg, "invalid api key") {
		return fmt.Errorf("Invalid Z.AI API key for Vision MCP. Reset via `glm-acp-agent --setup`. Original error: %w", err)
	}
	return err
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
