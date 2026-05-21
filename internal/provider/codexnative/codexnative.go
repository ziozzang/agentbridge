// Package codexnative adapts `codex app-server` as a native provider.
//
// Unlike the HTTP-backed OpenAI providers, this adapter talks to the local
// Codex runtime over its JSON-RPC stdio transport. That lets AgentBridge use
// the locally authenticated Codex CLI and expose it through `/v1/chat/completions`
// and the internal agent loop without requiring an explicit API key in config.
package codexnative

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ziozzang/agentbridge/internal/provider"
)

// Kind is the registry key for the Codex app-server provider.
const Kind = "codex-app-server"

const (
	defaultModel         = "gpt-5"
	defaultContextWindow = 400_000
	maxScannerBuffer     = 2 << 20
	modelListTimeout     = 8 * time.Second
)

func init() {
	provider.Register(Kind, func(cfg provider.Config) (provider.Provider, error) {
		return New(cfg), nil
	})
}

// Client invokes the local Codex app-server runtime.
type Client struct {
	cfg provider.Config

	mu       sync.Mutex
	sessions map[string]sessionState

	modelsOnce sync.Once
	models     []provider.ModelInfo
}

type sessionState struct {
	ThreadID string
	Seen     []string
}

// New constructs a Codex app-server provider.
func New(cfg provider.Config) *Client {
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = defaultModel
	}
	if cfg.ContextWindow <= 0 {
		cfg.ContextWindow = defaultContextWindow
	}
	return &Client{
		cfg:      cfg,
		sessions: map[string]sessionState{},
	}
}

func (c *Client) Name() string              { return firstNonEmpty(c.cfg.Name, Kind) }
func (c *Client) Kind() string              { return Kind }
func (c *Client) UsesNativeAgentLoop() bool { return true }

func (c *Client) AvailableModels() []provider.ModelInfo {
	c.modelsOnce.Do(func() {
		if len(c.cfg.Models) > 0 {
			c.models = attachNativeAgentModelMetadata(cloneModels(c.cfg.Models))
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), modelListTimeout)
		defer cancel()
		models, err := c.fetchModels(ctx)
		if err == nil && len(models) > 0 {
			c.models = attachNativeAgentModelMetadata(models)
			return
		}
		if c.cfg.DefaultModel != "" {
			c.models = attachNativeAgentModelMetadata([]provider.ModelInfo{{
				ModelID:       c.cfg.DefaultModel,
				Name:          c.cfg.DefaultModel,
				Description:   "Codex app-server via AgentBridge",
				Provider:      c.cfg.Name,
				ContextWindow: c.cfg.ContextWindow,
			}})
		}
	})
	return cloneModels(c.models)
}

func (c *Client) DefaultModel() string {
	if c.cfg.DefaultModel != "" {
		return c.cfg.DefaultModel
	}
	return defaultModel
}

func (c *Client) ContextWindow(model string) int {
	_ = model
	return c.cfg.ContextWindow
}

func (c *Client) SanitizeStreamOptions(opts provider.StreamOptions) provider.StreamOptions {
	// codex app-server supports model/service tier/reasoning effort, but not
	// OpenAI-style prompt cache markers or retention hints on this bridge path.
	opts.PromptCacheRetention = ""
	return opts
}

func (c *Client) SanitizeCompactOptions(opts provider.CompactOptions) provider.CompactOptions {
	opts.PromptCacheRetention = ""
	return opts
}

