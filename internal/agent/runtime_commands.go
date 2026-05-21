package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ziozzang/agentbridge/internal/acp"
	contextcompact "github.com/ziozzang/agentbridge/internal/compaction"
	harnessskills "github.com/ziozzang/agentbridge/internal/harness/skills"
	"github.com/ziozzang/agentbridge/internal/protocol/sessionstore"
	"github.com/ziozzang/agentbridge/internal/protocol/systemprompt"
	"github.com/ziozzang/agentbridge/internal/provider/glm"
)

const maxSkillPromptBytes = 64 * 1024

func (a *Agent) handleRuntimeCommand(ctx context.Context, s *sessionState, p acp.PromptParams) (bool, acp.PromptResponse, error) {
	text, ok := textOnlyPrompt(p.Prompt)
	if !ok {
		return false, acp.PromptResponse{}, nil
	}
	line := strings.TrimSpace(text)
	if !strings.HasPrefix(line, "/skill") && !strings.HasPrefix(line, "/btw") &&
		!strings.HasPrefix(line, "/save") && !strings.HasPrefix(line, "/checkpoint") &&
		!strings.HasPrefix(line, "/compact") && !strings.HasPrefix(line, "/context") &&
		!strings.HasPrefix(line, "/subagent") {
		return false, acp.PromptResponse{}, nil
	}

	s.promptMu.Lock()
	defer s.promptMu.Unlock()

	var out string
	var err error
	switch {
	case line == "/skill" || strings.HasPrefix(line, "/skill "):
		out, err = a.handleSkillCommand(s, strings.Fields(line))
	case line == "/btw" || strings.HasPrefix(line, "/btw ") || line == "/checkpoint" || strings.HasPrefix(line, "/checkpoint "):
		out, err = a.handleBTWCommand(s, strings.Fields(line))
	case line == "/save" || strings.HasPrefix(line, "/save "):
		fields := append([]string{"/btw", "mark"}, strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "/save")))...)
		out, err = a.handleBTWCommand(s, fields)
	case line == "/compact" || strings.HasPrefix(line, "/compact "):
		out, err = a.handleCompactCommand(ctx, s, strings.Fields(line))
	case line == "/context" || strings.HasPrefix(line, "/context "):
		out, err = a.handleContextCommand(s)
	case line == "/subagent" || strings.HasPrefix(line, "/subagent "):
		out, err = a.handleSubagentCommand(ctx, s, strings.TrimSpace(strings.TrimPrefix(line, "/subagent")))
	default:
		return false, acp.PromptResponse{}, nil
	}
	if err != nil {
		out = "error: " + err.Error()
	}
	if out != "" {
		a.notifyText(s.ID, out)
	}
	return true, acp.PromptResponse{StopReason: "end_turn", UserMessageID: p.MessageID}, nil
}

func (a *Agent) handleSubagentCommand(ctx context.Context, s *sessionState, args string) (string, error) {
	if args == "" {
		return "", fmt.Errorf("usage: /subagent [--model MODEL] TASK")
	}
	if subagentDepth(ctx) >= maxSubagentDepth {
		return "", fmt.Errorf("subagent recursion depth exceeded")
	}
	fields := strings.Fields(args)
	model := ""
	if len(fields) >= 3 && fields[0] == "--model" {
		model = fields[1]
		args = strings.TrimSpace(strings.TrimPrefix(args, fields[0]))
		args = strings.TrimSpace(strings.TrimPrefix(args, fields[1]))
	}
	text, err := a.runSubagent(ctx, s, subagentOptions{Model: model, Task: args})
	if err != nil {
		return "", err
	}
	if model == "" {
		model = s.Model
	}
	if model == "" {
		model = a.defaultModel()
	}
	return fmt.Sprintf("subagent result (%s):\n%s", model, text), nil
}

