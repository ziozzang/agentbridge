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
	calls        int
	firstRequest []provider.Message
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
	if p.calls == 1 {
		p.firstRequest = append([]provider.Message(nil), messages...)
	}
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
	if update := r.findUpdate("tool_call_update"); update == nil {
		t.Fatalf("expected subagent updates")
	}
}

func TestRuntimeSubagentRejectsRecursiveCommand(t *testing.T) {
	a := New(sessionstore.NewIn(t.TempDir()))
	a.Provider = &nativeTestProvider{}
	a.Conn = &recorderConn{}
	s := &sessionState{ID: "s1", Cwd: t.TempDir(), Model: "m", Mode: ModeDefault}
	a.sessions["s1"] = s
	ctx := context.WithValue(context.Background(), subagentDepthKey{}, maxSubagentDepth)
	handled, _, err := a.handleRuntimeCommand(ctx, s, acp.PromptParams{
		SessionID: "s1",
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "/subagent recurse"}},
	})
	if err != nil || !handled {
		t.Fatalf("subagent handled=%v err=%v", handled, err)
	}
	if a.Provider.(*nativeTestProvider).calls != 0 {
		t.Fatalf("recursive subagent should not call provider")
	}
}

func TestRuntimeSubagentInjectsActiveSkillsAndToolNames(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".agentbridge", "skills")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub.md"), []byte("Always preserve subagent skill context."), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &subagentToolProvider{}
	a := New(sessionstore.NewIn(t.TempDir()))
	a.Provider = p
	a.Conn = &recorderConn{}
	s := &sessionState{
		ID:           "s1",
		Cwd:          cwd,
		Model:        "tool-model",
		Mode:         ModeBypassPerms,
		ActiveSkills: []sessionstore.ActiveSkill{{Name: "sub", Path: filepath.Join(dir, "sub.md")}},
	}
	a.sessions["s1"] = s
	handled, _, err := a.handleRuntimeCommand(context.Background(), s, acp.PromptParams{
		SessionID: "s1",
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "/subagent check skills"}},
	})
	if err != nil || !handled {
		t.Fatalf("subagent handled=%v err=%v", handled, err)
	}
	if len(p.firstRequest) == 0 {
		t.Fatalf("provider did not receive subagent request")
	}
	system := fmt.Sprint(p.firstRequest[0].Content)
	if !strings.Contains(system, "Always preserve subagent skill context.") {
		t.Fatalf("missing active skill in subagent system prompt:\n%s", system)
	}
	if !strings.Contains(system, "write_file") {
		t.Fatalf("missing tool names in subagent system prompt:\n%s", system)
	}
}

type subagentOverflowProvider struct {
	calls int
}

func (p *subagentOverflowProvider) Name() string { return "subagent-overflow-test" }
func (p *subagentOverflowProvider) Kind() string { return "subagent-overflow-test" }
func (p *subagentOverflowProvider) AvailableModels() []provider.ModelInfo {
	return []provider.ModelInfo{{ModelID: "overflow-model", Name: "overflow-model"}}
}
func (p *subagentOverflowProvider) DefaultModel() string     { return "overflow-model" }
func (p *subagentOverflowProvider) ContextWindow(string) int { return 4096 }
func (p *subagentOverflowProvider) StreamChat(_ context.Context, _ []provider.Message, _ provider.StreamOptions) (<-chan provider.Chunk, <-chan error) {
	p.calls++
	chunks := make(chan provider.Chunk)
	errs := make(chan error, 1)
	go func() {
		close(chunks)
		if p.calls == 1 {
			errs <- &provider.ContextOverflowError{Provider: p.Name(), Model: "overflow-model", Message: "too long"}
			close(errs)
			return
		}
		errs <- nil
		close(errs)
	}()
	return chunks, errs
}

func TestRuntimeSubagentRetriesOnceAfterContextOverflow(t *testing.T) {
	p := &subagentOverflowProvider{}
	a := New(sessionstore.NewIn(t.TempDir()))
	a.Provider = p
	a.Conn = &recorderConn{}
	s := &sessionState{ID: "s1", Cwd: t.TempDir(), Model: "overflow-model", Mode: ModeDefault}
	a.sessions["s1"] = s
	handled, _, err := a.handleRuntimeCommand(context.Background(), s, acp.PromptParams{
		SessionID: "s1",
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "/subagent survive overflow"}},
	})
	if err != nil || !handled {
		t.Fatalf("subagent handled=%v err=%v", handled, err)
	}
	if p.calls != 2 {
		t.Fatalf("provider calls = %d, want retry", p.calls)
	}
}

type subagentLoopProvider struct {
	calls int
}

func (p *subagentLoopProvider) Name() string { return "subagent-loop-test" }
func (p *subagentLoopProvider) Kind() string { return "subagent-loop-test" }
func (p *subagentLoopProvider) AvailableModels() []provider.ModelInfo {
	return []provider.ModelInfo{{ModelID: "loop-model", Name: "loop-model"}}
}
func (p *subagentLoopProvider) DefaultModel() string     { return "loop-model" }
func (p *subagentLoopProvider) ContextWindow(string) int { return 100000 }
func (p *subagentLoopProvider) StreamChat(_ context.Context, _ []provider.Message, _ provider.StreamOptions) (<-chan provider.Chunk, <-chan error) {
	p.calls++
	chunks := make(chan provider.Chunk, 1)
	errs := make(chan error, 1)
	go func() {
		defer close(chunks)
		defer close(errs)
		chunks <- provider.Chunk{ToolCall: &provider.ToolCall{ID: "loop", Name: "list_files", Arguments: `{}`}}
		errs <- nil
	}()
	return chunks, errs
}

func TestRuntimeSubagentMaxTurnsReturnsError(t *testing.T) {
	p := &subagentLoopProvider{}
	r := &recorderConn{}
	a := New(sessionstore.NewIn(t.TempDir()))
	a.Provider = p
	a.Conn = r
	a.MaxTurns = 1
	s := &sessionState{ID: "s1", Cwd: t.TempDir(), Model: "loop-model", Mode: ModeBypassPerms}
	a.sessions["s1"] = s
	handled, _, err := a.handleRuntimeCommand(context.Background(), s, acp.PromptParams{
		SessionID: "s1",
		Prompt:    []acp.ContentBlock{{Type: "text", Text: "/subagent loop"}},
	})
	if err != nil || !handled {
		t.Fatalf("subagent handled=%v err=%v", handled, err)
	}
	update := r.findUpdate("tool_call_update")
	if update == nil {
		t.Fatalf("missing failed subagent update")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	found := false
	for _, u := range r.updates {
		if strings.Contains(fmt.Sprint(u), "max turns") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing max turns error update: %#v", r.updates)
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