func (c *Client) StreamChat(ctx context.Context, messages []provider.Message, opts provider.StreamOptions) (<-chan provider.Chunk, <-chan error) {
	chunks := make(chan provider.Chunk, 32)
	errs := make(chan error, 1)
	go func() {
		defer close(chunks)
		defer close(errs)

		rpc, err := c.startRPC(ctx)
		if err != nil {
			errs <- err
			return
		}
		defer rpc.Close()

		sessionKey := sessionKeyFor(opts)
		threadID, prompt, seen, err := c.prepareTurn(ctx, rpc, sessionKey, messages, opts)
		if err != nil {
			errs <- err
			return
		}

		started, err := rpc.Request("turn/start", c.turnStartParams(threadID, prompt, opts))
		if err != nil {
			errs <- normalizeCodexError(err, opts.Model)
			return
		}
		turnID := nestedString(started, "turn", "id")
		if turnID == "" {
			errs <- errors.New("codex app-server: turn/start did not return a turn id")
			return
		}

		var usage provider.Usage
		stopReason := "stop"
		for {
			msg, err := rpc.Next()
			if err != nil {
				errs <- err
				return
			}
			method, params, kind, ok := splitRPCMessage(msg)
			if !ok {
				continue
			}
			if kind == rpcKindResponse {
				continue
			}
			if kind == rpcKindRequest {
				if err := rpc.RespondToRequest(msg, defaultRequestResponse(method)); err != nil {
					errs <- err
					return
				}
				continue
			}
			switch method {
			case "item/agentMessage/delta":
				if stringAt(params, "turnId") != turnID {
					continue
				}
				if delta := stringAt(params, "delta"); delta != "" {
					chunks <- provider.Chunk{Text: delta}
				}
			case "thread/tokenUsage/updated":
				if stringAt(params, "threadId") != threadID || stringAt(params, "turnId") != turnID {
					continue
				}
				usage = usageFromTokenUpdate(params)
				chunks <- provider.Chunk{Usage: &usage}
			case "turn/completed":
				if nestedString(params, "turn", "id") != turnID {
					continue
				}
				status := nestedString(params, "turn", "status")
				if status == "failed" {
					errs <- normalizeCodexTurnFailure(params, opts.Model)
					return
				}
				c.recordSession(sessionKey, threadID, seen)
				chunks <- provider.Chunk{Done: true, StopReason: stopReason, Usage: usagePtr(usage)}
				errs <- nil
				return
			}
		}
	}()
	return chunks, errs
}

func (c *Client) CompactConversation(ctx context.Context, messages []provider.Message, opts provider.CompactOptions) ([]provider.Message, error) {
	key := sessionKeyFor(provider.StreamOptions{
		SessionID:      opts.SessionID,
		PromptCacheKey: opts.PromptCacheKey,
	})
	if key == "" {
		return nil, provider.ErrNativeCompactionUnavailable
	}
	threadID := c.lookupThread(key)
	if threadID == "" {
		return nil, provider.ErrNativeCompactionUnavailable
	}
	rpc, err := c.startRPC(ctx)
	if err != nil {
		return nil, err
	}
	defer rpc.Close()
	if _, err := rpc.Request("thread/resume", map[string]any{"threadId": threadID}); err != nil {
		return nil, err
	}
	if _, err := rpc.Request("thread/compact/start", map[string]any{"threadId": threadID}); err != nil {
		return nil, err
	}
	out := makeLocalCheckpoint(messages, opts.Reason)
	if len(out) == 0 {
		return nil, provider.ErrNativeCompactionUnavailable
	}
	return out, nil
}

func (c *Client) fetchModels(ctx context.Context) ([]provider.ModelInfo, error) {
	rpc, err := c.startRPC(ctx)
	if err != nil {
		return nil, err
	}
	defer rpc.Close()
	resp, err := rpc.Request("model/list", map[string]any{"includeHidden": false})
	if err != nil {
		return nil, err
	}
	data, _ := resp["data"].([]any)
	if len(data) == 0 {
		return nil, errors.New("codex app-server: model/list returned no data")
	}
	out := make([]provider.ModelInfo, 0, len(data))
	for _, raw := range data {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id := stringAt(m, "id")
		if id == "" {
			continue
		}
		out = append(out, provider.ModelInfo{
			ModelID:       id,
			Name:          firstNonEmpty(stringAt(m, "name"), id),
			Description:   "Codex app-server via AgentBridge",
			Provider:      firstNonEmpty(c.cfg.Name, "codex"),
			ContextWindow: c.cfg.ContextWindow,
		})
	}
	if len(out) == 0 {
		return nil, errors.New("codex app-server: model/list returned no parseable models")
	}
	return out, nil
}

