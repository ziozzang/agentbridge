package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ziozzang/glm-acp/internal/acp"
	"github.com/ziozzang/glm-acp/internal/glm"
	"github.com/ziozzang/glm-acp/internal/protocol/sessionstore"
)

// fakeConn satisfies what executor.SessionConn / agent.Conn needs.
type fakeConn struct {
	updates []map[string]any
	calls   []string
}

func (f *fakeConn) SendNotification(_ string, params any) error {
	if u, ok := params.(acp.SessionUpdateParams); ok {
		f.updates = append(f.updates, u.Update)
	}
	return nil
}
func (f *fakeConn) Call(_ context.Context, method string, _ any, _ any) error {
	f.calls = append(f.calls, method)
	return nil
}

func TestInitializeReportsCapabilitiesAndAuth(t *testing.T) {
	a := New(sessionstore.NewIn(t.TempDir()))
	res, err := a.Initialize(context.Background(), acp.InitializeParams{ProtocolVersion: 5})
	if err != nil {
		t.Fatal(err)
	}
	if res.ProtocolVersion != acp.ProtocolVersion {
		t.Errorf("protocol = %d", res.ProtocolVersion)
	}
	if res.AgentInfo.Name != AgentName || res.AgentInfo.Version != Version {
		t.Errorf("info = %+v", res.AgentInfo)
	}
	if !res.AgentCapabilities.LoadSession || !res.AgentCapabilities.PromptCapabilities.Image {
		t.Errorf("capabilities = %+v", res.AgentCapabilities)
	}
	if len(res.AuthMethods) == 0 {
		t.Error("expected auth methods")
	}
}

func TestInitializeImageDisabledByEnv(t *testing.T) {
	a := New(sessionstore.NewIn(t.TempDir()))
	t.Setenv("ACP_GLM_PROMPT_IMAGES", "false")
	res, _ := a.Initialize(context.Background(), acp.InitializeParams{ProtocolVersion: 1})
	if res.AgentCapabilities.PromptCapabilities.Image {
		t.Error("image should be disabled")
	}
	t.Setenv("ACP_GLM_PROMPT_IMAGES", "0")
	res, _ = a.Initialize(context.Background(), acp.InitializeParams{ProtocolVersion: 1})
	if res.AgentCapabilities.PromptCapabilities.Image {
		t.Error("image should be disabled with 0")
	}
}

