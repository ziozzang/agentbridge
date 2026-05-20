package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/ziozzang/agentbridge/internal/logger"
	"github.com/ziozzang/agentbridge/internal/provider"
	"github.com/ziozzang/agentbridge/internal/provider/glm"
	"github.com/ziozzang/agentbridge/internal/tools/definitions"
)

const (
	compactionSummaryPrefix = "The conversation history before this point was compacted into the following summary:\n\n<summary>\n"
	compactionSummarySuffix = "\n</summary>"
	toolResultMaxChars      = 2000
)

type compactResult struct {
	Messages     []glm.Message
	Summary      string
	TokensBefore int
	Compacted    bool
}

type compactionSettings struct {
	ReserveTokens    int
	KeepRecentTokens int
}

var defaultCompactionSettings = compactionSettings{
	ReserveTokens:    16_384,
	KeepRecentTokens: 20_000,
}

func (a *Agent) compactPromptMessages(ctx context.Context, messages []glm.Message, model string, tools []definitions.Tool, targetTokens int, reason string) compactResult {
	before := estimateMessagesTokens(messages)
	if len(messages) <= 3 {
		return compactResult{Messages: messages, TokensBefore: before}
	}
	if a.Provider != nil {
		if compactor, ok := a.Provider.(provider.ConversationCompactor); ok {
			compacted, err := compactor.CompactConversation(ctx, messages, provider.CompactOptions{
				Model:        model,
				Tools:        tools,
				TargetTokens: targetTokens,
				Reason:       reason,
			})
			if err == nil && len(compacted) > 0 {
				if compacted[0].Role != "system" && compacted[0].Type != "system" {
					compacted = append([]glm.Message{messages[0]}, compacted...)
				}
				return compactResult{
					Messages:     compacted,
					TokensBefore: before,
					Compacted:    true,
				}
			}
			if err != nil && !errors.Is(err, provider.ErrNativeCompactionUnavailable) {
				logger.Warnf("compaction: provider-native compaction failed, using summary fallback: %v", err)
			}
		}
	}
	settings := defaultCompactionSettings
	if targetTokens > 0 && settings.KeepRecentTokens > targetTokens/2 {
		settings.KeepRecentTokens = max(4_000, targetTokens/2)
	}

	system, history := messages[0], append([]glm.Message(nil), messages[1:]...)
	previousSummary, history := peelCompactionSummary(history)
	cut := findMessageCutPoint(history, settings.KeepRecentTokens)
	if cut <= 0 || cut >= len(history) {
		return compactResult{Messages: messages, TokensBefore: before}
	}

	toSummarize := history[:cut]
	recent := history[cut:]
	summary, err := a.generateCompactionSummary(ctx, toSummarize, model, previousSummary, reason)
	if err != nil || strings.TrimSpace(summary) == "" {
		logger.Warnf("compaction: summarization failed, using local fallback: %v", err)
		summary = localCompactionSummary(toSummarize, previousSummary, reason)
	}
	summary += formatCompactionFileOps(toSummarize)

	summaryMsg := glm.Message{
		Role:    "user",
		Content: compactionSummaryPrefix + summary + compactionSummarySuffix,
	}
	out := make([]glm.Message, 0, 2+len(recent))
	out = append(out, system, summaryMsg)
	out = append(out, recent...)
	return compactResult{
		Messages:     out,
		Summary:      summary,
		TokensBefore: before,
		Compacted:    true,
	}
}

func (a *Agent) generateCompactionSummary(ctx context.Context, messages []glm.Message, model, previousSummary, reason string) (string, error) {
	conversation := serializeMessagesForSummary(messages)
	if strings.TrimSpace(conversation) == "" {
		return "No prior history.", nil
	}
	prompt := "<conversation>\n" + conversation + "\n</conversation>\n\n"
	if previousSummary != "" {
		prompt += "<previous-summary>\n" + previousSummary + "\n</previous-summary>\n\n"
	}
	if previousSummary != "" {
		prompt += updateSummarizationPrompt
	} else {
		prompt += summarizationPrompt
	}
	if reason != "" {
		prompt += "\n\nAdditional focus: " + reason
	}
	chunks, errs := a.streamChat(ctx, []glm.Message{
		{Role: "system", Content: summarizationSystemPrompt},
		{Role: "user", Content: prompt},
	}, provider.StreamOptions{Model: model})
	var b strings.Builder
	for chunk := range chunks {
		b.WriteString(chunk.Text)
	}
	if err := <-errs; err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()), nil
}

func peelCompactionSummary(messages []glm.Message) (string, []glm.Message) {
	if len(messages) == 0 || messages[0].Role != "user" {
		return "", messages
	}
	content, ok := messages[0].Content.(string)
	if !ok || !strings.HasPrefix(content, compactionSummaryPrefix) {
		return "", messages
	}
	summary := strings.TrimPrefix(content, compactionSummaryPrefix)
	summary = strings.TrimSuffix(summary, compactionSummarySuffix)
	return strings.TrimSpace(summary), messages[1:]
}

