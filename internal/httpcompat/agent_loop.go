package httpcompat

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/ziozzang/agentbridge/internal/acp"
	"github.com/ziozzang/agentbridge/internal/protocol/systemprompt"
	"github.com/ziozzang/agentbridge/internal/provider"
	"github.com/ziozzang/agentbridge/internal/tools/definitions"
	"github.com/ziozzang/agentbridge/internal/tools/executor"
)

const (
	httpAgentModelPrefix = "agent:"
	httpAgentMaxTurns    = 20
)

type httpAgentOptions struct {
	Enabled  bool
	Model    string
	Cwd      string
	MaxTurns int
}

type noopAgentConn struct{}

func (noopAgentConn) SendNotification(string, any) error { return nil }

func (noopAgentConn) Call(_ context.Context, _ string, _ any, result any) error {
	if resp, ok := result.(*acp.RequestPermissionResponse); ok {
		resp.Outcome = acp.PermissionOutcome{Outcome: "selected", OptionID: "allow"}
	}
	return nil
}

func (h *handler) runProvider(ctx context.Context, model string, messages []provider.Message, agentOpts httpAgentOptions) (string, provider.Usage, string, error) {
	if agentOpts.Enabled {
		return h.runAgentProvider(ctx, agentOpts, messages)
	}
	return RunProvider(ctx, model, messages)
}

func (h *handler) runAgentProvider(ctx context.Context, opts httpAgentOptions, messages []provider.Message) (string, provider.Usage, string, error) {
	p, err := buildProvider()
	if err != nil {
		return "", provider.Usage{}, "", err
	}
	model := strings.TrimSpace(opts.Model)
	if model == "" {
		model = p.DefaultModel()
	}
	maxTurns := opts.MaxTurns
	if maxTurns <= 0 {
		maxTurns = httpAgentMaxTurns
	}
	cwd := strings.TrimSpace(opts.Cwd)
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	tools := definitions.All()
	if h != nil {
		if h.plugins != nil {
			tools = append(tools, h.plugins.Tools()...)
		}
		if h.externalMCP != nil {
			tools = append(tools, h.externalMCP.ToolDefinitions()...)
		}
	}
	toolNames := make([]string, len(tools))
	for i, t := range tools {
		toolNames[i] = t.Function.Name
	}
	system := systemprompt.Build(systemprompt.Input{
		Cwd:      cwd,
		Tools:    toolNames,
		AgentsMD: systemprompt.LoadProjectContext(cwd),
	})
	loopMessages := append([]provider.Message{{Role: "system", Content: system}}, messages...)
	exec := &executor.Executor{
		Conn:       noopAgentConn{},
		SessionID:  "http-agent",
		SessionCwd: cwd,
		Mode:       "bypass_permissions",
	}
	if h != nil {
		exec.Plugins = h.plugins
		exec.SessionMCP = h.externalMCP
	}

	var finalText strings.Builder
	var usage provider.Usage
	stop := "max_turn_requests"
	for i := 0; i < maxTurns; i++ {
		if ctx.Err() != nil {
			return finalText.String(), usage, "cancelled", ctx.Err()
		}
		chunks, errs := p.StreamChat(ctx, loopMessages, provider.StreamOptions{Model: model, Tools: tools})
		var assistantText strings.Builder
		var toolCalls []provider.ToolCall
		streamStop := ""
		for ch := range chunks {
			if ch.Text != "" {
				assistantText.WriteString(ch.Text)
			}
			if ch.ToolCall != nil {
				toolCalls = append(toolCalls, *ch.ToolCall)
			}
			if ch.Usage != nil {
				usage = *ch.Usage
			}
			if ch.StopReason != "" {
				streamStop = ch.StopReason
			}
		}
		if err := <-errs; err != nil {
			return finalText.String(), usage, streamStop, err
		}
		text := assistantText.String()
		if text != "" {
			finalText.WriteString(text)
		}
		assistantMsg := provider.Message{Role: "assistant", Content: text}
		if len(toolCalls) > 0 {
			assistantMsg.ToolCalls = make([]provider.ToolCallMsg, len(toolCalls))
			for j, tc := range toolCalls {
				toolCallID := firstNonEmpty(tc.ID, "toolcall_"+strconv.Itoa(j))
				assistantMsg.ToolCalls[j] = provider.ToolCallMsg{
					ID:   toolCallID,
					Type: "function",
					Function: provider.ToolCallMsgFunction{
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				}
			}
		}
		loopMessages = append(loopMessages, assistantMsg)
		if len(toolCalls) == 0 {
			stop = streamStop
			if stop == "" {
				stop = "stop"
			}
			break
		}
		for _, tc := range toolCalls {
			toolCallID := firstNonEmpty(tc.ID, "toolcall")
			res := exec.Execute(ctx, toolCallID, tc.Name, tc.Arguments)
			loopMessages = append(loopMessages, provider.Message{Role: "tool", ToolCallID: toolCallID, Content: res.Content})
		}
	}
	return finalText.String(), usage, stop, nil
}

func httpAgentOptionsFrom(model string, maps ...map[string]any) httpAgentOptions {
	opts := httpAgentOptions{Model: model}
	if strings.HasPrefix(strings.TrimSpace(model), httpAgentModelPrefix) {
		opts.Enabled = true
		opts.Model = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(model), httpAgentModelPrefix))
	}
	for _, m := range maps {
		if m == nil {
			continue
		}
		if truthyMeta(m, "agent") || truthyMeta(m, "agent_loop") || truthyMeta(m, "agentic") {
			opts.Enabled = true
		}
		if v := stringMeta(m, "model"); opts.Enabled && opts.Model == "" && v != "" {
			opts.Model = v
		}
		if v := stringMeta(m, "cwd"); v != "" {
			opts.Cwd = v
		} else if v := stringMeta(m, "working_directory"); v != "" {
			opts.Cwd = v
		} else if v := stringMeta(m, "workingDirectory"); v != "" {
			opts.Cwd = v
		}
		if v := intMeta(m, "max_turns"); v > 0 {
			opts.MaxTurns = v
		}
	}
	return opts
}

func truthyMeta(m map[string]any, key string) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "true" || s == "1" || s == "yes" || s == "agent"
	case float64:
		return v != 0
	case int:
		return v != 0
	default:
		return false
	}
}

func stringMeta(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func intMeta(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		n, _ := strconv.Atoi(v.String())
		return n
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	default:
		return 0
	}
}
