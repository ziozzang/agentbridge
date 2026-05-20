package compaction

import (
	"strings"
	"testing"

	"github.com/ziozzang/agentbridge/internal/provider"
)

func TestSettingsFromConfigNormalizesPercentages(t *testing.T) {
	threshold := 75.0
	target := 60.0
	prune := false
	got := SettingsFromConfig(RuntimeConfig{
		ThresholdPct:  &threshold,
		TargetPct:     &target,
		PruneFallback: &prune,
		PreserveTurns: 3,
	})
	if got.ProactiveThresholdPct != 0.75 || got.TargetPct != 0.60 {
		t.Fatalf("percentages not normalized: %#v", got)
	}
	if got.PruneFallbackEnabled {
		t.Fatalf("prune fallback should be disabled: %#v", got)
	}
	if got.ProactiveThreshold(1000) != 750 || got.TargetTokens(1000) != 600 {
		t.Fatalf("bad derived token caps: %#v", got)
	}
}

func TestPruneMessagesKeepsSystemAndRecentTurns(t *testing.T) {
	msgs := []provider.Message{{Role: "system", Content: "system"}}
	for i := 0; i < 6; i++ {
		msgs = append(msgs,
			provider.Message{Role: "user", Content: "user " + strings.Repeat("x", 1000)},
			provider.Message{Role: "assistant", Content: "assistant " + strings.Repeat("y", 1000)},
		)
	}
	out := PruneMessages(msgs, 1000, 2)
	if len(out) >= len(msgs) {
		t.Fatalf("expected pruning: before=%d after=%d", len(msgs), len(out))
	}
	if out[0].Role != "system" {
		t.Fatalf("system message not preserved: %#v", out[0])
	}
	if out[len(out)-4].Role != "user" || out[len(out)-2].Role != "user" {
		t.Fatalf("recent turns not preserved: %#v", out)
	}
}
