package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ziozzang/agentbridge/internal/acp"
	"github.com/ziozzang/agentbridge/internal/tools/zaimcp"
)

// fakeConn records every notification, and answers Call requests with the
// pre-staged outcome.
type fakeConn struct {
	mu              sync.Mutex
	updates         []map[string]any
	approve         bool
	reject          bool
	cancel          bool
	callErr         error
	luaOutput       string
	permissionCalls int
	luaCalls        int
	clientToolCalls int
	clientToolName  string
}

func (f *fakeConn) SendNotification(method string, params any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if method != "session/update" {
		return nil
	}
	p := params.(acp.SessionUpdateParams)
	f.updates = append(f.updates, p.Update)
	return nil
}

func (f *fakeConn) Call(_ context.Context, method string, params any, result any) error {
	if f.callErr != nil {
		return f.callErr
	}
	if method == "client/run_lua" {
		f.mu.Lock()
		f.luaCalls++
		f.mu.Unlock()
		if out, ok := result.(*struct {
			Output string `json:"output"`
		}); ok {
			out.Output = f.luaOutput
		}
		return nil
	}
	if method == "client/call_tool" {
		f.mu.Lock()
		f.clientToolCalls++
		if m, ok := params.(map[string]any); ok {
			f.clientToolName, _ = m["name"].(string)
		}
		f.mu.Unlock()
		if out, ok := result.(*struct {
			Output string `json:"output"`
		}); ok {
			out.Output = f.luaOutput
		}
		return nil
	}
	if method != "session/request_permission" {
		return errors.New("unexpected call: " + method)
	}
	f.mu.Lock()
	f.permissionCalls++
	f.mu.Unlock()
	resp := result.(*acp.RequestPermissionResponse)
	switch {
	case f.cancel:
		resp.Outcome = acp.PermissionOutcome{Outcome: "cancelled"}
	case f.reject:
		resp.Outcome = acp.PermissionOutcome{Outcome: "selected", OptionID: "reject"}
	default:
		resp.Outcome = acp.PermissionOutcome{Outcome: "selected", OptionID: "allow"}
	}
	_ = f.approve // documenting intent
	return nil
}

func (f *fakeConn) lastClientToolName() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clientToolName
}

func (f *fakeConn) countClientToolCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clientToolCalls
}

func (f *fakeConn) countLuaCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.luaCalls
}

func (f *fakeConn) hasStatus(status string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.updates {
		if u["status"] == status {
			return true
		}
	}
	return false
}

func newExecutor(t *testing.T, c *fakeConn) *Executor {
	t.Helper()
	return &Executor{Conn: c, SessionID: "s1", SessionCwd: t.TempDir()}
}

func TestExecuteUnknownToolFails(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc1", "made_up", "{}")
	if !strings.Contains(r.Content, "unknown tool") {
		t.Errorf("got %q", r.Content)
	}
}

func TestReadFile(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	p := filepath.Join(e.SessionCwd, "hello.txt")
	_ = os.WriteFile(p, []byte("hi"), 0o600)
	r := e.Execute(context.Background(), "tc", "read_file", `{"path":"hello.txt"}`)
	if r.Content != "hi" {
		t.Errorf("got %q", r.Content)
	}
	if !c.hasStatus("completed") {
		t.Error("expected completed update")
	}
}

func TestReadFileMissingPath(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "read_file", `{}`)
	if !strings.Contains(r.Content, "`path` is required") {
		t.Errorf("got %q", r.Content)
	}
}

func TestWriteFileApproved(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "write_file", `{"path":"out.txt","content":"data"}`)
	if !strings.Contains(r.Content, "written successfully") {
		t.Errorf("got %q", r.Content)
	}
	body, _ := os.ReadFile(filepath.Join(e.SessionCwd, "out.txt"))
	if string(body) != "data" {
		t.Errorf("file body = %q", string(body))
	}
}

func TestWriteFileRejected(t *testing.T) {
	c := &fakeConn{reject: true}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "write_file", `{"path":"out.txt","content":"x"}`)
	if !strings.Contains(r.Content, "rejected by user") {
		t.Errorf("got %q", r.Content)
	}
	if _, err := os.Stat(filepath.Join(e.SessionCwd, "out.txt")); err == nil {
		t.Error("file should not exist")
	}
}

func TestWriteFileCancelled(t *testing.T) {
	c := &fakeConn{cancel: true}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "write_file", `{"path":"out.txt","content":"x"}`)
	if !strings.Contains(r.Content, "cancelled by user") {
		t.Errorf("got %q", r.Content)
	}
}

