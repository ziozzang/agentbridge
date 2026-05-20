package compaction

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/ziozzang/agentbridge/internal/provider"
)

type Settings struct {
	Enabled               bool
	NativeEnabled         bool
	SummaryEnabled        bool
	PruneFallbackEnabled  bool
	ProactiveThresholdPct float64
	TargetPct             float64
	OverflowTargetPct     float64
	PreserveTurns         int
	KeepRecentTokens      int
	ReserveTokens         int
}

func DefaultSettings() Settings {
	return Settings{
		Enabled:               true,
		NativeEnabled:         true,
		SummaryEnabled:        true,
		PruneFallbackEnabled:  true,
		ProactiveThresholdPct: 0.90,
		TargetPct:             0.80,
		OverflowTargetPct:     0.70,
		PreserveTurns:         10,
		KeepRecentTokens:      20_000,
		ReserveTokens:         16_384,
	}
}

func SettingsFromEnv(base Settings) Settings {
	out := base
	out.Enabled = envBool("AGENTBRIDGE_COMPACTION_ENABLED", out.Enabled)
	out.NativeEnabled = envBool("AGENTBRIDGE_COMPACTION_NATIVE", out.NativeEnabled)
	out.SummaryEnabled = envBool("AGENTBRIDGE_COMPACTION_SUMMARY", out.SummaryEnabled)
	out.PruneFallbackEnabled = envBool("AGENTBRIDGE_COMPACTION_PRUNE_FALLBACK", out.PruneFallbackEnabled)
	out.ProactiveThresholdPct = envPct("AGENTBRIDGE_COMPACTION_THRESHOLD_PCT", out.ProactiveThresholdPct)
	out.TargetPct = envPct("AGENTBRIDGE_COMPACTION_TARGET_PCT", out.TargetPct)
	out.OverflowTargetPct = envPct("AGENTBRIDGE_COMPACTION_OVERFLOW_TARGET_PCT", out.OverflowTargetPct)
	out.PreserveTurns = envInt("AGENTBRIDGE_COMPACTION_PRESERVE_TURNS", out.PreserveTurns)
	out.KeepRecentTokens = envInt("AGENTBRIDGE_COMPACTION_KEEP_RECENT_TOKENS", out.KeepRecentTokens)
	out.ReserveTokens = envInt("AGENTBRIDGE_COMPACTION_RESERVE_TOKENS", out.ReserveTokens)
	return out.normalized()
}

type RuntimeConfig struct {
	Enabled           *bool
	Native            *bool
	Summary           *bool
	PruneFallback     *bool
	ThresholdPct      *float64
	TargetPct         *float64
	OverflowTargetPct *float64
	PreserveTurns     int
	KeepRecentTokens  int
	ReserveTokens     int
}

func SettingsFromConfig(c RuntimeConfig) Settings {
	out := DefaultSettings()
	if c.Enabled != nil {
		out.Enabled = *c.Enabled
	}
	if c.Native != nil {
		out.NativeEnabled = *c.Native
	}
	if c.Summary != nil {
		out.SummaryEnabled = *c.Summary
	}
	if c.PruneFallback != nil {
		out.PruneFallbackEnabled = *c.PruneFallback
	}
	if c.ThresholdPct != nil {
		out.ProactiveThresholdPct = normalizePct(*c.ThresholdPct)
	}
	if c.TargetPct != nil {
		out.TargetPct = normalizePct(*c.TargetPct)
	}
	if c.OverflowTargetPct != nil {
		out.OverflowTargetPct = normalizePct(*c.OverflowTargetPct)
	}
	if c.PreserveTurns > 0 {
		out.PreserveTurns = c.PreserveTurns
	}
	if c.KeepRecentTokens > 0 {
		out.KeepRecentTokens = c.KeepRecentTokens
	}
	if c.ReserveTokens > 0 {
		out.ReserveTokens = c.ReserveTokens
	}
	return SettingsFromEnv(out)
}