func (c *Client) prepareTurn(ctx context.Context, rpc *rpcClient, sessionKey string, messages []provider.Message, opts provider.StreamOptions) (string, string, []string, error) {
	model := firstNonEmpty(opts.Model, c.DefaultModel())
	seenNow := messageFingerprints(messages)
	if sessionKey == "" {
		threadID, err := c.startThread(ctx, rpc, model, opts)
		if err != nil {
			return "", "", nil, err
		}
		return threadID, formatTranscript(messages), seenNow, nil
	}

	prev := c.lookupSession(sessionKey)
	reset := prev.ThreadID == ""
	prompt := formatTranscript(messages)
	if prev.ThreadID != "" && len(prev.Seen) > 0 {
		if delta, ok := incrementalTranscript(prev.Seen, seenNow, messages); ok && delta != "" {
			if _, err := rpc.Request("thread/resume", map[string]any{"threadId": prev.ThreadID}); err == nil {
				return prev.ThreadID, delta, seenNow, nil
			}
		}
		reset = true
	}
	threadID := prev.ThreadID
	if reset {
		var err error
		threadID, err = c.startThread(ctx, rpc, model, opts)
		if err != nil {
			return "", "", nil, err
		}
	}
	return threadID, prompt, seenNow, nil
}

func (c *Client) startThread(ctx context.Context, rpc *rpcClient, model string, opts provider.StreamOptions) (string, error) {
	resp, err := rpc.Request("thread/start", c.threadStartParams(model, opts))
	if err != nil {
		return "", err
	}
	threadID := nestedString(resp, "thread", "id")
	if threadID == "" {
		threadID = stringAt(resp, "threadId")
	}
	if threadID == "" {
		return "", errors.New("codex app-server: thread/start did not return a thread id")
	}
	return threadID, nil
}

func (c *Client) threadStartParams(model string, opts provider.StreamOptions) map[string]any {
	params := map[string]any{
		"model":          model,
		"approvalPolicy": firstNonEmpty(c.extraString("approval_policy"), "never"),
	}
	if cwd := strings.TrimSpace(c.extraString("cwd")); cwd != "" {
		params["cwd"] = cwd
	}
	if sandbox := firstNonEmpty(c.extraString("sandbox"), c.extraString("sandbox_policy")); sandbox != "" {
		params["sandbox"] = sandbox
	}
	if effort := firstNonEmpty(opts.ReasoningEffort, c.extraString("reasoning_effort")); effort != "" {
		params["config"] = map[string]any{"reasoning_effort": effort}
	}
	if tier := firstNonEmpty(opts.ServiceTier, c.extraString("service_tier")); tier != "" {
		params["serviceTier"] = tier
	}
	if base := c.extraString("base_instructions"); base != "" {
		params["baseInstructions"] = base
	}
	if dev := c.extraString("developer_instructions"); dev != "" {
		params["developerInstructions"] = dev
	}
	return params
}

func (c *Client) turnStartParams(threadID, prompt string, opts provider.StreamOptions) map[string]any {
	params := map[string]any{
		"threadId": threadID,
		"input": []map[string]any{{
			"type": "text",
			"text": prompt,
		}},
	}
	if model := firstNonEmpty(opts.Model, c.DefaultModel()); model != "" {
		params["model"] = model
	}
	if effort := firstNonEmpty(opts.ReasoningEffort, c.extraString("reasoning_effort")); effort != "" {
		params["effort"] = effort
	}
	if tier := firstNonEmpty(opts.ServiceTier, c.extraString("service_tier")); tier != "" {
		params["serviceTier"] = tier
	}
	return params
}

