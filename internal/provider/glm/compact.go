package glm

import (
	"fmt"

	contextcompact "github.com/ziozzang/agentbridge/internal/compaction"
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

// EstimateTokens delegates to the provider-agnostic compaction estimator.
func EstimateTokens(messages []Message) int {
	return contextcompact.EstimateTokens(messages)
}

// Compact delegates to the provider-agnostic pruning fallback.
func Compact(messages []Message, targetTokens, preserveTurns int) []Message {
	return contextcompact.PruneMessages(messages, targetTokens, preserveTurns)
}
