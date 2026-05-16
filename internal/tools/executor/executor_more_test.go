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

func TestRunCommandRejectsEmpty(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "run_command", `{"command":""}`)
	if !strings.Contains(r.Content, "must be a non-empty string") {
		t.Errorf("got %q", r.Content)
	}
	r = e.Execute(context.Background(), "tc", "run_command", `{"command":"   "}`)
	if !strings.Contains(r.Content, "must be a non-empty string") {
		t.Errorf("whitespace got %q", r.Content)
	}
}

func TestRunCommandShellQuoting(t *testing.T) {
	// Pipes / quoting only work when we shell-execute. echo a | wc -c counts 2
	// (newline included).
	c := &fakeConn{}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "run_command", `{"command":"echo a | wc -c"}`)
	if !strings.Contains(r.Content, "Exit code: 0") {
		t.Fatalf("expected zero exit, got %q", r.Content)
	}
	if !strings.Contains(r.Content, "2") {
		t.Fatalf("expected piped output, got %q", r.Content)
	}
}

func TestRunCommandIncludesStderr(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "run_command", `{"command":"echo err 1>&2; exit 7"}`)
	if !strings.Contains(r.Content, "Exit code: 7") {
		t.Errorf("exit code missing: %q", r.Content)
	}
	if !strings.Contains(r.Content, "STDERR:") || !strings.Contains(r.Content, "err") {
		t.Errorf("stderr missing: %q", r.Content)
	}
}

func TestRunCommandRejected(t *testing.T) {
	c := &fakeConn{reject: true}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "run_command", `{"command":"echo hello"}`)
	if !strings.Contains(r.Content, "rejected by user") {
		t.Errorf("got %q", r.Content)
	}
}

func TestRunCommandCancelled(t *testing.T) {
	c := &fakeConn{cancel: true}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "run_command", `{"command":"echo hello"}`)
	if !strings.Contains(r.Content, "cancelled by user") {
		t.Errorf("got %q", r.Content)
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

func TestRunCommandDefaultModePromptsForPermission(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	e.Mode = "default"
	_ = e.Execute(context.Background(), "tc", "run_command", `{"command":"echo a"}`)
	if c.permissionCalls != 1 {
		t.Errorf("default mode should call permission once, got %d", c.permissionCalls)
	}
}

func TestRunCommandAcceptEditsStillPromptsForCommands(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	e.Mode = "accept_edits"
	_ = e.Execute(context.Background(), "tc", "run_command", `{"command":"echo a"}`)
	if c.permissionCalls != 1 {
		t.Errorf("accept_edits should still prompt for execute, got %d", c.permissionCalls)
	}
}

func TestRunCommandBypassSkipsPermission(t *testing.T) {
	c := &fakeConn{}
	e := newExecutor(t, c)
	e.Mode = "bypass_permissions"
	_ = e.Execute(context.Background(), "tc", "run_command", `{"command":"echo a"}`)
	if c.permissionCalls != 0 {
		t.Errorf("bypass should skip permission, got %d", c.permissionCalls)
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

func TestRunCommandPermissionTransportError(t *testing.T) {
	c := &fakeConn{callErr: errors.New("io blew up")}
	e := newExecutor(t, c)
	r := e.Execute(context.Background(), "tc", "run_command", `{"command":"echo nope"}`)
	if !strings.Contains(r.Content, "Error requesting permission") {
		t.Errorf("got %q", r.Content)
	}
	if !c.hasStatus("failed") {
		t.Error("expected failed status update")
	}
}
