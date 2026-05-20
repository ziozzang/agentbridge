package glm

import (
	"fmt"
	"math"
)

// ErrContextOverflow is the Z.AI / Zhipu AI business code returned when the
// total prompt (messages + tools) exceeds the model's context window. The
// agent prompt loop reacts to this by performing an emergency compaction and
// retrying once.
const ErrContextOverflow = 1261

// APIError is a typed wrapper around Z.AI HTTP errors so the prompt loop can
// inspect the business code (e.g. detect ErrContextOverflow).
type APIError struct {
	HTTPStatus int    // HTTP status from the server
	Code       any    // business code (number or string) from `error.code`
	Message    string // human-readable message
	RawBody    string // verbatim response body
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Z.AI HTTP %d: code=%v message=%s", e.HTTPStatus, e.Code, e.Message)
}

// IsContextOverflow reports whether the error is the Z.AI 1261 overflow.
func (e *APIError) IsContextOverflow() bool {
	switch v := e.Code.(type) {
	case float64:
		return int(v) == ErrContextOverflow
	case int:
		return v == ErrContextOverflow
	case int64:
		return int(v) == ErrContextOverflow
	case string:
		return v == fmt.Sprint(ErrContextOverflow)
	}
	return false
}

// modelMetadata holds the per-model context windows. Falls back to 128K for
// unrecognised model ids.
var modelMetadata = map[string]int{
	"glm-5.1":     128_000,
	"glm-5-turbo": 128_000,
	"glm-4.7":     200_000,
	"glm-4.5-air": 128_000,
}

// ContextWindow returns the per-model context window (tokens), defaulting to
// 128K for uncatalogued model ids.
func ContextWindow(model string) int {
	if v, ok := modelMetadata[model]; ok {
		return v
	}
	return 128_000
}

// EstimateTokens uses a simple 4-character-per-token rule to approximate the
// total token cost of a list of messages — safe for English text and source
// code where it tends to slightly over-estimate.
func EstimateTokens(messages []Message) int {
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
		}
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				chars += len(tc.Function.Name) + len(tc.Function.Arguments)
			}
		}
	}
	return int(math.Ceil(float64(chars) / 4))
}

// Compact prunes message history toward targetTokens.
//
// Strategy (mirrors TypeScript compactMessages):
//  1. Always keep the system prompt (index 0).
//  2. Group remaining messages into "turns" starting at each user message.
//  3. Always keep the last preserveTurns (default 10) turns to maintain
//     conversation flow.
//  4. Evict candidate turns largest-first until the estimate falls under
//     targetTokens.
func Compact(messages []Message, targetTokens, preserveTurns int) []Message {
	if preserveTurns <= 0 {
		preserveTurns = 10
	}
	if len(messages) <= 1 {
		return messages
	}
	system := messages[0]
	remaining := messages[1:]

	var turns [][]Message
	var current []Message
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
	// Largest-first.
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
	out := []Message{system}
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