func (a *Agent) handleContextCommand(s *sessionState) (string, error) {
	snap := a.contextSnapshot(s)
	return fmt.Sprintf("context: tokens=%d window=%d used=%.1f%% threshold=%d target=%d messages=%d checkpoints=%d cache_epoch=%d compaction_enabled=%v",
		snap["tokens"], snap["window"], snap["used_percent"], snap["threshold"], snap["target"], snap["messages"], snap["checkpoints"], snap["cache_epoch"], snap["compaction_enabled"]), nil
}

func (a *Agent) contextSnapshot(s *sessionState) map[string]any {
	settings := loadCompactionSettings()
	tools := s.tools
	if len(tools) == 0 {
		tools = a.availableTools()
	}
	tools = a.profileTools(s.Model, tools)
	toolNames := make([]string, len(tools))
	for i, t := range tools {
		toolNames[i] = t.Function.Name
	}
	system := systemprompt.Build(systemprompt.Input{
		Cwd: s.Cwd, Tools: toolNames,
		AgentsMD: systemprompt.LoadProjectContext(s.Cwd),
		Profile:  a.profilePrompt(s.Model),
	})
	if skillPrompt := a.activeSkillPrompt(s); skillPrompt != "" {
		system += "\n\n" + skillPrompt
	}
	messages := append([]glm.Message{{Role: "system", Content: system}}, s.Messages...)
	tokens := contextcompact.EstimateTokens(messages)
	window := a.contextWindow(a.effectiveModel(s.Model))
	threshold := settings.ProactiveThreshold(window)
	target := settings.TargetTokens(window)
	pct := 0.0
	if window > 0 {
		pct = float64(tokens) * 100 / float64(window)
	}
	left := 0.0
	if window > 0 {
		left = 100 - pct
		if left < 0 {
			left = 0
		}
	}
	return map[string]any{
		"tokens":             tokens,
		"window":             window,
		"used_percent":       pct,
		"left_percent":       left,
		"threshold":          threshold,
		"target":             target,
		"messages":           len(s.Messages),
		"checkpoints":        len(s.Checkpoints),
		"cache_epoch":        s.CacheEpoch,
		"compaction_enabled": settings.Enabled,
	}
}

func (a *Agent) handleCompactCommand(ctx context.Context, s *sessionState, fields []string) (string, error) {
	if len(s.Messages) <= 3 {
		return "not enough history to compact", nil
	}
	settings := loadCompactionSettings()
	if !settings.Enabled {
		settings.Enabled = true
	}
	targetTokens := 0
	if len(fields) > 1 {
		n, err := strconv.Atoi(fields[1])
		if err != nil || n <= 0 {
			return "", fmt.Errorf("usage: /compact [target_tokens]")
		}
		targetTokens = n
	}
	if targetTokens == 0 {
		targetTokens = settings.TargetTokens(a.contextWindow(a.effectiveModel(s.Model)))
	}

	tools := s.tools
	if len(tools) == 0 {
		tools = a.availableTools()
	}
	tools = a.profileTools(s.Model, tools)
	toolNames := make([]string, len(tools))
	for i, t := range tools {
		toolNames[i] = t.Function.Name
	}
	system := systemprompt.Build(systemprompt.Input{
		Cwd: s.Cwd, Tools: toolNames,
		AgentsMD: systemprompt.LoadProjectContext(s.Cwd),
		Profile:  a.profilePrompt(s.Model),
	})
	if skillPrompt := a.activeSkillPrompt(s); skillPrompt != "" {
		system += "\n\n" + skillPrompt
	}
	messages := append([]glm.Message{{Role: "system", Content: system}}, s.Messages...)
	before := contextcompact.EstimateTokens(messages)
	result := a.compactPromptMessages(ctx, messages, a.effectiveModel(s.Model), tools, settings, targetTokens, "manual runtime compaction")
	if !result.Compacted {
		return fmt.Sprintf("compaction skipped: tokens=%d target=%d", before, targetTokens), nil
	}
	if len(result.Messages) > 0 {
		s.Messages = append([]glm.Message(nil), result.Messages[1:]...)
	}
	s.CacheEpoch++
	s.UpdatedAt = nowRFC3339()
	_ = a.persistSession(s)
	after := contextcompact.EstimateTokens(result.Messages)
	return fmt.Sprintf("compacted: tokens %d -> %d; messages=%d; cache_epoch=%d", before, after, len(s.Messages), s.CacheEpoch), nil
}

