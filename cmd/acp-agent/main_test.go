package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ziozzang/agentbridge/internal/acp"
	"github.com/ziozzang/agentbridge/internal/harness/filecontext"
	"github.com/ziozzang/agentbridge/internal/tools/clienttools"
)

func TestPermissionPromptAcceptsNumericYes(t *testing.T) {
	var stderr bytes.Buffer
	c := &client{
		stdin:  bufio.NewReader(strings.NewReader("1\n")),
		stderr: &stderr,
		opts:   clientOptions{Permission: "prompt"},
	}
	resp, err := c.permission(acp.RequestPermissionParams{ToolCall: map[string]any{"title": "Run command: date"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Outcome.Outcome != "selected" || resp.Outcome.OptionID != "allow" {
		t.Fatalf("permission response = %#v", resp)
	}
	if !strings.Contains(stderr.String(), "Run command: date") {
		t.Fatalf("prompt missing tool title: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "1: yes") || !strings.Contains(stderr.String(), "3: no") || !strings.Contains(stderr.String(), "0: yolo") {
		t.Fatalf("numeric choices missing: %q", stderr.String())
	}
}

func TestPermissionPromptYoloSetsAllowMode(t *testing.T) {
	c := &client{
		stdin:  bufio.NewReader(strings.NewReader("0\n")),
		stderr: ioDiscard{},
		opts:   clientOptions{Permission: "prompt"},
	}
	resp, err := c.permission(acp.RequestPermissionParams{ToolCall: map[string]any{"title": "Write file"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Outcome.Outcome != "selected" || resp.Outcome.OptionID != "allow" {
		t.Fatalf("permission response = %#v", resp)
	}
	if c.opts.Permission != "allow" {
		t.Fatalf("permission mode = %q", c.opts.Permission)
	}
}

func TestPermissionOverlayChoiceCancelsOnInterrupt(t *testing.T) {
	events := make(chan uiEvent, 2)
	c := &client{
		events: events,
		done:   make(chan struct{}),
		state:  clientState{Busy: true},
		opts:   clientOptions{Permission: "prompt"},
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := c.choose("permission requested: Run command", "command: ps", []choiceOption{
			{Key: "1", Label: "yes"},
			{Key: "3", Label: "no"},
		})
		errCh <- err
	}()
	select {
	case ev := <-events:
		if _, ok := ev.(uiPermissionRequest); !ok {
			t.Fatalf("event = %#v", ev)
		}
	case <-time.After(time.Second):
		t.Fatalf("permission event not emitted")
	}
	c.Interrupt(context.Background())
	select {
	case err := <-errCh:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("choice did not cancel")
	}
}

func TestPermissionReadOnlyRejects(t *testing.T) {
	c := &client{
		stdin:  bufio.NewReader(strings.NewReader("")),
		stderr: ioDiscard{},
		opts:   clientOptions{Permission: "reject"},
	}
	resp, err := c.permission(acp.RequestPermissionParams{ToolCall: map[string]any{"title": "Write file"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Outcome.Outcome != "selected" || resp.Outcome.OptionID != "reject" {
		t.Fatalf("permission response = %#v", resp)
	}
}

func TestUpdateText(t *testing.T) {
	got := updateText(map[string]any{
		"content": map[string]any{"type": "text", "text": "hello"},
	})
	if got != "hello" {
		t.Fatalf("got %q", got)
	}
	got = updateText(map[string]any{
		"content": map[string]any{"content": map[string]any{"text": "nested"}},
	})
	if got != "nested" {
		t.Fatalf("nested got %q", got)
	}
}

func TestPrintUpdateHidesThinkingByDefault(t *testing.T) {
	var stdout, stderr bytes.Buffer
	c := &client{stdout: &stdout, stderr: &stderr}
	c.printUpdate(acp.SessionUpdateParams{Update: map[string]any{
		"sessionUpdate": "agent_thought_chunk",
		"content":       map[string]any{"type": "text", "text": "<think>hidden</think>"},
	}})
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("thinking should be hidden by default stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestPrintUpdateShowsThinkingWhenRequested(t *testing.T) {
	var stderr bytes.Buffer
	c := &client{stdout: ioDiscard{}, stderr: &stderr, opts: clientOptions{ShowThinking: true}}
	c.printUpdate(acp.SessionUpdateParams{Update: map[string]any{
		"sessionUpdate": "agent_thought_chunk",
		"content":       map[string]any{"type": "text", "text": "hidden"},
	}})
	if !strings.Contains(stderr.String(), "[thinking] hidden") {
		t.Fatalf("thinking not printed: %q", stderr.String())
	}
}

func TestPrintUpdateCoalescesThinkingChunks(t *testing.T) {
	var stderr bytes.Buffer
	c := &client{stdout: ioDiscard{}, stderr: &stderr, opts: clientOptions{ShowThinking: true}}
	for _, part := range []string{"useful", " things", " I", " could"} {
		c.printUpdate(acp.SessionUpdateParams{Update: map[string]any{
			"sessionUpdate": "agent_thought_chunk",
			"content":       map[string]any{"type": "text", "text": part},
		}})
	}
	c.printUpdate(acp.SessionUpdateParams{Update: map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": "answer"},
	}})
	got := stderr.String()
	if strings.Count(got, "[thinking]") != 1 {
		t.Fatalf("thinking header count mismatch: %q", got)
	}
	if !strings.Contains(got, "[thinking] useful things I could\n") {
		t.Fatalf("thinking chunks not coalesced: %q", got)
	}
}

func TestPrintUpdateSeparatesToolStatus(t *testing.T) {
	var stderr bytes.Buffer
	c := &client{stdout: ioDiscard{}, stderr: &stderr, opts: clientOptions{ShowTools: true}}
	c.printUpdate(acp.SessionUpdateParams{Update: map[string]any{
		"sessionUpdate": "tool_call",
		"title":         "Read file: README.md",
		"status":        "in_progress",
	}})
	if !strings.Contains(stderr.String(), "[tool:in_progress] Read file: README.md") {
		t.Fatalf("tool status not separated: %q", stderr.String())
	}
}

func TestInterruptCancelsLocalPromptContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := &client{state: clientState{Busy: true}}
	c.promptCancel = cancel
	if !c.Interrupt(context.Background()) {
		t.Fatalf("interrupt returned false")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatalf("prompt context was not cancelled")
	}
}

func TestStreamBufferFlushesOnToolUpdate(t *testing.T) {
	var stdout, stderr bytes.Buffer
	c := &client{
		stdout: &stdout,
		stderr: &stderr,
		stream: newStreamBuffer(&stdout),
		opts:   clientOptions{ShowTools: true},
	}
	c.stream.start()
	c.printUpdate(acp.SessionUpdateParams{Update: map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": "partial"},
	}})
	if stdout.String() != "" {
		t.Fatalf("stream flushed too early: %q", stdout.String())
	}
	c.printUpdate(acp.SessionUpdateParams{Update: map[string]any{
		"sessionUpdate": "tool_call_update",
		"status":        "completed",
	}})
	if !strings.Contains(stdout.String(), "partial") {
		t.Fatalf("stream did not flush before tool update: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestToolSurfaceTracksSubagents(t *testing.T) {
	c := &client{stderr: ioDiscard{}, opts: clientOptions{ShowTools: true}, activeTools: map[string]string{}, activeAgents: map[string]string{}}
	c.printUpdate(acp.SessionUpdateParams{Update: map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    "s1/sub/abcd",
		"kind":          "agent",
		"title":         "Subagent: inspect",
		"status":        "in_progress",
	}})
	if c.state.Subagents != 1 || c.state.Tools != 0 || c.state.LastTool != "Subagent: inspect" {
		t.Fatalf("state after subagent start = %#v", c.state)
	}
	c.printUpdate(acp.SessionUpdateParams{Update: map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    "s1/sub/abcd",
		"status":        "completed",
	}})
	if c.state.Subagents != 0 {
		t.Fatalf("state after subagent done = %#v", c.state)
	}
}

func TestPrintUpdateStoresContextSnapshot(t *testing.T) {
	c := &client{}
	c.printUpdate(acp.SessionUpdateParams{Update: map[string]any{
		"sessionUpdate": "session_info_update",
		"context": map[string]any{
			"tokens":             float64(35),
			"window":             float64(100),
			"used_percent":       float64(35),
			"left_percent":       float64(65),
			"messages":           float64(3),
			"checkpoints":        float64(1),
			"cache_epoch":        float64(2),
			"compaction_enabled": true,
		},
		"limits": map[string]any{
			"5h":     float64(94),
			"weekly": float64(84),
		},
	}})
	if c.state.Context.Tokens != 35 || c.state.Context.Window != 100 || c.state.Context.LeftPercent != 65 {
		t.Fatalf("context state = %#v", c.state.Context)
	}
	if c.state.Limits.FiveHourPercent != 94 || c.state.Limits.WeeklyPercent != 84 {
		t.Fatalf("limits = %#v", c.state.Limits)
	}
	if got := contextLabel(c.state.Context); !strings.Contains(got, "65% left") || !strings.Contains(got, "35% used") {
		t.Fatalf("context label = %q", got)
	}
}

func TestLimitLabelsShowUnknownSlots(t *testing.T) {
	got := strings.Join(limitLabels(limitState{}), " ")
	if !strings.Contains(got, "5h ?") || !strings.Contains(got, "weekly ?") {
		t.Fatalf("limit labels = %q", got)
	}
	got = strings.Join(limitLabels(limitState{FiveHourPercent: 94, WeeklyPercent: 84}), " ")
	if !strings.Contains(got, "5h 94%") || !strings.Contains(got, "weekly 84%") {
		t.Fatalf("limit labels = %q", got)
	}
}

func TestSubmitPromptQueuesWhileBusy(t *testing.T) {
	var stderr bytes.Buffer
	c := &client{
		stderr: &stderr,
		state:  clientState{Busy: true},
	}
	c.SubmitPrompt(context.Background(), "second prompt")
	if c.state.QueueLen != 1 || len(c.promptQueue) != 1 {
		t.Fatalf("queue state = %d %#v", c.state.QueueLen, c.promptQueue)
	}
	if !strings.Contains(stderr.String(), "queued 1") {
		t.Fatalf("queue message = %q", stderr.String())
	}
}

func TestCommandsUpdateLocalState(t *testing.T) {
	var stderr bytes.Buffer
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "SOUL.md"), []byte("soul"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &client{
		stderr: &stderr,
		opts:   clientOptions{Permission: "prompt", ShowTools: true},
		state:  clientState{Addr: "127.0.0.1:8765", Cwd: cwd, SessionID: "s1", Model: "m1", Mode: "default"},
	}
	c.commandPermission("reject")
	c.commandBool("thinking", "on", &c.opts.ShowThinking)
	c.commandBool("tools", "off", &c.opts.ShowTools)
	c.printStatus()
	got := stderr.String()
	for _, want := range []string{"permission reject", "thinking true", "tools false", "model=m1", "mode=default", "SOUL.md"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status missing %q in:\n%s", want, got)
		}
	}
}

func TestCapabilitiesAdvertiseClientShellTool(t *testing.T) {
	c := &client{}
	caps := c.capabilities()
	raw, ok := caps["clientTools"].([]clienttools.AdvertisedTool)
	if !ok {
		t.Fatalf("clientTools type = %T", caps["clientTools"])
	}
	names := map[string]bool{}
	for _, tool := range raw {
		names[tool.Name] = true
	}
	if !names["run_lua"] || !names["run_command"] {
		t.Fatalf("client tools = %+v", raw)
	}
}

func TestClientRunCommandToolExecutesInClientCwd(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "x.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &client{
		stdin:  bufio.NewReader(strings.NewReader("")),
		stderr: ioDiscard{},
		opts:   clientOptions{Permission: "allow"},
		state:  clientState{Cwd: cwd},
	}
	result, err := c.callClientTool(context.Background(), clientToolCallParams{
		Name: "run_command",
		Args: map[string]any{"command": "cat x.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "Exit code: 0") || !strings.Contains(result.Output, "hello") {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestClientRunCommandToolRejectsEmptyCommand(t *testing.T) {
	c := &client{stderr: ioDiscard{}, opts: clientOptions{Permission: "allow"}}
	_, err := c.callClientTool(context.Background(), clientToolCallParams{
		Name: "run_command",
		Args: map[string]any{"command": "  "},
	})
	if err == nil || !strings.Contains(err.Error(), "non-empty string") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientRunCommandOtherCommand(t *testing.T) {
	cwd := t.TempDir()
	c := &client{
		stdin:  bufio.NewReader(strings.NewReader("4\nprintf changed\n")),
		stderr: ioDiscard{},
		opts:   clientOptions{Permission: "prompt"},
		state:  clientState{Cwd: cwd},
	}
	result, err := c.callClientTool(context.Background(), clientToolCallParams{
		Name: "run_command",
		Args: map[string]any{"command": "printf original"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "changed") || strings.Contains(result.Output, "original") {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestClientRunCommandSameCommandRemembered(t *testing.T) {
	cwd := t.TempDir()
	var stderr bytes.Buffer
	c := &client{
		stdin:  bufio.NewReader(strings.NewReader("2\n")),
		stderr: &stderr,
		opts:   clientOptions{Permission: "prompt"},
		state:  clientState{Cwd: cwd},
	}
	for i := 0; i < 2; i++ {
		_, err := c.callClientTool(context.Background(), clientToolCallParams{
			Name: "run_command",
			Args: map[string]any{"command": "printf remembered"},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if strings.Count(stderr.String(), "permission requested") != 1 {
		t.Fatalf("same command should prompt once, stderr=%q", stderr.String())
	}
}

func TestLuaGoalCommandStoresGoal(t *testing.T) {
	t.Setenv("AGENTBRIDGE_CLI_ORCH_DB", filepath.Join(t.TempDir(), "orch.sqlite"))
	var stdout bytes.Buffer
	c := &client{stdout: &stdout, stderr: ioDiscard{}}
	c.commandGoal(context.Background(), "set ship robust permissions")
	c.commandGoal(context.Background(), "status")
	got := stdout.String()
	if !strings.Contains(got, "goal set: ship robust permissions") || !strings.Contains(got, "goal: ship robust permissions") {
		t.Fatalf("goal output = %q", got)
	}
}

func TestCommandResumeUsage(t *testing.T) {
	var stderr bytes.Buffer
	c := &client{stderr: &stderr}
	c.commandResume(context.Background(), "", false)
	if !strings.Contains(stderr.String(), "usage: /resume SESSION_ID") {
		t.Fatalf("resume usage missing: %q", stderr.String())
	}
	stderr.Reset()
	c.commandResume(context.Background(), "", true)
	if !strings.Contains(stderr.String(), "usage: /session-load SESSION_ID") {
		t.Fatalf("load usage missing: %q", stderr.String())
	}
}

func TestReplEOFExits(t *testing.T) {
	var stderr bytes.Buffer
	c := &client{
		stdin:  bufio.NewReader(strings.NewReader("")),
		stderr: &stderr,
	}
	if err := repl(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "acp>") {
		t.Fatalf("prompt not printed: %q", stderr.String())
	}
}

func TestCommandAttachQueuesFilesAndStructure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte("# Notes\nhello upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	c := &client{
		stderr: &stderr,
		state:  clientState{Cwd: dir, SessionID: "s1", Model: "m", Mode: "default"},
	}
	c.commandAttach(path)
	if len(c.files) != 1 {
		t.Fatalf("files = %+v", c.files)
	}
	c.commandFiles()
	c.commandStructure()
	got := stderr.String()
	for _, want := range []string{"attached", "queued files:", "notes.md", "queued_files: 1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	c.commandClearFiles()
	if len(c.files) != 0 {
		t.Fatalf("files not cleared: %+v", c.files)
	}
}

func TestNotifyWritesNotification(t *testing.T) {
	server, clientConn := net.Pipe()
	defer server.Close()
	defer clientConn.Close()
	c := &client{
		conn:    clientConn,
		dec:     json.NewDecoder(bufio.NewReader(clientConn)),
		stdin:   bufio.NewReader(strings.NewReader("")),
		stdout:  ioDiscard{},
		stderr:  ioDiscard{},
		pending: map[int64]*pendingResponse{},
		done:    make(chan struct{}),
	}
	done := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(server).ReadString('\n')
		done <- line
	}()
	if err := c.Notify(context.Background(), "session/cancel", acp.CancelParams{SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if !strings.Contains(got, `"method":"session/cancel"`) || !strings.Contains(got, `"sessionId":"s1"`) {
		t.Fatalf("notification = %s", got)
	}
}

func TestPromptSendsQueuedAttachmentsAndClears(t *testing.T) {
	server, clientConn := net.Pipe()
	defer server.Close()
	defer clientConn.Close()
	c := &client{
		conn:    clientConn,
		dec:     json.NewDecoder(bufio.NewReader(clientConn)),
		stdout:  ioDiscard{},
		stderr:  ioDiscard{},
		pending: map[int64]*pendingResponse{},
		done:    make(chan struct{}),
		files: []attachment{{Resource: filecontext.Resource{
			Path: "/tmp/doc.md", Name: "doc.md", MimeType: "text/markdown", Text: "attached body", Size: 13,
		}}},
	}
	go c.readLoop()
	done := make(chan string, 1)
	go func() {
		var msg rpcMessage
		dec := json.NewDecoder(bufio.NewReader(server))
		if err := dec.Decode(&msg); err != nil {
			done <- err.Error()
			return
		}
		done <- string(msg.Params)
		rawID := msg.ID
		resp := rpcMessage{
			JSONRPC: "2.0",
			ID:      rawID,
			Result:  json.RawMessage(`{"stopReason":"end_turn"}`),
		}
		body, _ := json.Marshal(resp)
		body = append(body, '\n')
		_, _ = server.Write(body)
	}()
	if err := c.Prompt(context.Background(), "s1", "question"); err != nil {
		t.Fatal(err)
	}
	got := <-done
	for _, want := range []string{`"type":"resource"`, "attached body", `"question"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt params missing %q in %s", want, got)
		}
	}
	if len(c.files) != 0 {
		t.Fatalf("attachments not cleared: %+v", c.files)
	}
}

func TestRunLuaCanInspectAndAttach(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte("hello lua"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	c := &client{
		stdout: &stdout,
		stderr: ioDiscard{},
		state:  clientState{Cwd: dir, SessionID: "s1", Model: "m", Mode: "default"},
	}
	result, err := c.runLua(context.Background(), runLuaParams{Code: `
cli.say(cli.status())
cli.attach("notes.md")
cli.say(cli.structure())
`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "session=s1") || !strings.Contains(result.Output, "queued_files: 1") {
		t.Fatalf("lua output = %q", result.Output)
	}
	if len(c.files) != 1 {
		t.Fatalf("files = %+v", c.files)
	}
}

func TestRunLuaOrchestrationControlLoop(t *testing.T) {
	c := &client{
		stdout: ioDiscard{},
		stderr: ioDiscard{},
		state:  clientState{Cwd: t.TempDir(), SessionID: "s1"},
	}
	result, err := c.runLua(context.Background(), runLuaParams{Code: `
local plan = cli.orch.plan({"a", "b"})
local trigger = cli.orch.trigger("all_done", function(ctx)
  local c = cli.orch.check_status(ctx.plan)
  return c.done == 2
end, function(ctx)
  cli.orch.steer(ctx, "summarize results")
  ctx.stop = true
end)
local ctx = cli.orch.control_loop({
  plan = plan,
  triggers = { trigger },
  run = function(job, ctx)
    return "ran " .. job.task
  end
})
cli.say(cli.orch.status_line(plan))
cli.say(ctx.steering[1])
`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "plan=done") || !strings.Contains(result.Output, "summarize results") {
		t.Fatalf("lua output = %q", result.Output)
	}
}

func TestRunLuaOrchestrationEmitsLiveUIEvents(t *testing.T) {
	events := make(chan uiEvent, 16)
	c := &client{
		stdout: ioDiscard{},
		stderr: ioDiscard{},
		state:  clientState{Cwd: t.TempDir(), SessionID: "s1"},
		events: events,
	}
	_, err := c.runLua(context.Background(), runLuaParams{Code: `
local plan = cli.orch.plan({{ id = "inspect", task = "inspect" }})
cli.orch.control_loop({
  plan = plan,
  run = function(job, ctx) return "ok" end
})
`})
	if err != nil {
		t.Fatal(err)
	}
	close(events)
	var titles []string
	for ev := range events {
		if info, ok := ev.(uiInfoEvent); ok {
			titles = append(titles, info.Title+":"+info.Body)
		}
	}
	got := strings.Join(titles, "\n")
	for _, want := range []string{"orch:control_loop_start", "orch:job_start:inspect", "orch:job_done:inspect", "orch:control_loop_done"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in events:\n%s", want, got)
		}
	}
}

func TestUIEventRecordSerializesPermissionWithoutReplyChannel(t *testing.T) {
	got := uiEventRecord(uiPermissionRequest{
		Title:   "tool permission",
		Detail:  "command: date",
		Options: []choiceOption{{Key: "1", Label: "yes"}},
		Reply:   make(chan string, 1),
	})
	if got["type"] != "permission_request" || got["title"] != "tool permission" {
		t.Fatalf("bad event record: %#v", got)
	}
	if _, err := json.Marshal(got); err != nil {
		t.Fatalf("event record should marshal without channel: %v", err)
	}
}

func TestUIEventRecordSerializesCommandEvent(t *testing.T) {
	got := uiEventRecord(uiCommandEvent{Text: "/help"})
	if got["type"] != "command" || got["text"] != "/help" {
		t.Fatalf("bad command event record: %#v", got)
	}
}

func TestRunCommandEmitsLocalCommandEvent(t *testing.T) {
	events := make(chan uiEvent, 8)
	c := &client{stdout: ioDiscard{}, stderr: ioDiscard{}, events: events}
	if err := c.runCommand(context.Background(), "/help"); err != nil {
		t.Fatal(err)
	}
	close(events)
	var got []string
	for ev := range events {
		switch ev := ev.(type) {
		case uiCommandEvent:
			got = append(got, "command:"+ev.Text)
		case uiInfoEvent:
			got = append(got, "info:"+ev.Title)
		}
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "command:/help") || !strings.Contains(joined, "info:help") {
		t.Fatalf("events = %q", joined)
	}
}

func TestRunCommandDoesNotDuplicateServerPromptCommandEcho(t *testing.T) {
	for _, line := range []string{"/context", "/compact 2000", "/save cp1", "/load cp1", "/list"} {
		if !serverPromptCommandLine(line) {
			t.Fatalf("server prompt command not recognized: %s", line)
		}
	}
	for _, line := range []string{"/help", "/status", "/goal status", "/permission allow"} {
		if serverPromptCommandLine(line) {
			t.Fatalf("local command incorrectly treated as server prompt: %s", line)
		}
	}
}

func TestRunLuaSnapshotTimerAndState(t *testing.T) {
	t.Setenv("AGENTBRIDGE_CLI_ORCH_DB", filepath.Join(t.TempDir(), "orch.sqlite"))
	c := &client{
		stdout: ioDiscard{},
		stderr: ioDiscard{},
		state:  clientState{Cwd: t.TempDir(), SessionID: "s1", Model: "m"},
	}
	result, err := c.runLua(context.Background(), runLuaParams{
		State: map[string]any{"seed": "ready"},
		Code: `
local snap = cli.snapshot()
cli.say(snap.session_id .. ":" .. snap.model)
cli.say(cli.get("seed"))
cli.set("seed", "changed")
cli.say(cli.get("seed"))
cli.mem_set("turn_note", "scratch")
cli.say(cli.mem_get("turn_note"))
local timer = cli.orch.timer({ interval_ms = 1, max_ticks = 2 })
cli.say("tick=" .. cli.orch.tick(timer))
local ok, result, elapsed = cli.orch.with_timeout({ timeout_ms = 1000 }, function()
  return cli.now().rfc3339
end)
cli.emit("timer_done", tostring(ok))
`})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"s1:m", "ready", "changed", "scratch", "tick=1", "[event] timer_done true"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("missing %q in %q", want, result.Output)
		}
	}
}

func TestRunLuaSQLiteAndKV(t *testing.T) {
	t.Setenv("AGENTBRIDGE_CLI_ORCH_DB", filepath.Join(t.TempDir(), "orch.sqlite"))
	c := &client{
		stdout: ioDiscard{},
		stderr: ioDiscard{},
		state:  clientState{Cwd: t.TempDir(), SessionID: "s1"},
	}
	result, err := c.runLua(context.Background(), runLuaParams{Code: `
cli.kv_set("goal/current", "ship")
cli.say(cli.kv_get("goal/current"))
cli.sql_exec("insert into jobs(id, status, payload, updated_at) values('j1', 'pending', 'next', 'now')")
local rows = cli.sql_query("select id, status from jobs order by id")
cli.say(rows[1].id .. ":" .. rows[1].status)
cli.emit("job_seen", rows[1].id)
local events = cli.sql_query("select name, payload from events order by id")
cli.say(events[1].name .. ":" .. events[1].payload)
`})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ship", "j1:pending", "job_seen:j1"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("missing %q in %q", want, result.Output)
		}
	}
}

func TestRunLuaPrimitiveNamespaces(t *testing.T) {
	t.Setenv("AGENTBRIDGE_CLI_ORCH_DB", filepath.Join(t.TempDir(), "orch.sqlite"))
	c := &client{
		stdout: ioDiscard{},
		stderr: ioDiscard{},
		state:  clientState{Cwd: t.TempDir(), SessionID: "s1"},
	}
	result, err := c.runLua(context.Background(), runLuaParams{Code: `
cli.memory.set("turn", "ok")
cli.memory.kv_set("persist", "yes")
cli.memory.put("alpha beta", "tag:test")
local found = cli.memory.search("alpha", 5)
local prompt = cli.llm.judge("goal", "evidence", { run = false })
local ranked = cli.data.rank({"a", "b"}, "prefer a", { run = false })
cli.say(cli.memory.get("turn"))
cli.say(cli.memory.kv_get("persist"))
cli.say(found[1].metadata)
cli.say(string.find(prompt, "<goal>") ~= nil and "judge_prompt" or "missing")
cli.say(string.find(ranked, "<criteria>") ~= nil and "rank_prompt" or "missing")
`})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ok", "yes", "tag:test", "judge_prompt", "rank_prompt"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("missing %q in %q", want, result.Output)
		}
	}
}

func TestRunLuaGoalLoopCaseFromDesign(t *testing.T) {
	t.Setenv("AGENTBRIDGE_CLI_ORCH_DB", filepath.Join(t.TempDir(), "orch.sqlite"))
	c := &client{stdout: ioDiscard{}, stderr: ioDiscard{}, state: clientState{Cwd: t.TempDir(), SessionID: "s1"}}
	result, err := c.runLua(context.Background(), runLuaParams{Code: `
local goal = "ship orchestration docs"
cli.memory.kv_set("goal/current", goal)
local plan = cli.orch.plan({
  { id = "inspect", task = "inspect current cli orchestration state" },
  { id = "judge", task = "judge whether docs and tests cover the design" },
})
local ctx = cli.orch.control_loop({
  plan = plan,
  run = function(job, ctx)
    local prompt = cli.llm.judge(goal, job.task, { run = false, memory = "goal-case" })
    cli.sql_exec("insert or replace into jobs(id, status, payload, result, updated_at) values('" .. job.id .. "', 'done', '" .. job.task .. "', 'prompted', 'now')")
    return prompt
  end,
  triggers = {
    cli.orch.trigger("done", function(ctx)
      local c = cli.orch.check_status(ctx.plan)
      return c.done == 2
    end, function(ctx)
      cli.orch.steer(ctx, "goal ready for review")
      ctx.stop = true
    end)
  }
})
cli.say(cli.memory.kv_get("goal/current"))
cli.say(cli.orch.status_line(plan))
cli.say(ctx.steering[1])
local rows = cli.sql_query("select count(*) as n from jobs")
cli.say("jobs=" .. rows[1].n)
`})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ship orchestration docs", "plan=done", "goal ready for review", "jobs=2"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("missing %q in %q", want, result.Output)
		}
	}
}

func TestRunLuaAutoResearchCaseFromDesign(t *testing.T) {
	t.Setenv("AGENTBRIDGE_CLI_ORCH_DB", filepath.Join(t.TempDir(), "orch.sqlite"))
	c := &client{stdout: ioDiscard{}, stderr: ioDiscard{}, state: clientState{Cwd: t.TempDir(), SessionID: "s1"}}
	result, err := c.runLua(context.Background(), runLuaParams{Code: `
local topic = "agent orchestration factors"
local research_prompt = cli.data.research_source(topic, { run = false, store_key = "research/plan" })
cli.memory.put(research_prompt, "kind:research_plan topic:" .. topic)
local plan = cli.orch.plan({
  "collect source candidates",
  "extract claims",
  "rank evidence",
})
cli.orch.loop(plan, function(job)
  return cli.llm.extract(job.task, "{claims:[], evidence:[]}", { run = false })
end)
local found = cli.memory.search("research_plan", 3)
cli.say(cli.memory.kv_get("research/plan") ~= nil and "stored_plan" or "missing")
cli.say(cli.orch.status_line(plan))
cli.say(found[1].metadata)
`})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"stored_plan", "plan=done", "kind:research_plan"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("missing %q in %q", want, result.Output)
		}
	}
}

func TestInboundClientRunLuaDoesNotBlockReadLoop(t *testing.T) {
	server, clientConn := net.Pipe()
	defer server.Close()
	defer clientConn.Close()
	c := &client{
		conn:    clientConn,
		dec:     json.NewDecoder(bufio.NewReader(clientConn)),
		stdout:  ioDiscard{},
		stderr:  ioDiscard{},
		pending: map[int64]*pendingResponse{},
		done:    make(chan struct{}),
		state:   clientState{SessionID: "s1", Cwd: t.TempDir()},
	}
	go c.readLoop()
	req := rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "client/run_lua"}
	req.Params, _ = json.Marshal(runLuaParams{Code: `cli.say("ok")`})
	body, _ := json.Marshal(req)
	body = append(body, '\n')
	if _, err := server.Write(body); err != nil {
		t.Fatal(err)
	}
	var resp rpcMessage
	if err := json.NewDecoder(bufio.NewReader(server)).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil || !strings.Contains(string(resp.Result), "ok") {
		t.Fatalf("response = %+v result=%s", resp, string(resp.Result))
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
