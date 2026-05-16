package executor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeDispatcher struct {
	gotName string
	gotArgs string
	out     string
	err     error
}

func (f *fakeDispatcher) Dispatch(_ context.Context, name string, args json.RawMessage) (string, bool, error) {
	f.gotName = name
	f.gotArgs = string(args)
	return f.out, true, f.err
}

func TestExecutor_PluginDispatchSuccess(t *testing.T) {
	fc := &fakeConn{}
	disp := &fakeDispatcher{out: `{"ok":true}`}
	e := &Executor{Conn: fc, SessionID: "s1", Plugins: disp}

	res := e.Execute(context.Background(), "tc1", "plugin__sqlite__sqlite_list", `{}`)
	if res.Content != `{"ok":true}` {
		t.Errorf("content=%q", res.Content)
	}
	if disp.gotName != "plugin__sqlite__sqlite_list" {
		t.Errorf("dispatcher saw %q", disp.gotName)
	}
	if disp.gotArgs != "{}" {
		t.Errorf("dispatcher args=%q", disp.gotArgs)
	}
	// Should have at least an in_progress and a completed update.
	if len(fc.updates) < 2 {
		t.Fatalf("expected >=2 updates, got %d", len(fc.updates))
	}
	if fc.updates[0]["status"] != "in_progress" {
		t.Errorf("first update status=%v", fc.updates[0]["status"])
	}
	if fc.updates[len(fc.updates)-1]["status"] != "completed" {
		t.Errorf("last update status=%v", fc.updates[len(fc.updates)-1]["status"])
	}
}

func TestExecutor_PluginDispatchError(t *testing.T) {
	fc := &fakeConn{}
	disp := &fakeDispatcher{err: errors.New("boom")}
	e := &Executor{Conn: fc, SessionID: "s1", Plugins: disp}
	res := e.Execute(context.Background(), "tc1", "plugin__sqlite__nope", `{}`)
	if !strings.Contains(res.Content, "boom") {
		t.Errorf("content=%q", res.Content)
	}
}

func TestExecutor_PluginPrefixWithoutDispatcherUnknownTool(t *testing.T) {
	fc := &fakeConn{}
	e := &Executor{Conn: fc, SessionID: "s1"}
	res := e.Execute(context.Background(), "tc1", "plugin__sqlite__list", `{}`)
	if !strings.Contains(res.Content, "unknown tool") {
		t.Errorf("expected unknown tool error, got %q", res.Content)
	}
}