func (c *Client) startRPC(ctx context.Context) (*rpcClient, error) {
	command, args := c.commandAndArgs()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = os.Environ()
	for k, v := range c.cfg.Headers {
		_ = k
		_ = v
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	rpc := &rpcClient{
		cmd:    cmd,
		stdin:  stdin,
		stdout: newScanner(stdout),
		stderr: &stderr,
		nextID: 1,
	}
	if _, err := rpc.Request("initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "agentbridge",
			"version": "1.0.0",
		},
		"capabilities": map[string]any{
			"experimentalApi": true,
		},
	}); err != nil {
		rpc.Close()
		return nil, err
	}
	if err := rpc.Notify("initialized", map[string]any{}); err != nil {
		rpc.Close()
		return nil, err
	}
	return rpc, nil
}

func (c *Client) commandAndArgs() (string, []string) {
	if argv := c.extraStringSlice("argv"); len(argv) > 0 {
		return argv[0], append([]string(nil), argv[1:]...)
	}
	command := firstNonEmpty(c.extraString("command"), "codex")
	args := c.extraStringSlice("command_args")
	if len(args) == 0 {
		args = []string{"app-server", "--listen", "stdio://"}
	}
	return command, args
}

func (c *Client) recordSession(key, threadID string, seen []string) {
	if key == "" || threadID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions[key] = sessionState{ThreadID: threadID, Seen: append([]string(nil), seen...)}
}