func (s Settings) normalized() Settings {
	if s.ProactiveThresholdPct <= 0 || s.ProactiveThresholdPct > 1 {
		s.ProactiveThresholdPct = 0.90
	}
	if s.TargetPct <= 0 || s.TargetPct > 1 {
		s.TargetPct = 0.80
	}
	if s.OverflowTargetPct <= 0 || s.OverflowTargetPct > 1 {
		s.OverflowTargetPct = 0.70
	}
	if s.PreserveTurns <= 0 {
		s.PreserveTurns = 10
	}
	if s.KeepRecentTokens <= 0 {
		s.KeepRecentTokens = 20_000
	}
	if s.ReserveTokens <= 0 {
		s.ReserveTokens = 16_384
	}
	return s
}

func (s Settings) ProactiveThreshold(window int) int {
	return int(float64(window) * s.ProactiveThresholdPct)
}

func (s Settings) TargetTokens(window int) int {
	return int(float64(window) * s.TargetPct)
}

func (s Settings) OverflowTargetTokens(window int) int {
	return int(float64(window) * s.OverflowTargetPct)
}

func EstimateTokens(messages []provider.Message) int {
	chars := 0
	for _, m := range messages {
		switch c := m.Content.(type) {
		case string:
			chars += len(c)
		case []any:
			for _, part := range c {
				if mp, ok := part.(map[string]any); ok {
					if s, ok := mp["text"].(string); ok {
						chars += len(s)
					}
				}
			}
		default:
			if c != nil {
				chars += len(fmt.Sprint(c))
			}
		}
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				chars += len(tc.Function.Name) + len(tc.Function.Arguments)
			}
		}
		chars += len(m.EncryptedContent)
	}
	return int(math.Ceil(float64(chars) / 4))
}

func PruneMessages(messages []provider.Message, targetTokens, preserveTurns int) []provider.Message {
	if preserveTurns <= 0 {
		preserveTurns = 10
	}
	if len(messages) <= 1 {
		return messages
	}
	system := messages[0]
	remaining := messages[1:]
	var turns [][]provider.Message
	var current []provider.Message
	for _, m := range remaining {
		if m.Role == "user" && len(current) > 0 {
			turns = append(turns, current)
			current = nil
		}
		current = append(current, m)
	}
	if len(current) > 0 {
		turns = append(turns, current)
	}
	if len(turns) <= preserveTurns {
		return messages
	}
	currentEstimate := EstimateTokens(messages)
	if currentEstimate <= targetTokens {
		return messages
	}
	tail := turns[len(turns)-preserveTurns:]
	candidates := make([]struct {
		Idx    int
		Tokens int
	}, len(turns)-preserveTurns)
	for i := range candidates {
		candidates[i].Idx = i
		candidates[i].Tokens = EstimateTokens(turns[i])
	}
	for i := 1; i < len(candidates); i++ {
		for j := i; j > 0 && candidates[j-1].Tokens < candidates[j].Tokens; j-- {
			candidates[j-1], candidates[j] = candidates[j], candidates[j-1]
		}
	}
	evicted := make(map[int]struct{}, len(candidates))
	for _, c := range candidates {
		if currentEstimate <= targetTokens {
			break
		}
		evicted[c.Idx] = struct{}{}
		currentEstimate -= c.Tokens
	}
	out := []provider.Message{system}
	for i := 0; i < len(turns)-preserveTurns; i++ {
		if _, drop := evicted[i]; drop {
			continue
		}
		out = append(out, turns[i]...)
	}
	for _, t := range tail {
		out = append(out, t...)
	}
	return out
}

func envBool(name string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

func envPct(name string, def float64) float64 {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return normalizePct(f)
}

func normalizePct(f float64) float64 {
	if f > 1 {
		return f / 100
	}
	return f
}

func envInt(name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
