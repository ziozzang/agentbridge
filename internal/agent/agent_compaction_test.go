package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ziozzang/agentbridge/internal/acp"
	contextcompact "github.com/ziozzang/agentbridge/internal/compaction"
	"github.com/ziozzang/agentbridge/internal/provider"
	"github.com/ziozzang/agentbridge/internal/provider/glm"
)

// Each call to the chat completions endpoint records the entire request body
// and returns the response in `responses[i]` (where i is the 0-based call
// number). Lets tests assert what the SECOND streamChat call saw — which is
// where tool-result feedback shows up.
type recordingServer struct {
	calls     atomic.Int32
	requests  [][]byte
	responses []string
}

func (s *recordingServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		idx := int(s.calls.Add(1)) - 1
		s.requests = append(s.requests, body)
		resp := ""
		if idx < len(s.responses) {
			resp = s.responses[idx]
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(resp))
		w.Write([]byte("data: [DONE]\n\n"))
	}
}

// TS test: "tool call result is fed back into the next streamChat call".
func TestToolResultFedBackIntoNextStreamChat(t *testing.T) {
	srv := &recordingServer{
		responses: []string{
			// 1st call: model emits a run_command tool call.
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"tc1","type":"function","function":{"name":"run_command","arguments":"{\"command\":\"echo HELLO\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n",
			// 2nd call: model finishes after seeing the tool result.
			`data: {"choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}` + "\n\n",
		},
	}
	httpSrv := httptest.NewServer(srv.handler())
	defer httpSrv.Close()
	r := &recorderConn{}
	a := newAgentWith(t, r, httpSrv)
	ns, _ := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: t.TempDir()})
	a.sessions[ns.SessionID].Mode = "bypass_permissions"
	resp, err := a.Prompt(context.Background(), acp.PromptParams{
		SessionID: ns.SessionID,
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "run echo"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("stop = %s", resp.StopReason)
	}
	if int(srv.calls.Load()) < 2 {
		t.Fatalf("expected at least 2 streamChat calls, got %d", srv.calls.Load())
	}
	// The 2nd request body must include the tool result (HELLO from echo).
	body := string(srv.requests[1])
	if !strings.Contains(body, "HELLO") {
		t.Errorf("tool result not fed back into 2nd request body:\n%s", body)
	}
	if !strings.Contains(body, `"role":"tool"`) {
		t.Errorf("tool role missing in 2nd request body:\n%s", body)
	}
	if !strings.Contains(body, `"tool_call_id":"tc1"`) {
		t.Errorf("tool_call_id missing in 2nd request body:\n%s", body)
	}
}

