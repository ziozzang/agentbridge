package glm

import (
	"errors"
	"testing"
)

func TestAPIError_IsContextOverflow(t *testing.T) {
	tests := []struct {
		code any
		want bool
	}{
		{float64(1261), true},
		{1261, true},
		{int64(1261), true},
		{"1261", true},
		{1262, false},
		{"other", false},
		{nil, false},
	}
	for _, tc := range tests {
		e := &APIError{Code: tc.code}
		if got := e.IsContextOverflow(); got != tc.want {
			t.Fatalf("code=%v: want=%v got=%v", tc.code, tc.want, got)
		}
	}
}

func TestParseAPIError_PopulatesCodeAndMessage(t *testing.T) {
	body := []byte(`{"error":{"code":1261,"message":"context overflow"}}`)
	err := parseAPIError(400, body)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if !apiErr.IsContextOverflow() {
		t.Fatalf("expected overflow detection, got code=%v", apiErr.Code)
	}
	if apiErr.HTTPStatus != 400 {
		t.Fatalf("status=%d", apiErr.HTTPStatus)
	}
	if apiErr.Message != "context overflow" {
		t.Fatalf("msg=%q", apiErr.Message)
	}
}

func TestParseAPIError_NonJSONBodyFallsBack(t *testing.T) {
	err := parseAPIError(500, []byte("plain text"))
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.Message != "plain text" {
		t.Fatalf("expected raw body fallback, got %q", apiErr.Message)
	}
}

func TestContextWindow_KnownAndUnknown(t *testing.T) {
	if ContextWindow("glm-4.7") != 200_000 {
		t.Fatalf("expected 200K for glm-4.7")
	}
	if ContextWindow("glm-5.1") != 128_000 {
		t.Fatalf("expected 128K for glm-5.1")
	}
	if ContextWindow("does-not-exist") != 128_000 {
		t.Fatalf("expected 128K fallback for unknown")
	}
}

func TestEstimateTokens_StringContent(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "abcdefgh"}}
	if EstimateTokens(msgs) != 2 {
		t.Fatalf("expected 2 tokens (8/4), got %d", EstimateTokens(msgs))
	}
}

func TestEstimateTokens_ToolCalls(t *testing.T) {
	msgs := []Message{
		{
			Role: "assistant", Content: "ok",
			ToolCalls: []ToolCallMsg{{
				ID: "1", Type: "function",
				Function: ToolCallMsgFunction{Name: "abcd", Arguments: "{}1234"},
			}},
		},
	}
	// 2 + 4 + 6 = 12 chars => 3 tokens
	if got := EstimateTokens(msgs); got != 3 {
		t.Fatalf("expected 3 tokens, got %d", got)
	}
}

func TestCompact_PreservesSystemAndTailTurns(t *testing.T) {
	// Build: system + 15 user turns, each with a long content string.
	long := make([]byte, 4000)
	for i := range long {
		long[i] = 'x'
	}
	msgs := []Message{{Role: "system", Content: "sys"}}
	for i := 0; i < 15; i++ {
		msgs = append(msgs, Message{Role: "user", Content: string(long)})
	}
	out := Compact(msgs, 5000, 10) // very small target → forces eviction
	if out[0].Role != "system" {
		t.Fatalf("system must remain at index 0")
	}
	// Tail must remain (last 10 user turns kept).
	if len(out) < 11 {
		t.Fatalf("expected at least system+10 turns, got %d", len(out))
	}
}

func TestCompact_NoOpWhenUnderTarget(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}
	out := Compact(msgs, 1_000_000, 10)
	if len(out) != len(msgs) {
		t.Fatalf("expected no-op compaction, got %d → %d", len(msgs), len(out))
	}
}
