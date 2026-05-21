// Package claudecode adapts Claude Code CLI as a harness provider.
//
// This is intentionally process-backed rather than HTTP-backed: each
// StreamChat call runs `claude -p ... --output-format json` and converts the
// single result object into harness chunks. It is a conservative first step
// before supporting Claude Code's persistent stream-json mode.
package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ziozzang/agentbridge/internal/provider"
	"github.com/ziozzang/agentbridge/internal/tools/definitions"
)

// Kind is the registry key for the Claude Code CLI provider.
const Kind = "claude-code-cli"

func init() {
	provider.Register(Kind, func(cfg provider.Config) (provider.Provider, error) {
		return New(cfg), nil
	})
}

// Client invokes the Claude Code CLI.
type Client struct {
	cfg provider.Config
}

// New constructs a Claude Code CLI provider.
func New(cfg provider.Config) *Client {
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = "sonnet"
	}
	if cfg.ContextWindow <= 0 {
		cfg.ContextWindow = 200_000
	}
	return &Client{cfg: cfg}
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
	return "sonnet"
}

func (c *Client) ContextWindow(model string) int {
	_ = model
	return c.cfg.ContextWindow
}

func (c *Client) Config() provider.Config { return c.cfg }

type result struct {
	Type       string `json:"type"`
	Subtype    string `json:"subtype"`
	IsError    bool   `json:"is_error"`
	Result     string `json:"result"`
	StopReason string `json:"stop_reason"`
	SessionID  string `json:"session_id"`
	Usage      *struct {
		InputTokens         int `json:"input_tokens"`
		CacheReadTokens     int `json:"cache_read_input_tokens"`
		OutputTokens        int `json:"output_tokens"`
		CacheCreationTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// StreamChat implements provider.Provider.
func (c *Client) StreamChat(ctx context.Context, messages []provider.Message, opts provider.StreamOptions) (<-chan provider.Chunk, <-chan error) {
	chunks := make(chan provider.Chunk, 2)
	errs := make(chan error, 1)
	go func() {
		defer close(chunks)
		defer close(errs)

		out, err := c.run(ctx, c.prompt(messages), opts.Model)
		if err != nil {
			errs <- err
			return
		}
		if out.Result != "" {
			chunks <- provider.Chunk{Text: out.Result}
		}
		chunk := provider.Chunk{Done: true, StopReason: firstNonEmpty(out.StopReason, "end_turn")}
		if out.Usage != nil {
			chunk.Usage = &provider.Usage{
				InputTokens:      out.Usage.InputTokens,
				OutputTokens:     out.Usage.OutputTokens,
				TotalTokens:      out.Usage.InputTokens + out.Usage.OutputTokens,
				CachedReadTokens: out.Usage.CacheReadTokens,
			}
		}
		chunks <- chunk
		errs <- nil
	}()
	return chunks, errs
}

func (c *Client) run(ctx context.Context, prompt, model string) (*result, error) {
	if model == "" {
		model = c.DefaultModel()
	}
	cmd := exec.CommandContext(ctx, c.command(), "-p", prompt, "--output-format", "json", "--model", model)
	if permissionMode := c.extraString("permission_mode"); permissionMode != "" {
		cmd.Args = append(cmd.Args, "--permission-mode", permissionMode)
	}
	if tools := c.extraString("tools"); tools != "" {
		cmd.Args = append(cmd.Args, "--tools", tools)
	}
	if allowed := c.extraString("allowed_tools"); allowed != "" {
		cmd.Args = append(cmd.Args, "--allowedTools", allowed)
	}
	if disallowed := c.extraString("disallowed_tools"); disallowed != "" {
		cmd.Args = append(cmd.Args, "--disallowedTools", disallowed)
	}
	if c.extraBool("bare", false) {
		cmd.Args = append(cmd.Args, "--bare")
	}
	if c.extraBool("no_session_persistence", false) {
		cmd.Args = append(cmd.Args, "--no-session-persistence")
	}
	if v := c.extraString("system_prompt"); v != "" {
		cmd.Args = append(cmd.Args, "--system-prompt", v)
	}
	if v := c.extraString("append_system_prompt"); v != "" {
		cmd.Args = append(cmd.Args, "--append-system-prompt", v)
	}
	cmd.Env = c.commandEnv()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("claude-code-cli: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var out result
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("claude-code-cli: parse json: %w: %s", err, strings.TrimSpace(string(data)))
	}
	if out.IsError {
		return nil, fmt.Errorf("claude-code-cli: result error: %s", strings.TrimSpace(out.Result))
	}
	return &out, nil
}

func (c *Client) command() string {
	if v := c.extraString("command"); v != "" {
		return v
	}
	return "claude"
}

func (c *Client) commandEnv() []string {
	env := os.Environ()
	for key, value := range c.extraStringMap("env") {
		if strings.TrimSpace(value) == "" {
			continue
		}
		env = upsertEnv(env, strings.TrimSpace(key), value)
	}
	return env
}

func upsertEnv(env []string, key, value string) []string {
	if key == "" {
		return env
	}
	prefix := key + "="
	for i, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func (c *Client) prompt(messages []provider.Message) string {
	var b strings.Builder
	for _, m := range messages {
		if m.Content == nil && len(m.ToolCalls) == 0 {
			continue
		}
		role := m.Role
		if role == "" {
			role = "user"
		}
		fmt.Fprintf(&b, "%s: %s\n", role, contentText(m.Content))
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

func (c *Client) extraString(key string) string {
	if c.cfg.Extra == nil {
		return ""
	}
	v, _ := c.cfg.Extra[key].(string)
	return strings.TrimSpace(v)
}

func (c *Client) extraStringMap(key string) map[string]string {
	out := map[string]string{}
	if c.cfg.Extra == nil {
		return out
	}
	switch raw := c.cfg.Extra[key].(type) {
	case map[string]string:
		for k, v := range raw {
			out[k] = v
		}
	case map[string]any:
		for k, v := range raw {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
	}
	return out
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
		}
	}
	return def
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

var _ = definitions.All
