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

	"github.com/ziozzang/glm-acp/internal/acp"
	"github.com/ziozzang/glm-acp/internal/tools/zaimcp"
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
	permissionCalls int
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

func (f *fakeConn) Call(_ context.Context, method string, _ any, result any) error {
	if f.callErr != nil {
		return f.callErr
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

func TestRunCommandApproved(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "run_command", `{"command":"echo hello"}`)
	if !strings.Contains(r.Content, "Exit code: 0") || !strings.Contains(r.Content, "hello") {
		t.Errorf("got %q", r.Content)
	}
}

func TestRunCommandFailingExit(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "run_command", `{"command":"exit 3"}`)
	if !strings.Contains(r.Content, "Exit code: 3") {
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