func (a *Agent) handleSkillCommand(s *sessionState, fields []string) (string, error) {
	if len(fields) == 1 || fields[1] == "status" {
		if len(s.ActiveSkills) == 0 {
			return "active skills: none", nil
		}
		var b strings.Builder
		b.WriteString("active skills:\n")
		for _, sk := range s.ActiveSkills {
			fmt.Fprintf(&b, "- %s (%s)\n", sk.Name, shortHash(sk.Hash))
		}
		return strings.TrimRight(b.String(), "\n"), nil
	}
	if fields[1] == "list" {
		list, err := harnessskills.List(s.Cwd)
		if err != nil {
			return "", err
		}
		if len(list) == 0 {
			return "skills: none", nil
		}
		var b strings.Builder
		b.WriteString("skills:\n")
		for _, sk := range list {
			fmt.Fprintf(&b, "- %s (%s)\n", sk.Name, filepath.Clean(sk.Path))
		}
		return strings.TrimRight(b.String(), "\n"), nil
	}
	if fields[1] == "clear" {
		if len(fields) == 2 {
			s.ActiveSkills = nil
			s.UpdatedAt = nowRFC3339()
			_ = a.persistSession(s)
			return "cleared all active skills", nil
		}
		name := fields[2]
		kept := s.ActiveSkills[:0]
		removed := false
		for _, sk := range s.ActiveSkills {
			if sk.Name == name {
				removed = true
				continue
			}
			kept = append(kept, sk)
		}
		s.ActiveSkills = kept
		s.UpdatedAt = nowRFC3339()
		_ = a.persistSession(s)
		if !removed {
			return "skill not active: " + name, nil
		}
		return "cleared skill: " + name, nil
	}

	skill, err := harnessskills.Load(s.Cwd, fields[1])
	if err != nil {
		return "", err
	}
	active := sessionstore.ActiveSkill{
		Name:       skill.Name,
		Path:       skill.Path,
		Hash:       skill.Hash,
		InjectedAt: nowRFC3339(),
	}
	replaced := false
	for i := range s.ActiveSkills {
		if s.ActiveSkills[i].Name == active.Name {
			s.ActiveSkills[i] = active
			replaced = true
			break
		}
	}
	if !replaced {
		s.ActiveSkills = append(s.ActiveSkills, active)
	}
	s.UpdatedAt = nowRFC3339()
	_ = a.persistSession(s)
	return fmt.Sprintf("activated skill: %s (%s)", active.Name, shortHash(active.Hash)), nil
}

