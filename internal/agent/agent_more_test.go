package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ziozzang/agentbridge/internal/acp"
	"github.com/ziozzang/agentbridge/internal/protocol/sessionstore"
	"github.com/ziozzang/agentbridge/internal/provider"
	"github.com/ziozzang/agentbridge/internal/provider/glm"
)

// recorderConn captures every outbound session/update and permission call so
// tests can assert on the agent's notification stream — substantially richer
// coverage than the I/O-stubbed *acp.Conn used in agent_test.go.
type recorderConn struct {
	mu              sync.Mutex
	updates         []map[string]any
	permissionCalls int
	permissionErr   error
	clientToolCalls int
}

func (r *recorderConn) SendNotification(method string, params any) error {
	if u, ok := params.(acp.SessionUpdateParams); ok {
		r.mu.Lock()
		r.updates = append(r.updates, u.Update)
		r.mu.Unlock()
	}
	return nil
}

func (r *recorderConn) Call(_ context.Context, method string, _ any, result any) error {
	if r.permissionErr != nil {
		return r.permissionErr
	}
	if method == "client/call_tool" {
		r.mu.Lock()
		r.clientToolCalls++
		r.mu.Unlock()
		if out, ok := result.(*struct {
			Output string `json:"output"`
		}); ok {
			out.Output = "Exit code: 0\n\nSTDOUT:\nHELLO\n\nSTDERR:\n(empty)"
		}
		return nil
	}
	if method != "session/request_permission" {
		return errors.New("unexpected call: " + method)
	}
	r.mu.Lock()
	r.permissionCalls++
	r.mu.Unlock()
	resp := result.(*acp.RequestPermissionResponse)
	resp.Outcome = acp.PermissionOutcome{Outcome: "selected", OptionID: "allow"}
	return nil
}

func (r *recorderConn) findUpdate(kind string) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range r.updates {
		if u["sessionUpdate"] == kind {
			return u
		}
	}
	return nil
}

func (r *recorderConn) countUpdates(kind string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, u := range r.updates {
		if u["sessionUpdate"] == kind {
			n++
		}
	}
	return n
}

// streamingServer streams a single chat-completions chunk back to the GLM
// client. Each call returns the canned text/finish_reason combination.
type streamingServer struct {
	calls     atomic.Int32
	responses []string
}

func (s *streamingServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idx := int(s.calls.Add(1)) - 1
		body := ""
		if idx < len(s.responses) {
			body = s.responses[idx]
		} else if len(s.responses) > 0 {
			body = s.responses[len(s.responses)-1]
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(body))
		w.Write([]byte(`data: [DONE]` + "\n\n"))
	}
}

// streamingServerExpecting fails the test if the request body does NOT contain
// expectFragment, otherwise replies with response.
func streamingServerExpecting(t *testing.T, expectFragment, response string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 64*1024)
		n, _ := r.Body.Read(b)
		body := string(b[:n])
		if expectFragment != "" && !strings.Contains(body, expectFragment) {
			t.Errorf("request body missing %q:\n%s", expectFragment, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(response))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
}

func newAgentWith(t *testing.T, conn Notifier, srv *httptest.Server) *Agent {
	t.Helper()
	t.Setenv("Z_AI_API_KEY", "key")
	t.Setenv("ACP_GLM_THINKING", "false")
	a := New(sessionstore.NewIn(t.TempDir()))
	a.GLM = &glm.Client{APIKey: "key", BaseURL: srv.URL, MaxTokens: 64, HTTPClient: srv.Client()}
	a.Conn = conn
	return a
}

type nativeTestProvider struct {
	calls int
}

func (p *nativeTestProvider) Name() string { return "native-test" }
func (p *nativeTestProvider) Kind() string { return "native-test" }
func (p *nativeTestProvider) AvailableModels() []provider.ModelInfo {
	return []provider.ModelInfo{{ModelID: "native-model", Name: "native-model"}}
}
func (p *nativeTestProvider) DefaultModel() string      { return "native-model" }
func (p *nativeTestProvider) ContextWindow(string) int  { return 100000 }
func (p *nativeTestProvider) UsesNativeAgentLoop() bool { return true }
func (p *nativeTestProvider) StreamChat(_ context.Context, messages []provider.Message, _ provider.StreamOptions) (<-chan provider.Chunk, <-chan error) {
	p.calls++
	chunks := make(chan provider.Chunk, 2)
	errs := make(chan error, 1)
	go func() {
		defer close(chunks)
		defer close(errs)
		last := messages[len(messages)-1]
		text := "native:" + strings.TrimSpace(last.Content.(string))
		chunks <- provider.Chunk{Text: text}
		chunks <- provider.Chunk{Done: true, StopReason: "stop"}
		errs <- nil
	}()
	return chunks, errs
}

