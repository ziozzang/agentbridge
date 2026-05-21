package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Mirrors src/tests/executor.test.ts test cases not already covered by
// executor_test.go.

func TestExecuteEmptyArgsAcceptedAsEmptyObject(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	// Empty string arguments should be treated as `{}` rather than failing
	// with a JSON parse error.
	r := e.Execute(context.Background(), "tc", "read_file", "")
	// Falls through to "`path` is required" rather than a JSON parse error.
	if !strings.Contains(r.Content, "`path` is required") {
		t.Errorf("expected path-required error, got %q", r.Content)
	}
}

func TestReadFileRejectsWhitespacePath(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "read_file", `{"path":"   "}`)
	if !strings.Contains(r.Content, "`path` is required") {
		t.Errorf("expected whitespace rejection, got %q", r.Content)
	}
}

func TestWriteFileRejectsWhitespacePath(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "write_file", `{"path":"   ","content":"x"}`)
	if !strings.Contains(r.Content, "`path` is required") {
		t.Errorf("expected whitespace rejection, got %q", r.Content)
	}
}

func TestListFilesRelativeToSessionCwd(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	sub := filepath.Join(e.SessionCwd, "sub")
	_ = os.Mkdir(sub, 0o755)
	_ = os.WriteFile(filepath.Join(sub, "x.txt"), []byte("hi"), 0o600)
	r := e.Execute(context.Background(), "tc", "list_files", `{"path":"sub"}`)
	if !strings.Contains(r.Content, "x.txt") {
		t.Errorf("relative path failed: %q", r.Content)
	}
}

func TestListFilesRejectsEmptyPath(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "list_files", `{"path":""}`)
	if !strings.Contains(r.Content, "non-empty string") {
		t.Errorf("got %q", r.Content)
	}
	r = e.Execute(context.Background(), "tc", "list_files", `{"path":"   "}`)
	if !strings.Contains(r.Content, "non-empty string") {
		t.Errorf("whitespace got %q", r.Content)
	}
}

// ---------------------------------------------------------------------------
// Session-mode gating
// ---------------------------------------------------------------------------

func TestWriteFileAcceptEditsSkipsPermission(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	e.Mode = "accept_edits"
	r := e.Execute(context.Background(), "tc", "write_file", `{"path":"out.txt","content":"data"}`)
	if !strings.Contains(r.Content, "written successfully") {
		t.Fatalf("got %q", r.Content)
	}
	// No permission call should have been made.
	if c.permissionCalls != 0 {
		t.Errorf("accept_edits should skip permission, got %d", c.permissionCalls)
	}
}

func TestWriteFileBypassSkipsPermission(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	e.Mode = "bypass_permissions"
	r := e.Execute(context.Background(), "tc", "write_file", `{"path":"out.txt","content":"x"}`)
	if !strings.Contains(r.Content, "written successfully") {
		t.Fatalf("got %q", r.Content)
	}
	if c.permissionCalls != 0 {
		t.Errorf("bypass should skip permission, got %d", c.permissionCalls)
	}
}

func TestWriteFileDefaultModePromptsForPermission(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	e.Mode = "default"
	_ = e.Execute(context.Background(), "tc", "write_file", `{"path":"out.txt","content":"x"}`)
	if c.permissionCalls != 1 {
		t.Errorf("default mode should call permission once, got %d", c.permissionCalls)
	}
}

// ---------------------------------------------------------------------------
// Permission transport errors → failed tool result
// ---------------------------------------------------------------------------

func TestWriteFilePermissionTransportError(t *testing.T) {
	c := &fakeConn{callErr: errors.New("connection lost")}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "write_file", `{"path":"x.txt","content":"y"}`)
	if !strings.Contains(r.Content, "Error requesting permission") || !strings.Contains(r.Content, "connection lost") {
		t.Errorf("expected transport error surfaced, got %q", r.Content)
	}
	if !c.hasStatus("failed") {
		t.Error("expected failed status update")
	}
	// The write must NOT have happened.
	if _, err := os.Stat(filepath.Join(e.SessionCwd, "x.txt")); err == nil {
		t.Error("file should not be created when permission errors")
	}
}