func (a *Agent) handleBTWCommand(s *sessionState, fields []string) (string, error) {
	if len(fields) == 1 || fields[1] == "status" {
		return fmt.Sprintf("messages=%d checkpoints=%d cache_epoch=%d", len(s.Messages), len(s.Checkpoints), s.CacheEpoch), nil
	}
	switch fields[1] {
	case "mark":
		if len(fields) < 3 {
			return "", fmt.Errorf("usage: /btw mark NAME")
		}
		name := fields[2]
		cp := sessionstore.Checkpoint{
			ID:           "cp_" + randomHex(4),
			Name:         name,
			MessageIndex: len(s.Messages),
			CreatedAt:    nowRFC3339(),
			Model:        s.Model,
			Mode:         s.Mode,
			CacheEpoch:   s.CacheEpoch,
			Skills:       append([]sessionstore.ActiveSkill(nil), s.ActiveSkills...),
		}
		s.Checkpoints = append(s.Checkpoints, cp)
		s.UpdatedAt = nowRFC3339()
		_ = a.persistSession(s)
		return fmt.Sprintf("checkpoint marked: %s %s at message %d", cp.ID, cp.Name, cp.MessageIndex), nil
	case "list":
		if len(s.Checkpoints) == 0 {
			return "checkpoints: none", nil
		}
		var b strings.Builder
		b.WriteString("checkpoints:\n")
		for _, cp := range s.Checkpoints {
			fmt.Fprintf(&b, "- %s %s messages=%d cache_epoch=%d\n", cp.ID, cp.Name, cp.MessageIndex, cp.CacheEpoch)
		}
		return strings.TrimRight(b.String(), "\n"), nil
	case "back":
		if len(fields) < 3 {
			return "", fmt.Errorf("usage: /btw back NAME|ID")
		}
		cp, ok := findCheckpoint(s.Checkpoints, fields[2])
		if !ok {
			return "", fmt.Errorf("checkpoint not found: %s", fields[2])
		}
		if cp.MessageIndex < 0 || cp.MessageIndex > len(s.Messages) {
			return "", fmt.Errorf("checkpoint has invalid message index: %d", cp.MessageIndex)
		}
		s.Messages = append(s.Messages[:0:0], s.Messages[:cp.MessageIndex]...)
		s.ActiveSkills = append([]sessionstore.ActiveSkill(nil), cp.Skills...)
		s.CacheEpoch++
		s.UpdatedAt = nowRFC3339()
		_ = a.persistSession(s)
		return fmt.Sprintf("rolled back to checkpoint: %s %s; cache_epoch=%d", cp.ID, cp.Name, s.CacheEpoch), nil
	default:
		return "", fmt.Errorf("usage: /btw status|mark|list|back")
	}
}

func (a *Agent) activeSkillPrompt(s *sessionState) string {
	if len(s.ActiveSkills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("The following runtime skills are active for this session. Follow them when relevant.\n")
	used := 0
	for _, active := range s.ActiveSkills {
		var sk harnessskills.Skill
		var err error
		if active.Path != "" {
			sk, err = harnessskills.LoadPath(active.Path, active.Name)
		} else {
			sk, err = harnessskills.Load(s.Cwd, active.Name)
		}
		if err != nil {
			fmt.Fprintf(&b, "\n<skill name=%q missing=%q></skill>\n", active.Name, err.Error())
			continue
		}
		body := sk.Body
		if used+len(body) > maxSkillPromptBytes {
			remaining := maxSkillPromptBytes - used
			if remaining < 0 {
				remaining = 0
			}
			body = body[:remaining]
		}
		fmt.Fprintf(&b, "\n<skill name=%q hash=%q>\n%s\n</skill>\n", sk.Name, sk.Hash, strings.TrimSpace(body))
		used += len(body)
		if used >= maxSkillPromptBytes {
			break
		}
	}
	return b.String()
}

func (a *Agent) persistSession(s *sessionState) error {
	return a.Store.Save(sessionstore.PersistedSession{
		SessionID:    s.ID,
		Cwd:          s.Cwd,
		Messages:     s.Messages,
		Title:        s.Title,
		UpdatedAt:    s.UpdatedAt,
		Model:        s.Model,
		Mode:         s.Mode,
		Checkpoints:  append([]sessionstore.Checkpoint(nil), s.Checkpoints...),
		ActiveSkills: append([]sessionstore.ActiveSkill(nil), s.ActiveSkills...),
		CacheEpoch:   s.CacheEpoch,
	})
}

func (a *Agent) notifyText(sessionID, text string) {
	a.notifyUpdate(sessionID, map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": text},
	})
}

func textOnlyPrompt(blocks []acp.ContentBlock) (string, bool) {
	if len(blocks) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Type != "text" {
			return "", false
		}
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n"), true
}

func findCheckpoint(checkpoints []sessionstore.Checkpoint, key string) (sessionstore.Checkpoint, bool) {
	for i := len(checkpoints) - 1; i >= 0; i-- {
		if checkpoints[i].ID == key || checkpoints[i].Name == key {
			return checkpoints[i], true
		}
	}
	return sessionstore.Checkpoint{}, false
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", nowUnixNano())
	}
	return hex.EncodeToString(buf)
}

func shortHash(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

func nowUnixNano() int64 {
	return time.Now().UTC().UnixNano()
}