func TestNativeProviderSessionBypassesLocalHarness(t *testing.T) {
	a := New(sessionstore.NewIn(t.TempDir()))
	conn := &recorderConn{}
	a.Conn = conn
	p := &nativeTestProvider{}
	a.Provider = p

	ns, err := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if ns.Modes == nil || ns.Modes.CurrentModeID != ModeProviderNative {
		t.Fatalf("modes = %#v", ns.Modes)
	}
	resp, err := a.Prompt(context.Background(), acp.PromptParams{
		SessionID: ns.SessionID,
		MessageID: "m1",
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "hello native"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "end_turn" {
		t.Fatalf("stop = %q", resp.StopReason)
	}
	if p.calls != 1 {
		t.Fatalf("provider calls = %d", p.calls)
	}
	got := a.sessions[ns.SessionID]
	if !got.NativeAgent || len(got.Messages) != 2 {
		t.Fatalf("session = %#v", got)
	}
	if text := got.Messages[1].Content.(string); text != "native:hello native" {
		t.Fatalf("assistant text = %q", text)
	}
	if conn.permissionCalls != 0 {
		t.Fatalf("native provider should not request local tool permissions; calls=%d", conn.permissionCalls)
	}
}

// ---------------------------------------------------------------------------
// Initialize
// ---------------------------------------------------------------------------

func TestInitializeNegotiatesLowerProtocolVersion(t *testing.T) {
	a := New(sessionstore.NewIn(t.TempDir()))
	resp, _ := a.Initialize(context.Background(), acp.InitializeParams{ProtocolVersion: 1})
	if resp.ProtocolVersion != 1 {
		t.Errorf("expected negotiated to 1, got %d", resp.ProtocolVersion)
	}
}

func TestInitializeCapsToServerVersionWhenClientHigher(t *testing.T) {
	a := New(sessionstore.NewIn(t.TempDir()))
	resp, _ := a.Initialize(context.Background(), acp.InitializeParams{ProtocolVersion: 99})
	if resp.ProtocolVersion != acp.ProtocolVersion {
		t.Errorf("expected %d, got %d", acp.ProtocolVersion, resp.ProtocolVersion)
	}
}

func TestAvailableToolsIncludeClientLuaWhenAdvertised(t *testing.T) {
	a := New(sessionstore.NewIn(t.TempDir()))
	_, err := a.Initialize(context.Background(), acp.InitializeParams{
		ProtocolVersion: acp.ProtocolVersion,
		ClientCapabilities: map[string]any{
			"clientTools": []map[string]any{{
				"name":        "run_lua",
				"description": "run lua",
				"parameters":  map[string]any{"type": "object"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range a.availableTools() {
		if tool.Function.Name == "client__run_lua" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("client_run_lua not exposed")
	}
	a.clientCapabilities = map[string]any{}
	a.clientTools = nil
	for _, tool := range a.availableTools() {
		if tool.Function.Name == "client__run_lua" {
			t.Fatal("client__run_lua should be hidden without advertised client tool")
		}
	}
}

// ---------------------------------------------------------------------------
// Session mode
// ---------------------------------------------------------------------------

func TestSetSessionModePersistsAndEmitsCurrentModeUpdate(t *testing.T) {
	t.Setenv("Z_AI_API_KEY", "k")
	r := &recorderConn{}
	a := New(sessionstore.NewIn(t.TempDir()))
	a.GLM = &glm.Client{APIKey: "k"}
	a.Conn = r
	ns, _ := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: "/x"})
	if _, err := a.SetSessionMode(context.Background(), acp.SetModeParams{
		SessionID: ns.SessionID, ModeID: "accept_edits",
	}); err != nil {
		t.Fatal(err)
	}
	if a.sessions[ns.SessionID].Mode != "accept_edits" {
		t.Errorf("mode = %s", a.sessions[ns.SessionID].Mode)
	}
	u := r.findUpdate("current_mode_update")
	if u == nil {
		t.Fatal("expected current_mode_update notification")
	}
	if u["currentModeId"] != "accept_edits" {
		t.Errorf("notification carries wrong modeId: %v", u["currentModeId"])
	}
}

func TestSetSessionModeRejectsInvalidId(t *testing.T) {
	t.Setenv("Z_AI_API_KEY", "k")
	a := New(sessionstore.NewIn(t.TempDir()))
	a.GLM = &glm.Client{APIKey: "k"}
	a.Conn = &recorderConn{}
	ns, _ := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: "/x"})
	_, err := a.SetSessionMode(context.Background(), acp.SetModeParams{
		SessionID: ns.SessionID, ModeID: "not_a_real_mode",
	})
	if err == nil {
		t.Fatal("expected invalid mode error")
	}
	if !strings.Contains(err.Error(), "Invalid modeId") {
		t.Errorf("got %v", err)
	}
}

func TestNewSessionReturnsAvailableModesAndModels(t *testing.T) {
	t.Setenv("Z_AI_API_KEY", "k")
	a := New(sessionstore.NewIn(t.TempDir()))
	a.GLM = &glm.Client{APIKey: "k"}
	a.Conn = &recorderConn{}
	ns, _ := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: "/x"})
	if ns.Modes == nil || len(ns.Modes.AvailableModes) != 3 {
		t.Fatalf("expected 3 modes, got %+v", ns.Modes)
	}
	if ns.Modes.CurrentModeID != "default" {
		t.Errorf("expected default mode, got %s", ns.Modes.CurrentModeID)
	}
	if ns.Models == nil || ns.Models.CurrentModelID == "" {
		t.Fatalf("expected current model set, got %+v", ns.Models)
	}
}

// ---------------------------------------------------------------------------
// Stop reasons
// ---------------------------------------------------------------------------

func TestMapStopReason(t *testing.T) {
	cases := map[string]string{
		"":               "end_turn",
		"stop":           "end_turn",
		"tool_calls":     "end_turn",
		"length":         "max_tokens",
		"content_filter": "refusal",
		"weird":          "weird",
	}
	for in, want := range cases {
		if got := mapStopReason(in); got != want {
			t.Errorf("mapStopReason(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPromptMapsLengthToMaxTokens(t *testing.T) {
	srv := streamingServerExpecting(t, "",
		`data: {"choices":[{"index":0,"delta":{"content":"abc"},"finish_reason":"length"}]}`+"\n\n")
	defer srv.Close()
	r := &recorderConn{}
	a := newAgentWith(t, r, srv)
	ns, _ := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: t.TempDir()})
	resp, err := a.Prompt(context.Background(), acp.PromptParams{
		SessionID: ns.SessionID,
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "max_tokens" {
		t.Errorf("stopReason = %s", resp.StopReason)
	}
}

func TestPromptMapsContentFilterToRefusal(t *testing.T) {
	srv := streamingServerExpecting(t, "",
		`data: {"choices":[{"index":0,"delta":{"content":"abc"},"finish_reason":"content_filter"}]}`+"\n\n")
	defer srv.Close()
	r := &recorderConn{}
	a := newAgentWith(t, r, srv)
	ns, _ := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: t.TempDir()})
	resp, _ := a.Prompt(context.Background(), acp.PromptParams{
		SessionID: ns.SessionID,
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "hi"}},
	})
	if resp.StopReason != "refusal" {
		t.Errorf("stopReason = %s", resp.StopReason)
	}
}

// ---------------------------------------------------------------------------
// Title + session_info_update
// ---------------------------------------------------------------------------

func TestPromptDerivesTitleFromFirstUserMessage(t *testing.T) {
	srv := streamingServerExpecting(t, "",
		`data: {"choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":"stop"}]}`+"\n\n")
	defer srv.Close()
	r := &recorderConn{}
	a := newAgentWith(t, r, srv)
	ns, _ := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: t.TempDir()})
	_, _ = a.Prompt(context.Background(), acp.PromptParams{
		SessionID: ns.SessionID,
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "Refactor the executor module"}},
	})
	infoUpdate := r.findUpdate("session_info_update")
	if infoUpdate == nil {
		t.Fatal("missing session_info_update")
	}
	title, _ := infoUpdate["title"].(string)
	if !strings.Contains(title, "Refactor the executor module") {
		t.Errorf("title = %q", title)
	}
	if ctx, ok := infoUpdate["context"].(map[string]any); !ok || ctx["window"] == nil {
		t.Fatalf("missing context snapshot: %#v", infoUpdate["context"])
	}
}