func TestListFiles(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	_ = os.WriteFile(filepath.Join(e.SessionCwd, "a.txt"), []byte("hi"), 0o600)
	_ = os.Mkdir(filepath.Join(e.SessionCwd, "d"), 0o755)
	r := e.Execute(context.Background(), "tc", "list_files", `{"path":"."}`)
	if !strings.Contains(r.Content, "a.txt") || !strings.Contains(r.Content, "dir\t") {
		t.Errorf("got %q", r.Content)
	}
}

func TestImageAnalysisRequiresVision(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "image_analysis", `{"image_source":"a.png"}`)
	if !strings.Contains(r.Content, "vision is not configured") {
		t.Errorf("got %q", r.Content)
	}
}

func TestClientRunLuaDelegatesToACPClient(t *testing.T) {
	c := &fakeConn{luaOutput: "lua says ok"}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "client_run_lua", `{"code":"cli.say('ok')","args":["a"]}`)
	if !strings.Contains(r.Content, "lua says ok") {
		t.Fatalf("content = %q", r.Content)
	}
	if c.countLuaCalls() != 1 {
		t.Fatalf("lua calls = %d", c.countLuaCalls())
	}
	if !c.hasStatus("completed") {
		t.Fatal("expected completed update")
	}
}

func TestClientRunLuaRequiresCodeOrPath(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "client_run_lua", `{}`)
	if !strings.Contains(r.Content, "either `code` or `path` is required") {
		t.Fatalf("content = %q", r.Content)
	}
}

func TestClientNamespacedToolDelegatesToACPClient(t *testing.T) {
	c := &fakeConn{luaOutput: "client tool ok"}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "client__run_lua", `{"code":"cli.say('ok')"}`)
	if !strings.Contains(r.Content, "client tool ok") {
		t.Fatalf("content = %q", r.Content)
	}
	if c.countClientToolCalls() != 1 {
		t.Fatalf("client tool calls = %d", c.countClientToolCalls())
	}
	if c.lastClientToolName() != "run_lua" {
		t.Fatalf("client tool name = %q", c.lastClientToolName())
	}
}

func TestClientRunCommandDelegatesToACPClient(t *testing.T) {
	c := &fakeConn{luaOutput: "Exit code: 0\n\nSTDOUT:\nhello\n\nSTDERR:\n(empty)"}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "client__run_command", `{"command":"echo hello"}`)
	if !strings.Contains(r.Content, "hello") {
		t.Fatalf("content = %q", r.Content)
	}
	if c.countClientToolCalls() != 1 {
		t.Fatalf("client tool calls = %d", c.countClientToolCalls())
	}
	if c.lastClientToolName() != "run_command" {
		t.Fatalf("client tool name = %q", c.lastClientToolName())
	}
}

type fakeMCP struct {
	out json.RawMessage
	err error
}

func (f *fakeMCP) CallTool(_ context.Context, _ zaimcp.CallToolInput) (json.RawMessage, error) {
	return f.out, f.err
}

func TestWebSearchFormat(t *testing.T) {
	t.Setenv("Z_AI_API_KEY", "k")
	c := &fakeConn{}
	e := newExecutor(t, c)
	e.MCP = &fakeMCP{out: json.RawMessage(`{"content":[{"type":"text","text":"{\"search_result\":[{\"title\":\"T\",\"link\":\"https://x\",\"content\":\"sum\"}]}"}]}`)}
	r := e.Execute(context.Background(), "tc", "web_search", `{"query":"go"}`)
	if !strings.Contains(r.Content, "[1] T") || !strings.Contains(r.Content, "URL: https://x") || !strings.Contains(r.Content, "Summary: sum") {
		t.Errorf("got %q", r.Content)
	}
}

func TestWebReaderFormat(t *testing.T) {
	t.Setenv("Z_AI_API_KEY", "k")
	c := &fakeConn{}
	e := newExecutor(t, c)
	e.MCP = &fakeMCP{out: json.RawMessage(`{"content":[{"type":"text","text":"{\"reader_result\":{\"title\":\"Hi\",\"url\":\"https://x\",\"content\":\"body\"}}"}]}`)}
	r := e.Execute(context.Background(), "tc", "web_reader", `{"url":"https://x"}`)
	if !strings.Contains(r.Content, "# Hi") || !strings.Contains(r.Content, "body") {
		t.Errorf("got %q", r.Content)
	}
}

func TestExecuteBadArgsJson(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "read_file", `{ this is not json`)
	if !strings.Contains(r.Content, "could not parse tool arguments") {
		t.Errorf("got %q", r.Content)
	}
}