func (c *Client) lookupSession(key string) sessionState {
	if key == "" {
		return sessionState{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions[key]
}

func (c *Client) lookupThread(key string) string {
	return c.lookupSession(key).ThreadID
}

func (c *Client) extraString(key string) string {
	if c.cfg.Extra == nil {
		return ""
	}
	if v, ok := c.cfg.Extra[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func (c *Client) extraStringSlice(key string) []string {
	if c.cfg.Extra == nil {
		return nil
	}
	switch raw := c.cfg.Extra[key].(type) {
	case []string:
		return append([]string(nil), raw...)
	case []any:
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	default:
		return nil
	}
}

type rpcKind int

const (
	rpcKindUnknown rpcKind = iota
	rpcKindResponse
	rpcKindRequest
	rpcKindNotification
)

type rpcClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
	stderr *bytes.Buffer
	nextID int
	queue  []map[string]any
}

func (c *rpcClient) Close() error {
	if c == nil || c.cmd == nil {
		return nil
	}
	_ = c.stdin.Close()
	err := c.cmd.Wait()
	if err == nil {
		return nil
	}
	return fmt.Errorf("codex app-server exited: %w: %s", err, strings.TrimSpace(c.stderr.String()))
}

func (c *rpcClient) Notify(method string, params any) error {
	return c.write(map[string]any{
		"method": method,
		"params": params,
	})
}

func (c *rpcClient) Request(method string, params any) (map[string]any, error) {
	id := c.nextID
	c.nextID++
	if err := c.write(map[string]any{
		"id":     id,
		"method": method,
		"params": params,
	}); err != nil {
		return nil, err
	}
	for {
		msg, err := c.Next()
		if err != nil {
			return nil, err
		}
		_, payload, kind, ok := splitRPCMessage(msg)
		if !ok {
			continue
		}
		switch kind {
		case rpcKindResponse:
			if intAt(msg, "id") != id {
				continue
			}
			if errObj, ok := msg["error"].(map[string]any); ok {
				return nil, fmt.Errorf("%s", firstNonEmpty(stringAt(errObj, "message"), "unknown app-server error"))
			}
			if result, ok := msg["result"].(map[string]any); ok {
				return result, nil
			}
			if msg["result"] == nil {
				return map[string]any{}, nil
			}
			return map[string]any{"value": msg["result"]}, nil
		case rpcKindRequest:
			if err := c.RespondToRequest(msg, defaultRequestResponse(stringAt(msg, "method"))); err != nil {
				return nil, err
			}
		case rpcKindNotification:
			c.queue = append(c.queue, msg)
		default:
			_ = payload
		}
	}
}

func (c *rpcClient) RespondToRequest(req map[string]any, result any) error {
	return c.write(map[string]any{
		"id":     req["id"],
		"result": result,
	})
}

func (c *rpcClient) Next() (map[string]any, error) {
	if len(c.queue) > 0 {
		msg := c.queue[0]
		c.queue = c.queue[1:]
		return msg, nil
	}
	for c.stdout.Scan() {
		line := strings.TrimSpace(c.stdout.Text())
		if line == "" {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		return msg, nil
	}
	if err := c.stdout.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("codex app-server closed stdout: %s", strings.TrimSpace(c.stderr.String()))
}

func (c *rpcClient) write(payload map[string]any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(c.stdin, string(data)+"\n"); err != nil {
		return err
	}
	return nil
}

func newScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, maxScannerBuffer)
	return scanner
}

func splitRPCMessage(msg map[string]any) (string, map[string]any, rpcKind, bool) {
	method := stringAt(msg, "method")
	params, _ := msg["params"].(map[string]any)
	if params == nil {
		params = map[string]any{}
	}
	if method != "" && msg["id"] != nil {
		return method, params, rpcKindRequest, true
	}
	if method != "" {
		return method, params, rpcKindNotification, true
	}
	if msg["id"] != nil {
		return "", params, rpcKindResponse, true
	}
	return "", nil, rpcKindUnknown, false
}

func defaultRequestResponse(method string) map[string]any {
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		return map[string]any{"decision": "accept"}
	default:
		return map[string]any{}
	}
}

func usageFromTokenUpdate(params map[string]any) provider.Usage {
	last, _ := nestedMap(params, "tokenUsage", "last")
	return provider.Usage{
		InputTokens:      intAt(last, "inputTokens"),
		OutputTokens:     intAt(last, "outputTokens"),
		TotalTokens:      intAt(last, "totalTokens"),
		CachedReadTokens: intAt(last, "cachedInputTokens"),
		ThoughtTokens:    intAt(last, "reasoningOutputTokens"),
	}
}

func normalizeCodexTurnFailure(params map[string]any, model string) error {
	msg := nestedString(params, "turn", "error", "message")
	if msg == "" {
		msg = "codex turn failed"
	}
	if looksLikeContextOverflow(msg) {
		return &provider.ContextOverflowError{Provider: Kind, Model: model, Message: msg}
	}
	return errors.New(msg)
}

func normalizeCodexError(err error, model string) error {
	if err == nil {
		return nil
	}
	if looksLikeContextOverflow(err.Error()) {
		return &provider.ContextOverflowError{Provider: Kind, Model: model, Message: err.Error(), Cause: err}
	}
	return err
}

func looksLikeContextOverflow(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "context") && (strings.Contains(msg, "overflow") || strings.Contains(msg, "length") || strings.Contains(msg, "exceed"))
}

func makeLocalCheckpoint(messages []provider.Message, reason string) []provider.Message {
	if len(messages) <= 3 {
		return nil
	}
	recentCount := 2
	if recentCount > len(messages)-1 {
		recentCount = len(messages) - 1
	}
	start := len(messages) - recentCount
	if start < 1 {
		start = 1
	}
	out := make([]provider.Message, 0, 2+len(messages[start:]))
	if messages[0].Role == "system" || messages[0].Type == "system" {
		out = append(out, messages[0])
	}
	note := "Earlier conversation history was compacted in the upstream Codex session and should be treated as preserved there."
	if strings.TrimSpace(reason) != "" {
		note += "\n\nReason: " + strings.TrimSpace(reason)
	}
	out = append(out, provider.Message{Role: "user", Content: note})
	out = append(out, messages[start:]...)
	return out
}

func sessionKeyFor(opts provider.StreamOptions) string {
	return firstNonEmpty(strings.TrimSpace(opts.SessionID), strings.TrimSpace(opts.PromptCacheKey))
}