func TestSetSessionModelEmitsSessionInfoUpdate(t *testing.T) {
	t.Setenv("Z_AI_API_KEY", "k")
	r := &recorderConn{}
	a := New(sessionstore.NewIn(t.TempDir()))
	a.GLM = &glm.Client{APIKey: "k"}
	a.Conn = r
	ns, _ := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: t.TempDir()})
	_, _ = a.SetSessionModel(context.Background(), acp.SetModelParams{
		SessionID: ns.SessionID, ModelID: "glm-4.7",
	})
	if r.findUpdate("session_info_update") == nil {
		t.Error("expected session_info_update notification")
	}
	if ctx, ok := r.findUpdate("session_info_update")["context"].(map[string]any); !ok || ctx["tokens"] == nil {
		t.Fatalf("missing context snapshot")
	}
}

func TestSetSessionModelRejectsUnknownSession(t *testing.T) {
	t.Setenv("Z_AI_API_KEY", "k")
	a := New(sessionstore.NewIn(t.TempDir()))
	a.GLM = &glm.Client{APIKey: "k"}
	a.Conn = &recorderConn{}
	_, err := a.SetSessionModel(context.Background(), acp.SetModelParams{
		SessionID: "no-such", ModelID: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "session not found") {
		t.Errorf("got %v", err)
	}
}

// ---------------------------------------------------------------------------
// LoadSession replay + persistence
// ---------------------------------------------------------------------------

func TestLoadSessionReplaysMessagesAsSessionUpdates(t *testing.T) {
	t.Setenv("Z_AI_API_KEY", "k")
	store := sessionstore.NewIn(t.TempDir())
	a := New(store)
	a.GLM = &glm.Client{APIKey: "k"}
	r := &recorderConn{}
	a.Conn = r
	// Persist a session with two text exchanges.
	id := "abc123"
	_ = store.Save(sessionstore.PersistedSession{
		SessionID: id, Cwd: "/x", Model: "glm-5.1", UpdatedAt: "2024-01-01T00:00:00Z",
		Messages: []glm.Message{
			{Role: "user", Content: "first user line"},
			{Role: "assistant", Content: "first assistant line"},
			{Role: "user", Content: "second user line"},
			{Role: "assistant", Content: "second assistant line"},
		},
	})
	_, err := a.LoadSession(context.Background(), acp.LoadSessionParams{SessionID: id, Cwd: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if r.countUpdates("user_message_chunk") != 2 {
		t.Errorf("expected 2 user replays, got %d", r.countUpdates("user_message_chunk"))
	}
	if r.countUpdates("agent_message_chunk") != 2 {
		t.Errorf("expected 2 assistant replays, got %d", r.countUpdates("agent_message_chunk"))
	}
}

func TestCloseSessionPersistsFinalState(t *testing.T) {
	t.Setenv("Z_AI_API_KEY", "k")
	store := sessionstore.NewIn(t.TempDir())
	a := New(store)
	a.GLM = &glm.Client{APIKey: "k"}
	a.Conn = &recorderConn{}
	ns, _ := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: "/x"})
	a.sessions[ns.SessionID].Messages = []glm.Message{{Role: "user", Content: "hello"}}
	_, _ = a.CloseSession(context.Background(), acp.CloseSessionParams{SessionID: ns.SessionID})
	// In-memory cleared.
	if _, ok := a.sessions[ns.SessionID]; ok {
		t.Error("expected in-memory session cleared")
	}
	persisted, _ := store.Load(ns.SessionID)
	if persisted == nil || len(persisted.Messages) != 1 {
		t.Errorf("expected 1 persisted message, got %+v", persisted)
	}
}

func TestListSessionsMergesPersistedAndInMemory(t *testing.T) {
	t.Setenv("Z_AI_API_KEY", "k")
	store := sessionstore.NewIn(t.TempDir())
	a := New(store)
	a.GLM = &glm.Client{APIKey: "k"}
	a.Conn = &recorderConn{}
	// Persist a closed session.
	_ = store.Save(sessionstore.PersistedSession{
		SessionID: "older", Cwd: "/x", Model: "glm-5.1", UpdatedAt: "2024-01-01T00:00:00Z",
	})
	// Open a fresh in-memory one.
	ns, _ := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: "/x"})
	list, _ := a.ListSessions(context.Background(), acp.ListSessionsParams{Cwd: "/x"})
	if len(list.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(list.Sessions))
	}
	have := map[string]bool{}
	for _, s := range list.Sessions {
		have[s.SessionID] = true
	}
	if !have["older"] || !have[ns.SessionID] {
		t.Errorf("missing sessions: %+v", have)
	}
}

