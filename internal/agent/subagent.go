package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/ziozzang/agentbridge/internal/protocol/systemprompt"
	"github.com/ziozzang/agentbridge/internal/provider/glm"
	"github.com/ziozzang/agentbridge/internal/tools/executor"
)

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
	system := systemprompt.Build(systemprompt.Input{
		Cwd:      parent.Cwd,
		Tools:    nil,
		AgentsMD: systemprompt.LoadProjectContext(parent.Cwd),
		Profile:  a.profilePrompt(model),
	})
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
	messages := []glm.Message{
		{Role: "system", Content: system + "\n\nYou are a bounded subagent. Complete only the delegated task and return a concise result for the parent agent."},
		{Role: "user", Content: task},
	}
	tools := parent.tools
	if len(tools) == 0 {
		tools = a.availableTools()
	}
	tools = a.profileTools(model, tools)
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
	for iter := 0; iter < maxTurns; iter++ {
		if ctx.Err() != nil {
			a.notifyUpdate(parent.ID, map[string]any{
				"sessionUpdate": "tool_call_update",
				"toolCallId":    childID,
				"status":        "failed",
				"rawOutput":     map[string]any{"error": ctx.Err().Error()},
			})
			return "", ctx.Err()
		}
		exec.Mode = parent.Mode
		chunks, errs := a.streamChat(ctx, messages, glm.StreamOptions{
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
			a.notifyUpdate(parent.ID, map[string]any{
				"sessionUpdate": "tool_call_update",
				"toolCallId":    childID,
				"status":        "failed",
				"rawOutput":     map[string]any{"error": err.Error()},
			})
			return "", fmt.Errorf("subagent stream failed: %w", err)
		}
		assistantMsg := glm.Message{Role: "assistant", Content: assistantText}
		if len(toolCalls) > 0 {
			tcs := make([]glm.ToolCallMsg, len(toolCalls))
			for i, t := range toolCalls {
				tcs[i] = glm.ToolCallMsg{
					ID:   t.ID,
					Type: "function",
					Function: glm.ToolCallMsgFunction{
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
			break
		}
		for _, tc := range toolCalls {
			res := exec.Execute(ctx, tc.ID, tc.Name, tc.Arguments)
			messages = append(messages, glm.Message{Role: "tool", ToolCallID: tc.ID, Content: res.Content})
		}
	}
	text := strings.TrimSpace(out.String())
	if text == "" {
		text = "(subagent returned no text)"
	}
	a.notifyUpdate(parent.ID, map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    childID,
		"status":        "completed",
		"content":       []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": text}}},
		"rawOutput":     map[string]any{"model": model, "output": text},
	})
	return text, nil
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