func findMessageCutPoint(messages []glm.Message, keepRecentTokens int) int {
	if len(messages) <= 2 {
		return 0
	}
	accumulated := 0
	cut := 0
	for i := len(messages) - 1; i >= 0; i-- {
		accumulated += estimateMessageTokens(messages[i])
		if accumulated >= keepRecentTokens {
			cut = i
			break
		}
	}
	if cut == 0 {
		return 0
	}
	for cut > 0 && messages[cut].Role != "user" {
		cut--
	}
	if cut == 0 {
		return 0
	}
	return cut
}

func estimateMessagesTokens(messages []glm.Message) int {
	total := 0
	for _, msg := range messages {
		total += estimateMessageTokens(msg)
	}
	return total
}

func estimateMessageTokens(msg glm.Message) int {
	chars := 0
	switch c := msg.Content.(type) {
	case string:
		chars += len(c)
	case []any:
		for _, part := range c {
			if m, ok := part.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					chars += len(text)
				}
			}
		}
	default:
		if c != nil {
			chars += len(fmt.Sprint(c))
		}
	}
	if msg.Role == "assistant" {
		for _, call := range msg.ToolCalls {
			chars += len(call.Function.Name) + len(call.Function.Arguments)
		}
	}
	chars += len(msg.EncryptedContent)
	return int(math.Ceil(float64(chars) / 4))
}

func serializeMessagesForSummary(messages []glm.Message) string {
	var parts []string
	for _, msg := range messages {
		switch msg.Role {
		case "system":
			continue
		case "user":
			if text := messageText(msg); text != "" {
				parts = append(parts, "[User]: "+text)
			}
		case "assistant":
			if text := messageText(msg); text != "" {
				parts = append(parts, "[Assistant]: "+text)
			}
			if len(msg.ToolCalls) > 0 {
				var calls []string
				for _, call := range msg.ToolCalls {
					calls = append(calls, call.Function.Name+"("+formatToolArgs(call.Function.Arguments)+")")
				}
				parts = append(parts, "[Assistant tool calls]: "+strings.Join(calls, "; "))
			}
		case "tool":
			if text := messageText(msg); text != "" {
				parts = append(parts, "[Tool result]: "+truncateForSummary(text, toolResultMaxChars))
			}
		default:
			if text := messageText(msg); text != "" {
				parts = append(parts, "["+msg.Role+"]: "+text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func messageText(msg glm.Message) string {
	switch c := msg.Content.(type) {
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
	case nil:
		return ""
	default:
		return fmt.Sprint(c)
	}
}

func formatToolArgs(raw string) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return raw
	}
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", key, safeJSONString(args[key])))
	}
	return strings.Join(parts, ", ")
}

func safeJSONString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[unserializable]"
	}
	return string(b)
}

func truncateForSummary(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}
	return text[:maxChars] + fmt.Sprintf("\n\n[... %d more characters truncated]", len(text)-maxChars)
}

func localCompactionSummary(messages []glm.Message, previousSummary, reason string) string {
	var b strings.Builder
	if previousSummary != "" {
		b.WriteString(previousSummary)
		b.WriteString("\n\n")
	}
	b.WriteString("## Goal\n")
	b.WriteString("- Continue the compacted AgentBridge agent session.\n\n")
	b.WriteString("## Progress\n")
	if reason != "" {
		b.WriteString("- Compaction reason: " + reason + "\n")
	}
	b.WriteString(fmt.Sprintf("- Compacted %d previous messages.\n\n", len(messages)))
	b.WriteString("## Critical Context\n")
	snippet := truncateForSummary(serializeMessagesForSummary(messages), 4000)
	if snippet == "" {
		b.WriteString("- (none)\n")
	} else {
		b.WriteString(snippet)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func formatCompactionFileOps(messages []glm.Message) string {
	read := map[string]bool{}
	modified := map[string]bool{}
	for _, msg := range messages {
		if msg.Role != "assistant" {
			continue
		}
		for _, call := range msg.ToolCalls {
			var args map[string]any
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				continue
			}
			path, _ := args["path"].(string)
			if path == "" {
				continue
			}
			switch call.Function.Name {
			case "read_file", "read":
				read[path] = true
			case "write_file", "edit", "write":
				modified[path] = true
			}
		}
	}
	for path := range modified {
		delete(read, path)
	}
	readList := sortedKeys(read)
	modifiedList := sortedKeys(modified)
	if len(readList) == 0 && len(modifiedList) == 0 {
		return ""
	}
	var sections []string
	if len(readList) > 0 {
		sections = append(sections, "<read-files>\n"+strings.Join(readList, "\n")+"\n</read-files>")
	}
	if len(modifiedList) > 0 {
		sections = append(sections, "<modified-files>\n"+strings.Join(modifiedList, "\n")+"\n</modified-files>")
	}
	return "\n\n" + strings.Join(sections, "\n\n")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

const summarizationSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI coding assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

const summarizationPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const updateSummarizationPrompt = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work - update based on progress]

### Blocked
- [Current blockers - remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve all previous, add new)

## Next Steps
1. [Update based on current state]

## Critical Context
- [Preserve important context, add new if needed]

Keep each section concise. Preserve exact file paths, function names, and error messages.`