func TestCloseSessionThenLoadCanRestore(t *testing.T) {
	t.Setenv("Z_AI_API_KEY", "k")
	store := sessionstore.NewIn(t.TempDir())
	a := New(store)
	a.GLM = &glm.Client{APIKey: "k"}
	a.Conn = &recorderConn{}
	ns, _ := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: "/x"})
	a.sessions[ns.SessionID].Messages = []glm.Message{{Role: "user", Content: "hi"}}
	_, _ = a.CloseSession(context.Background(), acp.CloseSessionParams{SessionID: ns.SessionID})
	_, err := a.LoadSession(context.Background(), acp.LoadSessionParams{SessionID: ns.SessionID, Cwd: "/x"})
	if err != nil {
		t.Fatalf("load after close: %v", err)
	}
	if len(a.sessions[ns.SessionID].Messages) != 1 {
		t.Errorf("expected 1 message restored, got %d", len(a.sessions[ns.SessionID].Messages))
	}
}

// ---------------------------------------------------------------------------
// max_turn_requests + cancellation
// ---------------------------------------------------------------------------

func TestPromptReturnsMaxTurnRequestsAfterExhaustion(t *testing.T) {
	// Server always emits a client-owned shell tool call, so the loop spins
	// until MaxTurns is hit.
	body := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"tc","type":"function","function":{"name":"client__run_command","arguments":"{\"command\":\"true\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(body))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()
	a := newAgentWith(t, &recorderConn{}, srv)
	a.MaxTurns = 2 // tiny cap so the test finishes quickly
	ns, _ := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: t.TempDir()})
	// Force mode=bypass so we don't need a working permission stub.
	a.sessions[ns.SessionID].Mode = "bypass_permissions"
	resp, err := a.Prompt(context.Background(), acp.PromptParams{
		SessionID: ns.SessionID,
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "loop forever"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "max_turn_requests" {
		t.Errorf("stopReason = %s", resp.StopReason)
	}
}

func TestPromptCancellationReturnsCancelled(t *testing.T) {
	// Slow server: keeps the connection open until ctx cancels.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		<-r.Context().Done()
	}))
	defer srv.Close()
	a := newAgentWith(t, &recorderConn{}, srv)
	ns, _ := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Give the prompt a moment to start, then cancel.
		// (Tight timing in CI; use a short sleep.)
		for i := 0; i < 50; i++ {
			if len(a.sessions) > 0 {
				break
			}
		}
		cancel()
	}()
	resp, _ := a.Prompt(ctx, acp.PromptParams{
		SessionID: ns.SessionID,
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "hi"}},
	})
	if resp.StopReason != "cancelled" {
		t.Errorf("stopReason = %s", resp.StopReason)
	}
}