// TS test: "prompt performs emergency compaction and retries on 1261 error".
func TestPromptEmergencyCompactsOn1261AndRetries(t *testing.T) {
	srv := &recordingServer{
		responses: []string{
			// 1st: 1261 context-overflow error.
			// The server returns 200 with an SSE-shaped error payload that
			// the GLM client surfaces as *APIError. We approximate by
			// sending an HTTP 400 with the canonical body.
		},
	}
	// Custom handler: 1st call returns 400 with 1261, 2nd call returns a
	// normal completion stream.
	calls := atomic.Int32{}
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1)) - 1
		if n == 0 {
			body, _ := io.ReadAll(r.Body)
			srv.requests = append(srv.requests, body)
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"code":"1261","message":"context overflow"}}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		srv.requests = append(srv.requests, body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"after compaction"},"finish_reason":"stop"}]}` + "\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer httpSrv.Close()
	r := &recorderConn{}
	a := newAgentWith(t, r, httpSrv)
	ns, _ := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: t.TempDir()})
	resp, err := a.Prompt(context.Background(), acp.PromptParams{
		SessionID: ns.SessionID,
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "long context"}},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("stop = %s", resp.StopReason)
	}
	if int(calls.Load()) != 2 {
		t.Errorf("expected exactly 2 calls (1 fail + 1 retry), got %d", calls.Load())
	}
}

// TS test: "prompt fails fast when context overflow persists after emergency compaction".
func TestPromptFailsFastWhenContextOverflowPersists(t *testing.T) {
	calls := atomic.Int32{}
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(400)
		w.Write([]byte(`{"error":{"code":"1261","message":"context overflow"}}`))
	}))
	defer httpSrv.Close()
	r := &recorderConn{}
	a := newAgentWith(t, r, httpSrv)
	ns, _ := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: t.TempDir()})
	_, err := a.Prompt(context.Background(), acp.PromptParams{
		SessionID: ns.SessionID,
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error after persistent overflow")
	}
	// We should not have looped indefinitely: exactly 2 calls (the original
	// failure plus one emergency-compacted retry that also failed).
	if int(calls.Load()) != 2 {
		t.Errorf("expected exactly 2 calls (1 + 1 retry), got %d", calls.Load())
	}
}

func TestSerializeMessagesForSummaryIncludesToolCallsAndTruncatesResults(t *testing.T) {
	longResult := strings.Repeat("x", toolResultMaxChars+25)
	got := serializeMessagesForSummary([]glm.Message{
		{Role: "user", Content: "inspect files"},
		{
			Role:    "assistant",
			Content: "I will inspect.",
			ToolCalls: []glm.ToolCallMsg{{
				ID:   "tc1",
				Type: "function",
				Function: glm.ToolCallMsgFunction{
					Name:      "read_file",
					Arguments: `{"path":"/tmp/a.txt","limit":10}`,
				},
			}},
		},
		{Role: "tool", ToolCallID: "tc1", Content: longResult},
	})
	for _, want := range []string{
		"[User]: inspect files",
		"[Assistant]: I will inspect.",
		"[Assistant tool calls]: read_file(",
		`path="/tmp/a.txt"`,
		"[Tool result]: " + strings.Repeat("x", 32),
		"[... 25 more characters truncated]",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary serialization missing %q:\n%s", want, got)
		}
	}
}

func TestFormatCompactionFileOpsPreservesReadAndModifiedFiles(t *testing.T) {
	got := formatCompactionFileOps([]glm.Message{
		{
			Role: "assistant",
			ToolCalls: []glm.ToolCallMsg{{
				Function: glm.ToolCallMsgFunction{Name: "read_file", Arguments: `{"path":"/repo/README.md"}`},
			}},
		},
		{
			Role: "assistant",
			ToolCalls: []glm.ToolCallMsg{{
				Function: glm.ToolCallMsgFunction{Name: "write_file", Arguments: `{"path":"/repo/main.go"}`},
			}},
		},
	})
	if !strings.Contains(got, "<read-files>\n/repo/README.md\n</read-files>") {
		t.Fatalf("read files not preserved:\n%s", got)
	}
	if !strings.Contains(got, "<modified-files>\n/repo/main.go\n</modified-files>") {
		t.Fatalf("modified files not preserved:\n%s", got)
	}
}

func TestCompactPromptMessagesUsesStructuredSummaryAndRecentHistory(t *testing.T) {
	var requestBodies []string
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestBodies = append(requestBodies, string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"## Goal\nSummarized goal"},"finish_reason":"stop"}]}` + "\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer httpSrv.Close()

	a := newAgentWith(t, &recorderConn{}, httpSrv)
	messages := []glm.Message{{Role: "system", Content: "system"}}
	for i := 0; i < 12; i++ {
		messages = append(messages,
			glm.Message{Role: "user", Content: "old user " + strconv.Itoa(i) + " " + strings.Repeat("a", 2000)},
			glm.Message{Role: "assistant", Content: "old assistant " + strconv.Itoa(i) + " " + strings.Repeat("b", 2000)},
		)
	}
	messages = append(messages, glm.Message{Role: "user", Content: "recent request"})

	result := a.compactPromptMessages(context.Background(), messages, "glm-test", nil, contextcompact.DefaultSettings(), 12000, "test compaction")
	if !result.Compacted {
		t.Fatal("expected compaction")
	}
	if len(requestBodies) != 1 {
		t.Fatalf("expected one summarization call, got %d", len(requestBodies))
	}
	if len(result.Messages) < 3 {
		t.Fatalf("expected system, summary, recent messages; got %d", len(result.Messages))
	}
	if result.Messages[0].Role != "system" {
		t.Fatalf("first message should remain system, got %s", result.Messages[0].Role)
	}
	summaryText, _ := result.Messages[1].Content.(string)
	if !strings.Contains(summaryText, compactionSummaryPrefix) || !strings.Contains(summaryText, "Summarized goal") {
		t.Fatalf("missing structured summary wrapper:\n%s", summaryText)
	}
	if got := result.Messages[len(result.Messages)-1].Content; got != "recent request" {
		t.Fatalf("recent history not preserved, last content=%v", got)
	}
}

type nativeCompactorProvider struct {
	provider.Provider
	gotOptions provider.CompactOptions
}

func (p *nativeCompactorProvider) CompactConversation(_ context.Context, _ []provider.Message, opts provider.CompactOptions) ([]provider.Message, error) {
	p.gotOptions = opts
	return []provider.Message{
		{Role: "user", Content: "preserved"},
		{Type: "compaction", EncryptedContent: "encrypted-summary"},
	}, nil
}

func TestCompactPromptMessagesPrefersProviderNativeCompaction(t *testing.T) {
	p := &nativeCompactorProvider{}
	a := New(nil)
	a.Provider = p
	messages := []glm.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "old"},
		{Role: "assistant", Content: "answer"},
		{Role: "user", Content: "new"},
	}
	result := a.compactPromptMessages(context.Background(), messages, "gpt-5.5", nil, contextcompact.DefaultSettings(), 1000, "test native")
	if !result.Compacted {
		t.Fatal("expected provider-native compaction")
	}
	if result.Messages[0].Role != "system" || result.Messages[0].Content != "system" {
		t.Fatalf("system message should be prepended to provider replacement: %#v", result.Messages)
	}
	if len(result.Messages) != 3 || result.Messages[2].Type != "compaction" {
		t.Fatalf("provider replacement history not used: %#v", result.Messages)
	}
	if p.gotOptions.Model != "gpt-5.5" || p.gotOptions.Reason != "test native" {
		t.Fatalf("bad native compact options: %#v", p.gotOptions)
	}
}
