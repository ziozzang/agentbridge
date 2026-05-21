package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	contextcompact "github.com/ziozzang/agentbridge/internal/compaction"
	"github.com/ziozzang/agentbridge/internal/logger"
	"github.com/ziozzang/agentbridge/internal/protocol/systemprompt"
	"github.com/ziozzang/agentbridge/internal/provider"
	"github.com/ziozzang/agentbridge/internal/provider/glm"
	"github.com/ziozzang/agentbridge/internal/tools/executor"
)

const maxSubagentDepth = 1

type subagentDepthKey struct{}

type subagentOptions struct {
	Model string
	Task  string
}

func (a *Agent) runSubagent(ctx context.Context, parent *sessionState, opts subagentOptions) (string, error) {
	task := strings.TrimSpace(opts.Task)
	if task == "" {
		return "", fmt.Errorf("subagent task is empty")
	}
	model := strings.TrimSpace(opts.Model)
	if model == "" {
		model = parent.Model
	}
	if model == "" {
		model = a.defaultModel()
	}
	tools := parent.tools
	if len(tools) == 0 {
		tools = a.availableTools()
	}
	tools = a.profileTools(model, tools)
	toolNames := make([]string, len(tools))
	for i, t := range tools {
		toolNames[i] = t.Function.Name
	}
	system := systemprompt.Build(systemprompt.Input{
		Cwd:      parent.Cwd,
		Tools:    toolNames,
		AgentsMD: systemprompt.LoadProjectContext(parent.Cwd),
		Profile:  a.profilePrompt(model),
	})
	if skillPrompt := a.activeSkillPrompt(parent); skillPrompt != "" {
		system += "\n\n" + skillPrompt
	}
	childID := parent.ID + "/sub/" + randomHex(4)
	title := "Subagent"
	if task != "" {
		title += ": " + truncateStatusText(task, 80)
	}
	a.notifyUpdate(parent.ID, map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    childID,
		"title":         title,
		"kind":          "agent",
		"status":        "in_progress",
		"rawInput":      map[string]any{"model": model, "task": task},
	})
	messages := []provider.Message{
		{Role: "system", Content: system + "\n\nYou are a bounded subagent. Complete only the delegated task and return a concise result for the parent agent."},
		{Role: "user", Content: task},
	}
	exec := &executor.Executor{
		Conn:       a.Conn,
		SessionID:  parent.ID,
		SessionCwd: parent.Cwd,
		MCP:        a.MCP,
		Vision:     a.visionClient(),
		Mode:       parent.Mode,
		SessionMCP: parent.sessionMcp,
		Plugins:    a.Plugins,
	}
	var out strings.Builder
	maxTurns := a.MaxTurns
	if maxTurns <= 0 {
		maxTurns = DefaultMaxTurns
	}
	compactSettings := loadCompactionSettings()
	overflowRetries := 0
	trace := make([]string, 0)
	childCtx := context.WithValue(ctx, subagentDepthKey{}, subagentDepth(ctx)+1)
	completed := false
	for iter := 0; iter < maxTurns; iter++ {
		if childCtx.Err() != nil {
			a.notifyUpdate(parent.ID, map[string]any{
				"sessionUpdate": "tool_call_update",
				"toolCallId":    childID,
				"status":        "failed",
				"rawOutput":     map[string]any{"error": childCtx.Err().Error()},
			})
			return "", childCtx.Err()
		}
		window := a.contextWindow(a.effectiveModel(model))
		if compactSettings.Enabled && contextcompact.EstimateTokens(messages) > compactSettings.ProactiveThreshold(window) {
			result := a.compactPromptMessages(childCtx, messages, a.effectiveModel(model), tools, compactSettings, compactSettings.TargetTokens(window), "subagent proactive context compaction")
			if result.Compacted {
				messages = result.Messages
				trace = append(trace, fmt.Sprintf("compacted context: %d -> %d tokens", result.TokensBefore, contextcompact.EstimateTokens(result.Messages)))
			} else if compactSettings.PruneFallbackEnabled {
				messages = contextcompact.PruneMessages(messages, compactSettings.TargetTokens(window), compactSettings.PreserveTurns)
				trace = append(trace, "pruned context after proactive threshold")
			}
		}
		exec.Mode = parent.Mode
		chunks, errs := a.streamChat(childCtx, messages, glm.StreamOptions{
			Model:     a.effectiveModel(model),
			Tools:     tools,
			SessionID: childID,
		})
		var assistantText string
		var toolCalls []glm.ToolCall
		for c := range chunks {
			if c.Text != "" {
				assistantText += c.Text
			}
			if c.ToolCall != nil {
				toolCalls = append(toolCalls, *c.ToolCall)
			}
		}
		if err := <-errs; err != nil {
			var apiErr *glm.APIError
			isOverflow := provider.IsContextOverflow(err) || (errors.As(err, &apiErr) && apiErr.IsContextOverflow())
			if isOverflow && overflowRetries < 1 {
				window := a.contextWindow(a.effectiveModel(model))
				result := a.compactPromptMessages(childCtx, messages, a.effectiveModel(model), tools, compactSettings, compactSettings.OverflowTargetTokens(window), "subagent context overflow retry")
				if result.Compacted {
					logger.Debugf("subagent: emergency compacted context tokens_before=%d tokens_after=%d", result.TokensBefore, contextcompact.EstimateTokens(result.Messages))
					messages = result.Messages
					trace = append(trace, fmt.Sprintf("emergency compacted context: %d -> %d tokens", result.TokensBefore, contextcompact.EstimateTokens(result.Messages)))
				} else if compactSettings.PruneFallbackEnabled {
					messages = contextcompact.PruneMessages(messages, compactSettings.OverflowTargetTokens(window), compactSettings.PreserveTurns)
					trace = append(trace, "pruned context after context overflow")
				}
				overflowRetries++
				iter--
				continue
			}
			a.notifyUpdate(parent.ID, map[string]any{
				"sessionUpdate": "tool_call_update",
				"toolCallId":    childID,
				"status":        "failed",
				"rawOutput":     map[string]any{"error": err.Error()},
			})
			return "", fmt.Errorf("subagent stream failed: %w", err)
		}
		assistantMsg := provider.Message{Role: "assistant", Content: assistantText}
		if len(toolCalls) > 0 {
			tcs := make([]provider.ToolCallMsg, len(toolCalls))
			for i, t := range toolCalls {
				tcs[i] = provider.ToolCallMsg{
					ID:   t.ID,
					Type: "function",
					Function: provider.ToolCallMsgFunction{
						Name:      t.Name,
						Arguments: t.Arguments,
					},
				}
			}
			assistantMsg.ToolCalls = tcs
		}
		messages = append(messages, assistantMsg)
		if len(toolCalls) == 0 {
			out.WriteString(assistantText)
			completed = true
			break
		}
		for _, tc := range toolCalls {
			trace = append(trace, fmt.Sprintf("tool %s(%s)", tc.Name, truncateStatusText(tc.Arguments, 120)))
			a.notifyUpdate(parent.ID, map[string]any{
				"sessionUpdate": "tool_call_update",
				"toolCallId":    childID,
				"status":        "in_progress",
				"rawOutput":     map[string]any{"event": "subagent_tool_call", "tool": tc.Name, "arguments": tc.Arguments},
			})
			res := exec.Execute(childCtx, tc.ID, tc.Name, tc.Arguments)
			trace = append(trace, fmt.Sprintf("tool %s result: %s", tc.Name, truncateStatusText(res.Content, 160)))
			messages = append(messages, provider.Message{Role: "tool", ToolCallID: tc.ID, Content: res.Content})
		}
	}
	if !completed {
		rawOutput := map[string]any{"model": model, "error": "subagent reached max turns"}
		if len(trace) > 0 {
			rawOutput["trace"] = trace
		}
		a.notifyUpdate(parent.ID, map[string]any{
			"sessionUpdate": "tool_call_update",
			"toolCallId":    childID,
			"status":        "failed",
			"rawOutput":     rawOutput,
		})
		return "", fmt.Errorf("subagent reached max turns")
	}
	text := strings.TrimSpace(out.String())
	if text == "" {
		text = "(subagent returned no text)"
	}
	rawOutput := map[string]any{"model": model, "output": text}
	if len(trace) > 0 {
		rawOutput["trace"] = trace
	}
	a.notifyUpdate(parent.ID, map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    childID,
		"status":        "completed",
		"content":       []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": text}}},
		"rawOutput":     rawOutput,
	})
	if len(trace) == 0 {
		return text, nil
	}
	return text + "\n\nsubagent trace:\n- " + strings.Join(trace, "\n- "), nil
}

func subagentDepth(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	if v, ok := ctx.Value(subagentDepthKey{}).(int); ok {
		return v
	}
	return 0
}

func truncateStatusText(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
