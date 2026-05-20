package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ziozzang/agentbridge/internal/acp"
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