// ---------------------------------------------------------------------------
// Invalid MaxTurns fallback
// ---------------------------------------------------------------------------

func TestInvalidMaxTurnsFallsBackToDefault(t *testing.T) {
	t.Setenv("Z_AI_API_KEY", "k")
	a := New(sessionstore.NewIn(t.TempDir()))
	a.GLM = &glm.Client{APIKey: "k"}
	a.Conn = &recorderConn{}
	a.MaxTurns = -5 // invalid
	srv := streamingServerExpecting(t, "",
		`data: {"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}`+"\n\n")
	defer srv.Close()
	a.GLM = &glm.Client{APIKey: "k", BaseURL: srv.URL, MaxTokens: 64, HTTPClient: srv.Client()}
	ns, _ := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: t.TempDir()})
	resp, _ := a.Prompt(context.Background(), acp.PromptParams{
		SessionID: ns.SessionID,
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "x"}},
	})
	if resp.StopReason != "end_turn" {
		t.Errorf("invalid MaxTurns should still produce a normal prompt loop; stop=%s", resp.StopReason)
	}
}

// ---------------------------------------------------------------------------
// Auth methods + image disabled
// ---------------------------------------------------------------------------

func TestAuthenticateIsNoOp(t *testing.T) {
	a := New(sessionstore.NewIn(t.TempDir()))
	_, err := a.Authenticate(context.Background(), []byte(`{}`))
	if err != nil {
		t.Error(err)
	}
}

// ---------------------------------------------------------------------------
// Title helper unit tests
// ---------------------------------------------------------------------------

func TestDeriveTitleClipsToEightyChars(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := deriveTitle(long)
	if len(got) > 80 {
		t.Errorf("title length = %d", len(got))
	}
}

func TestDeriveTitleCollapsesWhitespace(t *testing.T) {
	got := deriveTitle("hello\nworld   foo")
	if got != "hello world foo" {
		t.Errorf("title = %q", got)
	}
}
