package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziozzang/agentbridge/internal/acp"
	"github.com/ziozzang/agentbridge/internal/protocol/sessionstore"
	"github.com/ziozzang/agentbridge/internal/provider"
	"github.com/ziozzang/agentbridge/internal/provider/glm"
)

func TestRuntimeSkillCommandActivatesAndInjects(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".agentbridge", "skills")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "repo.md"), []byte("Always answer with repo skill."), 0o600); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "Always answer with repo skill.") {
			t.Fatalf("request body missing injected skill:\n%s", string(body))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()
	a := newAgentWith(t, &recorderConn{}, srv)
	ns, err := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Prompt(context.Background(), acp.PromptParams{
		SessionID: ns.SessionID,
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "/skill repo"}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(a.sessions[ns.SessionID].ActiveSkills) != 1 {
		t.Fatalf("active skills = %+v", a.sessions[ns.SessionID].ActiveSkills)
	}
	if _, err := a.Prompt(context.Background(), acp.PromptParams{
		SessionID: ns.SessionID,
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "hello"}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeCheckpointCommandsRollback(t *testing.T) {
	a := New(sessionstore.NewIn(t.TempDir()))
	a.GLM = &glm.Client{APIKey: "k"}
	a.Conn = &recorderConn{}
	s := &sessionState{
		ID: "s1", Cwd: t.TempDir(), Model: "m", Mode: ModeDefault,
		Messages: []glm.Message{{Role: "user", Content: "before"}},
	}
	a.sessions["s1"] = s

	handled, _, err := a.handleRuntimeCommand(context.Background(), s, acp.PromptParams{
		SessionID: "s1",
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "/btw mark keep"}},
	})
	if err != nil || !handled {
		t.Fatalf("mark handled=%v err=%v", handled, err)
	}
	s.Messages = append(s.Messages, glm.Message{Role: "assistant", Content: "after"})
	handled, _, err = a.handleRuntimeCommand(context.Background(), s, acp.PromptParams{
		SessionID: "s1",
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "/btw back keep"}},
	})
	if err != nil || !handled {
		t.Fatalf("back handled=%v err=%v", handled, err)
	}
	if len(s.Messages) != 1 || s.CacheEpoch != 1 {
		t.Fatalf("rollback messages=%+v cache_epoch=%d", s.Messages, s.CacheEpoch)
	}
}

func TestCLIStyleSaveAliasMarksCheckpoint(t *testing.T) {
	a := New(sessionstore.NewIn(t.TempDir()))
	a.GLM = &glm.Client{APIKey: "k"}
	a.Conn = &recorderConn{}
	s := &sessionState{ID: "s1", Cwd: t.TempDir(), Model: "m", Mode: ModeDefault}
	a.sessions["s1"] = s
	handled, _, err := a.handleRuntimeCommand(context.Background(), s, acp.PromptParams{
		SessionID: "s1",
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "/save first"}},
	})
	if err != nil || !handled || len(s.Checkpoints) != 1 {
		t.Fatalf("save handled=%v checkpoints=%+v err=%v", handled, s.Checkpoints, err)
	}
	if !strings.Contains(s.Checkpoints[0].Name, "first") {
		t.Fatalf("checkpoint = %+v", s.Checkpoints[0])
	}
}

func TestRuntimeContextCommandReportsUsage(t *testing.T) {
	a := New(sessionstore.NewIn(t.TempDir()))
	a.GLM = &glm.Client{APIKey: "k"}
	a.Conn = &recorderConn{}
	s := &sessionState{
		ID: "s1", Cwd: t.TempDir(), Model: "m", Mode: ModeDefault,
		Messages: []glm.Message{{Role: "user", Content: "hello"}},
	}
	a.sessions["s1"] = s
	handled, _, err := a.handleRuntimeCommand(context.Background(), s, acp.PromptParams{
		SessionID: "s1",
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "/context"}},
	})
	if err != nil || !handled {
		t.Fatalf("context handled=%v err=%v", handled, err)
	}
}

func TestRuntimeCompactCommandSkipsSmallHistory(t *testing.T) {
	a := New(sessionstore.NewIn(t.TempDir()))
	a.GLM = &glm.Client{APIKey: "k"}
	a.Conn = &recorderConn{}
	s := &sessionState{
		ID: "s1", Cwd: t.TempDir(), Model: "m", Mode: ModeDefault,
		Messages: []glm.Message{{Role: "user", Content: "hello"}},
	}
	a.sessions["s1"] = s
	handled, _, err := a.handleRuntimeCommand(context.Background(), s, acp.PromptParams{
		SessionID: "s1",
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "/compact"}},
	})
	if err != nil || !handled {
		t.Fatalf("compact handled=%v err=%v", handled, err)
	}
	if s.CacheEpoch != 0 {
		t.Fatalf("small skipped compaction should not bump cache epoch: %d", s.CacheEpoch)
	}
}

