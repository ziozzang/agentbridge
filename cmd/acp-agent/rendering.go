package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/ziozzang/agentbridge/internal/acp"
)

func (c *client) printUpdate(p acp.SessionUpdateParams) {
	update := p.Update
	switch update["sessionUpdate"] {
	case "agent_message_chunk":
		if c.ui != nil {
			c.ui.beginAnswer()
		}
		if text := updateText(update); text != "" {
			if c.stream != nil {
				c.stream.push(text)
			} else {
				fmt.Fprint(c.stdout, text)
				flush(c.stdout)
			}
		}
	case "agent_thought_chunk":
		c.mu.Lock()
		show := c.opts.ShowThinking
		c.mu.Unlock()
		if show {
			if text := updateText(update); text != "" {
				if c.ui != nil && c.ui.active() {
					c.ui.toolCell("thinking", "reasoning", text)
				} else {
					if c.ui != nil {
						c.ui.clear()
					}
					fmt.Fprintf(c.stderr, "\n[thinking] %s\n", text)
					flush(c.stderr)
				}
			}
		}
	case "tool_call":
		if c.stream != nil {
			c.stream.finish()
		}
		title, _ := update["title"].(string)
		status, _ := update["status"].(string)
		kind, _ := update["kind"].(string)
		c.updateToolSurface(update, firstNonEmpty(status, "in_progress"), title, kind)
		c.mu.Lock()
		show := c.opts.ShowTools
		c.mu.Unlock()
		if !show {
			return
		}
		if c.ui != nil && title != "" {
			c.ui.setActivity(c, firstNonEmpty(status, "tool")+" "+title)
		}
		if title != "" {
			if c.ui != nil && c.ui.active() {
				c.ui.toolCell(toolLabel(kind, firstNonEmpty(status, "start")), title, toolDetail(update))
			} else {
				if c.ui != nil {
					c.ui.clear()
				}
				fmt.Fprintf(c.stderr, "\n[tool:%s] %s\n", firstNonEmpty(status, "start"), title)
				flush(c.stderr)
			}
		}
	case "tool_call_update":
		if c.stream != nil {
			c.stream.finish()
		}
		status, _ := update["status"].(string)
		c.updateToolSurface(update, status, "", "")
		c.mu.Lock()
		show := c.opts.ShowTools
		c.mu.Unlock()
		if !show {
			return
		}
		if c.ui != nil && status != "" {
			c.ui.setActivity(c, "tool "+status)
		}
		detail := toolDetail(update)
		if status == "completed" && strings.TrimSpace(detail) == "" {
			return
		}
		if status != "" {
			if c.ui != nil && c.ui.active() {
				c.ui.toolCell(status, "tool update", detail)
			} else {
				if c.ui != nil {
					c.ui.clear()
				}
				fmt.Fprintf(c.stderr, "[tool:%s]\n", status)
				flush(c.stderr)
			}
		}
	case "session_info_update":
		c.applySessionInfoUpdate(update)
		return
	case "current_mode_update":
		if mode, _ := update["currentModeId"].(string); mode != "" {
			c.mu.Lock()
			c.state.Mode = mode
			c.mu.Unlock()
			if c.ui != nil {
				c.ui.refresh(c)
			}
		}
		return
	default:
		c.mu.Lock()
		rawUpdates := c.opts.RawUpdates
		c.mu.Unlock()
		if rawUpdates {
			raw, _ := json.Marshal(update)
			fmt.Fprintf(c.stderr, "\n[update] %s\n", raw)
			flush(c.stderr)
		}
	}
}

