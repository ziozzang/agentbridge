package httpcompat

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/ziozzang/agentbridge/internal/acp"
	contextcompact "github.com/ziozzang/agentbridge/internal/compaction"
	"github.com/ziozzang/agentbridge/internal/logger"
	"github.com/ziozzang/agentbridge/internal/protocol/systemprompt"
	"github.com/ziozzang/agentbridge/internal/provider"
	"github.com/ziozzang/agentbridge/internal/runtimeconfig"
	"github.com/ziozzang/agentbridge/internal/tools/definitions"
	"github.com/ziozzang/agentbridge/internal/tools/executor"
)

const (
	httpAgentModelPrefix = "agent:"
	httpAgentMaxTurns    = 20
)

type httpAgentOptions struct {
	Enabled         bool
	Model           string
	Cwd             string
	MaxTurns        int
	SessionID       string
	PromptCacheKey  string
	ServiceTier     string
	ReasoningEffort string
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
	chunks, errs, err := StreamProviderWithOptions(ctx, model, messages, provider.StreamOptions{
		SessionID:       agentOpts.SessionID,
		PromptCacheKey:  agentOpts.PromptCacheKey,
		ServiceTier:     agentOpts.ServiceTier,
		ReasoningEffort: agentOpts.ReasoningEffort,
	})
	if err != nil {
		return "", provider.Usage{}, "", err
	}
	var b strings.Builder
	var usage provider.Usage
	var stop string
	for ch := range chunks {
		b.WriteString(ch.Text)
		if ch.Usage != nil {
			usage = *ch.Usage
		}
		if ch.StopReason != "" {
			stop = ch.StopReason
		}
	}
	if err := <-errs; err != nil {
		return "", usage, stop, err
	}
	return b.String(), usage, stop, nil
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
	compactSettings := loadHTTPCompactionSettings()
	overflowRetries := 0
	for i := 0; i < maxTurns; i++ {
		if ctx.Err() != nil {
			return finalText.String(), usage, "cancelled", ctx.Err()
		}
		window := p.ContextWindow(model)
		if compactSettings.Enabled && contextcompact.EstimateTokens(loopMessages) > compactSettings.ProactiveThreshold(window) {
			compacted, ok := compactHTTPMessages(ctx, p, loopMessages, model, tools, compactSettings, compactSettings.TargetTokens(window), "http agent proactive context compaction")
			if ok {
				loopMessages = compacted
			}
		}
		chunks, errs := p.StreamChat(ctx, loopMessages, provider.StreamOptions{
			Model:           model,
			Tools:           tools,
			SessionID:       opts.SessionID,
			PromptCacheKey:  opts.PromptCacheKey,
			ServiceTier:     opts.ServiceTier,
			ReasoningEffort: opts.ReasoningEffort,
		})
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
			if provider.IsContextOverflow(err) && overflowRetries < 1 && compactSettings.Enabled {
				compacted, ok := compactHTTPMessages(ctx, p, loopMessages, model, tools, compactSettings, compactSettings.OverflowTargetTokens(window), "http agent context overflow retry")
				if ok {
					loopMessages = compacted
					overflowRetries++
					i--
					continue
				}
			}
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

func compactHTTPMessages(ctx context.Context, p provider.Provider, messages []provider.Message, model string, tools []definitions.Tool, settings contextcompact.Settings, targetTokens int, reason string) ([]provider.Message, bool) {
	if len(messages) <= 3 {
		return messages, false
	}
	if settings.NativeEnabled {
		if compactor, ok := p.(provider.ConversationCompactor); ok {
			out, err := compactor.CompactConversation(ctx, messages, provider.CompactOptions{
				Model:        model,
				Tools:        tools,
				TargetTokens: targetTokens,
				Reason:       reason,
			})
			if err == nil && len(out) > 0 {
				if out[0].Role != "system" && out[0].Type != "system" {
					out = append([]provider.Message{messages[0]}, out...)
				}
				return out, true
			}
			if err != nil && !errors.Is(err, provider.ErrNativeCompactionUnavailable) {
				logger.Warnf("http agent compaction: provider-native failed, using fallback: %v", err)
			}
		}
	}
	if settings.SummaryEnabled {
		if out, err := summarizeHTTPMessages(ctx, p, messages, model, settings, reason); err == nil && len(out) > 0 {
			return out, true
		} else if err != nil && !errors.Is(err, provider.ErrNativeCompactionUnavailable) {
			logger.Warnf("http agent compaction: summary failed, using pruning fallback: %v", err)
		}
	}
	if settings.PruneFallbackEnabled {
		return contextcompact.PruneMessages(messages, targetTokens, settings.PreserveTurns), true
	}
	return messages, false
}

func summarizeHTTPMessages(ctx context.Context, p provider.Provider, messages []provider.Message, model string, settings contextcompact.Settings, reason string) ([]provider.Message, error) {
	cut := httpCompactionCutPoint(messages[1:], settings.KeepRecentTokens)
	if cut <= 0 || cut >= len(messages)-1 {
		return nil, provider.ErrNativeCompactionUnavailable
	}
	system := messages[0]
	history := messages[1:]
	toSummarize := history[:cut]
	recent := history[cut:]
	prompt := "You are performing a CONTEXT CHECKPOINT COMPACTION. Create a concise structured handoff summary preserving current progress, constraints, key decisions, next steps, exact file paths, commands, errors, and tool results needed to continue.\n\n"
	if reason != "" {
		prompt += "Reason: " + reason + "\n\n"
	}
	prompt += "<conversation>\n" + serializeHTTPMessagesForSummary(toSummarize) + "\n</conversation>"
	chunks, errs := p.StreamChat(ctx, []provider.Message{{Role: "user", Content: prompt}}, provider.StreamOptions{Model: model})
	var b strings.Builder
	for ch := range chunks {
		b.WriteString(ch.Text)
	}
	if err := <-errs; err != nil {
		return nil, err
	}
	summary := strings.TrimSpace(b.String())
	if summary == "" {
		return nil, provider.ErrNativeCompactionUnavailable
	}
	out := []provider.Message{
		system,
		{Role: "user", Content: "The conversation history before this point was compacted into the following summary:\n\n<summary>\n" + summary + "\n</summary>"},
	}
	out = append(out, recent...)
	return out, nil
}

func httpCompactionCutPoint(messages []provider.Message, keepRecentTokens int) int {
	accumulated := 0
	cut := 0
	for i := len(messages) - 1; i >= 0; i-- {
		accumulated += contextcompact.EstimateTokens(messages[i : i+1])
		if accumulated >= keepRecentTokens {
			cut = i
			break
		}
	}
	for cut > 0 && messages[cut].Role != "user" {
		cut--
	}
	return cut
}

func serializeHTTPMessagesForSummary(messages []provider.Message) string {
	var parts []string
	for _, msg := range messages {
		text := strings.TrimSpace(messageContentText(msg.Content))
		switch msg.Role {
		case "system":
			continue
		case "assistant":
			if text != "" {
				parts = append(parts, "[Assistant]: "+text)
			}
			for _, tc := range msg.ToolCalls {
				parts = append(parts, "[Assistant tool call]: "+tc.Function.Name+"("+tc.Function.Arguments+")")
			}
		case "tool":
			if text != "" {
				parts = append(parts, "[Tool result]: "+truncateHTTPText(text, 2000))
			}
		default:
			if text != "" {
				parts = append(parts, "["+firstNonEmpty(msg.Role, msg.Type)+"]: "+text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func messageContentText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var out []string
		for _, part := range c {
			if m, ok := part.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					out = append(out, text)
				}
			}
		}
		return strings.Join(out, "")
	default:
		if c == nil {
			return ""
		}
		b, _ := json.Marshal(c)
		return string(b)
	}
}

func truncateHTTPText(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}
	return text[:maxChars] + "\n\n[truncated]"
}

func loadHTTPCompactionSettings() contextcompact.Settings {
	rc, err := runtimeconfig.Load()
	if err != nil {
		logger.Warnf("http agent compaction: failed to load runtime config, using defaults: %v", err)
		return contextcompact.SettingsFromEnv(contextcompact.DefaultSettings())
	}
	return contextcompact.SettingsFromConfig(contextcompact.RuntimeConfig{
		Enabled:           rc.Compaction.Enabled,
		Native:            rc.Compaction.Native,
		Summary:           rc.Compaction.Summary,
		PruneFallback:     rc.Compaction.PruneFallback,
		ThresholdPct:      rc.Compaction.ThresholdPct,
		TargetPct:         rc.Compaction.TargetPct,
		OverflowTargetPct: rc.Compaction.OverflowTargetPct,
		PreserveTurns:     rc.Compaction.PreserveTurns,
		KeepRecentTokens:  rc.Compaction.KeepRecentTokens,
		ReserveTokens:     rc.Compaction.ReserveTokens,
	})
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
		if v := stringMeta(m, "session_id"); v != "" {
			opts.SessionID = v
		} else if v := stringMeta(m, "sessionId"); v != "" {
			opts.SessionID = v
		} else if v := stringMeta(m, "thread_id"); v != "" {
			opts.SessionID = v
		}
		if v := stringMeta(m, "prompt_cache_key"); v != "" {
			opts.PromptCacheKey = v
		} else if v := stringMeta(m, "cache_key"); v != "" {
			opts.PromptCacheKey = v
		}
		if v := stringMeta(m, "service_tier"); v != "" {
			opts.ServiceTier = v
		}
		if v := stringMeta(m, "reasoning_effort"); v != "" {
			opts.ReasoningEffort = v
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