func messageFingerprints(messages []provider.Message) []string {
	out := make([]string, 0, len(messages))
	for _, msg := range messages {
		var b strings.Builder
		b.WriteString(msg.Role)
		b.WriteString("|")
		b.WriteString(msg.Type)
		b.WriteString("|")
		b.WriteString(contentText(msg.Content))
		for _, tc := range msg.ToolCalls {
			b.WriteString("|tool:")
			b.WriteString(tc.ID)
			b.WriteString(":")
			b.WriteString(tc.Function.Name)
			b.WriteString(":")
			b.WriteString(tc.Function.Arguments)
		}
		if msg.ToolCallID != "" {
			b.WriteString("|tool_result:")
			b.WriteString(msg.ToolCallID)
		}
		out = append(out, b.String())
	}
	return out
}

func incrementalTranscript(prev, now []string, messages []provider.Message) (string, bool) {
	if len(now) < len(prev) {
		return "", false
	}
	for i := range prev {
		if prev[i] != now[i] {
			return "", false
		}
	}
	if len(now) == len(prev) {
		return "", false
	}
	return formatTranscript(messages[len(prev):]), true
}

func formatTranscript(messages []provider.Message) string {
	var b strings.Builder
	for _, msg := range messages {
		role := firstNonEmpty(msg.Role, msg.Type, "user")
		switch msg.Role {
		case "system":
			b.WriteString("[System]\n")
			b.WriteString(strings.TrimSpace(contentText(msg.Content)))
			b.WriteString("\n\n")
		case "assistant":
			text := strings.TrimSpace(contentText(msg.Content))
			if text != "" {
				b.WriteString("[Assistant]\n")
				b.WriteString(text)
				b.WriteString("\n\n")
			}
			for _, tc := range msg.ToolCalls {
				b.WriteString("[Assistant tool call]\n")
				b.WriteString(tc.Function.Name)
				b.WriteString("(")
				b.WriteString(tc.Function.Arguments)
				b.WriteString(")\n\n")
			}
		case "tool":
			b.WriteString("[Tool result")
			if msg.ToolCallID != "" {
				b.WriteString(" ")
				b.WriteString(msg.ToolCallID)
			}
			b.WriteString("]\n")
			b.WriteString(strings.TrimSpace(contentText(msg.Content)))
			b.WriteString("\n\n")
		default:
			b.WriteString("[")
			b.WriteString(strings.Title(role))
			b.WriteString("]\n")
			b.WriteString(strings.TrimSpace(contentText(msg.Content)))
			b.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func contentText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []any:
		var parts []string
		for _, p := range x {
			if m, ok := p.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func cloneModels(in []provider.ModelInfo) []provider.ModelInfo {
	out := make([]provider.ModelInfo, len(in))
	copy(out, in)
	return out
}

func attachNativeAgentModelMetadata(in []provider.ModelInfo) []provider.ModelInfo {
	for i := range in {
		if len(in[i].Tags) == 0 {
			in[i].Tags = []string{"native_agent"}
		} else if !containsString(in[i].Tags, "native_agent") {
			in[i].Tags = append(in[i].Tags, "native_agent")
		}
		if in[i].Compat == nil {
			in[i].Compat = map[string]any{}
		}
		in[i].Compat["agent_loop"] = "provider_native"
	}
	return in
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func usagePtr(usage provider.Usage) *provider.Usage {
	if usage == (provider.Usage{}) {
		return nil
	}
	cp := usage
	return &cp
}

func stringAt(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func intAt(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func nestedMap(m map[string]any, path ...string) (map[string]any, bool) {
	cur := m
	for i, key := range path {
		if i == len(path)-1 {
			next, ok := cur[key].(map[string]any)
			return next, ok
		}
		next, ok := cur[key].(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return nil, false
}

func nestedString(m map[string]any, path ...string) string {
	cur := m
	for i, key := range path {
		if i == len(path)-1 {
			return stringAt(cur, key)
		}
		next, ok := cur[key].(map[string]any)
		if !ok {
			return ""
		}
		cur = next
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