func (c *client) updateToolSurface(update map[string]any, status, title, kind string) {
	id, _ := update["toolCallId"].(string)
	if id == "" {
		return
	}
	if kind == "" {
		if strings.Contains(id, "/sub/") {
			kind = "agent"
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeTools == nil {
		c.activeTools = map[string]string{}
	}
	if c.activeAgents == nil {
		c.activeAgents = map[string]string{}
	}
	if title == "" {
		title = c.activeTools[id]
		if title == "" {
			title = c.activeAgents[id]
		}
	}
	done := status == "completed" || status == "failed" || status == "cancelled"
	if done {
		delete(c.activeTools, id)
		delete(c.activeAgents, id)
	} else if kind == "agent" {
		c.activeAgents[id] = title
		delete(c.activeTools, id)
	} else {
		c.activeTools[id] = title
		delete(c.activeAgents, id)
	}
	if title != "" {
		c.state.LastTool = title
	}
	c.state.Tools = len(c.activeTools)
	c.state.Subagents = len(c.activeAgents)
}

func toolLabel(kind, status string) string {
	if kind == "agent" {
		return "subagent:" + status
	}
	return status
}

func (c *client) applySessionInfoUpdate(update map[string]any) {
	c.mu.Lock()
	if model, _ := update["model"].(string); model != "" {
		c.state.Model = model
	}
	if ctxMap, ok := update["context"].(map[string]any); ok {
		c.state.Context = parseContextState(ctxMap)
	}
	if limits := parseLimitState(update); limits != (limitState{}) {
		c.state.Limits = limits
	}
	c.mu.Unlock()
	if c.ui != nil {
		c.ui.refresh(c)
	}
}

func parseLimitState(update map[string]any) limitState {
	for _, key := range []string{"limits", "rate_limits", "usage_limits", "quota"} {
		if raw, ok := update[key]; ok {
			return parseLimitValue(raw)
		}
	}
	return limitState{}
}

func parseLimitValue(raw any) limitState {
	out := limitState{}
	switch v := raw.(type) {
	case map[string]any:
		out.FiveHourPercent = firstPercent(v, "5h", "five_hour", "fiveHour", "five-hour-limit")
		out.WeeklyPercent = firstPercent(v, "weekly", "week", "weekly-limit")
		out.MonthlyPercent = firstPercent(v, "monthly", "month", "monthly-limit")
		out.Refreshing = boolFromAny(v["refreshing"])
	case []any:
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name := strings.ToLower(firstNonEmpty(stringFromAny(m["name"]), stringFromAny(m["label"]), stringFromAny(m["limit_name"]), stringFromAny(m["id"]), stringFromAny(m["limit_id"])))
			pct := firstPercent(m, "percent", "remaining_percent", "used_percent", "usage_percent")
			if pct == 0 {
				continue
			}
			switch {
			case strings.Contains(name, "5h") || strings.Contains(name, "five"):
				out.FiveHourPercent = pct
			case strings.Contains(name, "week"):
				out.WeeklyPercent = pct
			case strings.Contains(name, "month"):
				out.MonthlyPercent = pct
			}
		}
	}
	return out
}

func firstPercent(m map[string]any, keys ...string) float64 {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			switch x := v.(type) {
			case map[string]any:
				if p := firstPercent(x, "percent", "remaining_percent", "used_percent", "usage_percent"); p != 0 {
					return p
				}
			default:
				if p := floatFromAny(v); p != 0 {
					if p <= 1 {
						p *= 100
					}
					return p
				}
			}
		}
	}
	return 0
}

func parseContextState(m map[string]any) contextState {
	return contextState{
		Tokens:       intFromAny(m["tokens"]),
		Window:       intFromAny(m["window"]),
		UsedPercent:  floatFromAny(m["used_percent"]),
		LeftPercent:  floatFromAny(m["left_percent"]),
		Messages:     intFromAny(m["messages"]),
		Checkpoints:  intFromAny(m["checkpoints"]),
		CacheEpoch:   intFromAny(m["cache_epoch"]),
		CompactionOn: boolFromAny(m["compaction_enabled"]),
	}
}

func intFromAny(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	default:
		return 0
	}
}

func floatFromAny(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		n, _ := x.Float64()
		return n
	case string:
		n, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return n
	default:
		return 0
	}
}

func boolFromAny(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		b, _ := strconv.ParseBool(strings.TrimSpace(x))
		return b
	default:
		return false
	}
}

func flush(w io.Writer) {
	if f, ok := w.(interface{ Flush() error }); ok {
		_ = f.Flush()
	}
}

func updateText(update map[string]any) string {
	content, ok := update["content"].(map[string]any)
	if !ok {
		return ""
	}
	if text, _ := content["text"].(string); text != "" {
		return text
	}
	nested, ok := content["content"].(map[string]any)
	if !ok {
		return ""
	}
	text, _ := nested["text"].(string)
	return text
}

func toolDetail(update map[string]any) string {
	for _, key := range []string{"rawInput", "rawOutput"} {
		if v, ok := update[key]; ok && v != nil {
			return summarizeToolValue(v)
		}
	}
	if content := updateText(update); content != "" {
		return content
	}
	return ""
}

func summarizeToolValue(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return truncateLine(fmt.Sprint(v), 240)
	}
	var lines []string
	for _, key := range []string{"command", "path", "file", "query", "url", "model", "task", "error", "output"} {
		if val, ok := m[key]; ok {
			text := strings.TrimSpace(fmt.Sprint(val))
			if text != "" {
				lines = append(lines, key+": "+truncateLine(text, 220))
			}
		}
	}
	if trace, ok := m["trace"].([]any); ok && len(trace) > 0 {
		lines = append(lines, fmt.Sprintf("trace: %d events", len(trace)))
	}
	if len(lines) > 0 {
		return strings.Join(lines, "\n")
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return truncateLine(fmt.Sprint(m), 240)
	}
	return truncateLine(string(raw), 240)
}