func TestNewSessionPromptRoundTrip(t *testing.T) {
	t.Setenv("Z_AI_API_KEY", "key")
	t.Setenv("ACP_GLM_THINKING", "false")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"Hello world"},"finish_reason":"stop"}]}` + "\n\n"))
		w.Write([]byte(`data: [DONE]` + "\n\n"))
	}))
	defer srv.Close()

	store := sessionstore.NewIn(t.TempDir())
	a := New(store)
	a.GLM = &glm.Client{APIKey: "key", BaseURL: srv.URL, MaxTokens: 100, HTTPClient: srv.Client()}

	conn := &fakeConn{}
	a.Conn = (*acp.Conn)(nil) // executor will use conn from override below
	// We need to wire a real *acp.Conn whose SendNotification works with fakeConn semantics.
	// Easiest: bypass by using executor SessionConn via a custom Conn-like struct. Instead, we
	// inject Conn directly with a stub that satisfies executor.SessionConn through an adapter.
	a.SetConn(newStubConn(conn))

	ns, err := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if ns.SessionID == "" {
		t.Fatal("no session id")
	}

	resp, err := a.Prompt(context.Background(), acp.PromptParams{
		SessionID: ns.SessionID,
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("stopReason = %s", resp.StopReason)
	}
	// Persistence: session file should exist.
	got, _ := store.Load(ns.SessionID)
	if got == nil || len(got.Messages) < 2 {
		t.Errorf("session not persisted: %+v", got)
	}
}

// stubConn returns a throwaway acp.Conn that swallows IO. We can't easily
// build a real *acp.Conn without a process, but the agent only invokes
// SendNotification / Call on it, both of which are exercised by the executor
// unit tests. We rely on those for tool-side coverage and use this stub for
// the prompt-loop integration test.
func newStubConn(_ *fakeConn) *acp.Conn {
	return acp.NewConn(devNullReader{}, devNullWriter{}, &noopAgent{})
}

type devNullReader struct{}

func (devNullReader) Read(p []byte) (int, error) { return 0, nil }

type devNullWriter struct{}

func (devNullWriter) Write(p []byte) (int, error) { return len(p), nil }

type noopAgent struct{}

func (noopAgent) Initialize(context.Context, acp.InitializeParams) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{}, nil
}
func (noopAgent) Authenticate(context.Context, json.RawMessage) (any, error) { return nil, nil }
func (noopAgent) NewSession(context.Context, acp.NewSessionParams) (acp.NewSessionResponse, error) {
	return acp.NewSessionResponse{}, nil
}
func (noopAgent) LoadSession(context.Context, acp.LoadSessionParams) (acp.LoadSessionResponse, error) {
	return acp.LoadSessionResponse{}, nil
}
func (noopAgent) ForkSession(context.Context, acp.LoadSessionParams) (acp.ForkSessionResponse, error) {
	return acp.ForkSessionResponse{}, nil
}
func (noopAgent) ResumeSession(context.Context, acp.LoadSessionParams) (acp.LoadSessionResponse, error) {
	return acp.LoadSessionResponse{}, nil
}
func (noopAgent) Prompt(context.Context, acp.PromptParams) (acp.PromptResponse, error) {
	return acp.PromptResponse{}, nil
}
func (noopAgent) Cancel(context.Context, acp.CancelParams) {}
func (noopAgent) CloseSession(context.Context, acp.CloseSessionParams) (any, error) {
	return nil, nil
}
func (noopAgent) ListSessions(context.Context, acp.ListSessionsParams) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, nil
}
func (noopAgent) SetSessionMode(context.Context, acp.SetModeParams) (any, error)   { return nil, nil }
func (noopAgent) SetSessionModel(context.Context, acp.SetModelParams) (any, error) { return nil, nil }

func TestSetSessionModel(t *testing.T) {
	t.Setenv("Z_AI_API_KEY", "k")
	a := New(sessionstore.NewIn(t.TempDir()))
	a.GLM = &glm.Client{APIKey: "k"}
	a.SetConn(newStubConn(nil))
	ns, _ := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: t.TempDir()})
	if _, err := a.SetSessionModel(context.Background(), acp.SetModelParams{
		SessionID: ns.SessionID, ModelID: "glm-4.7",
	}); err != nil {
		t.Fatal(err)
	}
	if a.sessions[ns.SessionID].Model != "glm-4.7" {
		t.Errorf("model = %s", a.sessions[ns.SessionID].Model)
	}
}

func TestForkAndListSession(t *testing.T) {
	t.Setenv("Z_AI_API_KEY", "k")
	store := sessionstore.NewIn(t.TempDir())
	a := New(store)
	a.GLM = &glm.Client{APIKey: "k"}
	a.SetConn(newStubConn(nil))
	ns, _ := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: "/x"})
	a.sessions[ns.SessionID].Messages = []glm.Message{{Role: "user", Content: "hi"}}
	_ = store.Save(sessionstore.PersistedSession{
		SessionID: ns.SessionID, Cwd: "/x", Model: "glm-5.1",
		Messages: a.sessions[ns.SessionID].Messages, UpdatedAt: "2024-01-01T00:00:00Z",
	})
	fork, err := a.ForkSession(context.Background(), acp.LoadSessionParams{SessionID: ns.SessionID, Cwd: "/y"})
	if err != nil || fork.SessionID == "" || fork.SessionID == ns.SessionID {
		t.Fatalf("fork = %+v err=%v", fork, err)
	}
	list, _ := a.ListSessions(context.Background(), acp.ListSessionsParams{Cwd: "/x"})
	if len(list.Sessions) != 1 || list.Sessions[0].SessionID != ns.SessionID {
		t.Errorf("list = %+v", list)
	}
}

func TestLoadSessionMissing(t *testing.T) {
	t.Setenv("Z_AI_API_KEY", "k")
	a := New(sessionstore.NewIn(t.TempDir()))
	a.GLM = &glm.Client{APIKey: "k"}
	if _, err := a.LoadSession(context.Background(), acp.LoadSessionParams{SessionID: "nonexistent_x9"}); err == nil {
		t.Error("expected error for missing session")
	}
}