func TestRuntimeCompactCommandRejectsBadTarget(t *testing.T) {
	a := New(sessionstore.NewIn(t.TempDir()))
	a.GLM = &glm.Client{APIKey: "k"}
	a.Conn = &recorderConn{}
	s := &sessionState{
		ID: "s1", Cwd: t.TempDir(), Model: "m", Mode: ModeDefault,
		Messages: []glm.Message{
			{Role: "user", Content: "one"},
			{Role: "assistant", Content: "two"},
			{Role: "user", Content: "three"},
			{Role: "assistant", Content: "four"},
		},
	}
	a.sessions["s1"] = s
	handled, _, err := a.handleRuntimeCommand(context.Background(), s, acp.PromptParams{
		SessionID: "s1",
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "/compact nope"}},
	})
	if err != nil || !handled {
		t.Fatalf("compact handled=%v err=%v", handled, err)
	}
}

func TestRuntimeSubagentCommandUsesProvider(t *testing.T) {
	p := &nativeTestProvider{}
	r := &recorderConn{}
	a := New(sessionstore.NewIn(t.TempDir()))
	a.Provider = p
	a.Conn = r
	s := &sessionState{
		ID: "s1", Cwd: t.TempDir(), Model: "native-model", Mode: ModeProviderNative,
	}
	a.sessions["s1"] = s
	handled, _, err := a.handleRuntimeCommand(context.Background(), s, acp.PromptParams{
		SessionID: "s1",
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "/subagent inspect files"}},
	})
	if err != nil || !handled {
		t.Fatalf("subagent handled=%v err=%v", handled, err)
	}
	if p.calls != 1 {
		t.Fatalf("provider calls = %d", p.calls)
	}
	if r.countUpdates("tool_call") != 1 {
		t.Fatalf("expected subagent tool_call update, got %d", r.countUpdates("tool_call"))
	}
	if r.countUpdates("tool_call_update") != 1 {
		t.Fatalf("expected subagent completion update, got %d", r.countUpdates("tool_call_update"))
	}
	update := r.findUpdate("tool_call")
	if update["kind"] != "agent" || !strings.Contains(fmt.Sprint(update["title"]), "Subagent") {
		t.Fatalf("bad subagent update: %#v", update)
	}
}

type subagentToolProvider struct {
	calls int
}

func (p *subagentToolProvider) Name() string { return "subagent-tool-test" }
func (p *subagentToolProvider) Kind() string { return "subagent-tool-test" }
func (p *subagentToolProvider) AvailableModels() []provider.ModelInfo {
	return []provider.ModelInfo{{ModelID: "tool-model", Name: "tool-model"}}
}
func (p *subagentToolProvider) DefaultModel() string     { return "tool-model" }
func (p *subagentToolProvider) ContextWindow(string) int { return 100000 }
func (p *subagentToolProvider) StreamChat(_ context.Context, messages []provider.Message, _ provider.StreamOptions) (<-chan provider.Chunk, <-chan error) {
	p.calls++
	chunks := make(chan provider.Chunk, 2)
	errs := make(chan error, 1)
	go func() {
		defer close(chunks)
		defer close(errs)
		last := messages[len(messages)-1]
		if last.Role != "tool" {
			chunks <- provider.Chunk{ToolCall: &provider.ToolCall{
				ID:        "sub-write",
				Name:      "write_file",
				Arguments: `{"path":"subagent.txt","content":"from subagent"}`,
			}}
			chunks <- provider.Chunk{Done: true, StopReason: "tool_calls"}
			errs <- nil
			return
		}
		chunks <- provider.Chunk{Text: "done after " + strings.TrimSpace(fmt.Sprint(last.Content))}
		chunks <- provider.Chunk{Done: true, StopReason: "stop"}
		errs <- nil
	}()
	return chunks, errs
}

func TestRuntimeSubagentToolCallsUseParentPermissionPath(t *testing.T) {
	p := &subagentToolProvider{}
	r := &recorderConn{}
	a := New(sessionstore.NewIn(t.TempDir()))
	a.Provider = p
	a.Conn = r
	cwd := t.TempDir()
	s := &sessionState{ID: "s1", Cwd: cwd, Model: "tool-model", Mode: ModeDefault}
	a.sessions["s1"] = s
	handled, _, err := a.handleRuntimeCommand(context.Background(), s, acp.PromptParams{
		SessionID: "s1",
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "/subagent write a file"}},
	})
	if err != nil || !handled {
		t.Fatalf("subagent handled=%v err=%v", handled, err)
	}
	if p.calls != 2 {
		t.Fatalf("provider calls = %d", p.calls)
	}
	if r.permissionCalls != 1 {
		t.Fatalf("permission calls = %d", r.permissionCalls)
	}
	body, err := os.ReadFile(filepath.Join(cwd, "subagent.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "from subagent" {
		t.Fatalf("file body = %q", string(body))
	}
	if r.countUpdates("tool_call") < 2 {
		t.Fatalf("expected subagent and write_file tool updates, got %d", r.countUpdates("tool_call"))
	}
}

func TestRuntimeCommandsAreHandledBeforeNativeProvider(t *testing.T) {
	p := &nativeTestProvider{}
	a := New(sessionstore.NewIn(t.TempDir()))
	a.Provider = p
	a.Conn = &recorderConn{}
	ns, err := a.NewSession(context.Background(), acp.NewSessionParams{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Prompt(context.Background(), acp.PromptParams{
		SessionID: ns.SessionID,
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "/btw status"}},
	}); err != nil {
		t.Fatal(err)
	}
	if p.calls != 0 {
		t.Fatalf("native provider should not receive runtime command; calls=%d", p.calls)
	}
}
