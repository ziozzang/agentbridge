// Package executor dispatches GLM tool calls inside the agent process.
//
// File reads/list operations and approved writes/commands run locally with
// paths resolved relative to the ACP session cwd. Writes and shell commands
// always ask the ACP client for permission before executing.
package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ziozzang/agentbridge/internal/acp"
	"github.com/ziozzang/agentbridge/internal/credentials"
	"github.com/ziozzang/agentbridge/internal/metrics"
	"github.com/ziozzang/agentbridge/internal/tools/zaimcp"
)

// SessionConn is the subset of acp.Conn that an Executor needs. Tests inject
// a stub that records sessionUpdate / requestPermission interactions.
type SessionConn interface {
	SendNotification(method string, params any) error
	Call(ctx context.Context, method string, params any, result any) error
}

// Vision is the optional vision client used by the image_analysis tool.
type Vision interface {
	CallTool(ctx context.Context, name string, args map[string]any) (string, error)
}

// MCPCaller abstracts the Z.AI MCP client so tests can swap it out.
type MCPCaller interface {
	CallTool(ctx context.Context, in zaimcp.CallToolInput) (json.RawMessage, error)
}

// Executor dispatches tool calls.
type Executor struct {
	Conn       SessionConn
	SessionID  string
	SessionCwd string
	Vision     Vision
	MCP        MCPCaller
	SessionMCP sessionMcpClient
	// Plugins, when set, handles tool calls whose name matches the
	// plugin naming convention (plugin__<name>__<tool>). The executor
	// uses the PluginDispatcher interface so it does not need to import
	// internal/plugins directly.
	Plugins PluginDispatcher
	// Mode is the ACP session mode for this turn. Empty / "default" means
	// always ask for permission; "accept_edits" auto-allows writes;
	// "bypass_permissions" auto-allows both writes and commands.
	Mode string
}

// PluginDispatcher routes a tool invocation to a plugin. ok=true means the
// call was claimed by a plugin (regardless of whether the call succeeded).
type PluginDispatcher interface {
	Dispatch(ctx context.Context, name string, args json.RawMessage) (result string, ok bool, err error)
}

// sessionMcpClient is the interface to session-scoped MCP servers.
type sessionMcpClient interface {
	CallTool(ctx context.Context, fullName string, args map[string]any) (string, error)
}

// Result is the shape returned to the prompt loop after a tool runs.
type Result struct {
	Content string
}

// Execute looks up and runs a GLM tool call. The returned content is fed back
// to the model as the tool message body.
func (e *Executor) Execute(ctx context.Context, toolCallID, toolName, rawArgs string) Result {
	var args map[string]any
	if strings.TrimSpace(rawArgs) == "" {
		args = map[string]any{}
	} else if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		msg := fmt.Sprintf("Error: could not parse tool arguments as JSON: %s", rawArgs)
		e.failedToolCall(toolCallID, toolName, map[string]any{}, msg)
		return Result{Content: msg}
	}
	// Route mcp__ prefixed tools to sessionMCP.
	if strings.HasPrefix(toolName, "mcp__") && e.SessionMCP != nil {
		return e.mcpTool(ctx, toolCallID, toolName, args)
	}
	// Route plugin__ prefixed tools to the active plugin dispatcher.
	if strings.HasPrefix(toolName, "plugin__") && e.Plugins != nil {
		return e.pluginTool(ctx, toolCallID, toolName, args, rawArgs)
	}
	switch toolName {
	case "read_file":
		return e.readFile(ctx, toolCallID, args)
	case "write_file":
		return e.writeFile(ctx, toolCallID, args)
	case "list_files":
		return e.listFiles(ctx, toolCallID, args)
	case "run_command":
		return e.runCommand(ctx, toolCallID, args)
	case "web_search":
		return e.webSearch(ctx, toolCallID, args)
	case "web_reader":
		return e.webReader(ctx, toolCallID, args)
	case "image_analysis":
		return e.imageAnalysis(ctx, toolCallID, args)
	default:
		msg := fmt.Sprintf(`Error: unknown tool %q`, toolName)
		e.failedToolCall(toolCallID, toolName, args, msg)
		return Result{Content: msg}
	}
}

// ---------------------------------------------------------------------------
// Local file/shell tools
// ---------------------------------------------------------------------------

func (e *Executor) readFile(ctx context.Context, id string, args map[string]any) Result {
	path := strings.TrimSpace(stringArg(args["path"]))
	if path == "" {
		return e.failAndReturn(id, "read_file", args, "Error: `path` is required.")
	}
	abs := e.resolvePath(path)
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    id,
		"title":         "Read file: " + path,
		"kind":          "read",
		"status":        "in_progress",
		"locations":     []any{map[string]any{"path": path}},
		"rawInput":      args,
	})
	body, err := os.ReadFile(abs)
	if err != nil {
		e.markFailed(id, err.Error())
		return Result{Content: "Error reading file: " + err.Error()}
	}
	content := string(body)
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    id,
		"status":        "completed",
		"content":       []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": content}}},
		"rawOutput":     map[string]any{"content": content},
	})
	return Result{Content: content}
}

func (e *Executor) writeFile(ctx context.Context, id string, args map[string]any) Result {
	path := strings.TrimSpace(stringArg(args["path"]))
	content := stringArg(args["content"])
	if path == "" {
		return e.failAndReturn(id, "write_file", args, "Error: `path` is required.")
	}
	abs := e.resolvePath(path)
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    id,
		"title":         "Write file: " + path,
		"kind":          "edit",
		"status":        "pending",
		"locations":     []any{map[string]any{"path": path}},
		"rawInput":      args,
	})
	outcome := e.maybeRequestPermission(ctx, permArgs{
		ToolCallID: id, Kind: "write", Title: "Write file: " + path,
		Locations: []any{map[string]any{"path": path}}, RawInput: args,
	})
	switch outcome.Type {
	case permError:
		e.markFailed(id, outcome.Message)
		return Result{Content: "Error requesting permission: " + outcome.Message}
	case permCancelled:
		e.markFailed(id, "Cancelled by user.")
		return Result{Content: "Write cancelled by user."}
	case permReject:
		e.markFailed(id, "Rejected by user.")
		return Result{Content: "Write rejected by user."}
	}
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    id,
		"status":        "in_progress",
	})
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		e.markFailed(id, err.Error())
		return Result{Content: "Error writing file: " + err.Error()}
	}
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    id,
		"status":        "completed",
		"rawOutput":     map[string]any{"success": true},
	})
	return Result{Content: "File written successfully: " + path}
}

func (e *Executor) listFiles(ctx context.Context, id string, args map[string]any) Result {
	path := strings.TrimSpace(stringArg(args["path"]))
	if path == "" {
		return e.failAndReturn(id, "list_files", args, "Error listing files: `path` must be a non-empty string.")
	}
	abs := e.resolvePath(path)
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    id,
		"title":         "List files: " + path,
		"kind":          "read",
		"status":        "in_progress",
		"locations":     []any{map[string]any{"path": path}},
		"rawInput":      args,
	})
	entries, err := os.ReadDir(abs)
	if err != nil {
		e.markFailed(id, err.Error())
		return Result{Content: "Error listing files: " + err.Error()}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	lines := []string{fmt.Sprintf("Listing for %s (%s)", path, abs)}
	for _, ent := range entries {
		info, err := os.Lstat(filepath.Join(abs, ent.Name()))
		if err != nil {
			continue
		}
		var typ string
		switch {
		case ent.IsDir():
			typ = "dir"
		case info.Mode()&os.ModeSymlink != 0:
			typ = "link"
		default:
			typ = "file"
		}
		lines = append(lines, fmt.Sprintf("%s\t%d\t%s", typ, info.Size(), ent.Name()))
	}
	out := strings.Join(lines, "\n")
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    id,
		"status":        "completed",
		"content":       []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": out}}},
		"rawOutput":     map[string]any{"output": out},
	})
	return Result{Content: out}
}

func (e *Executor) runCommand(ctx context.Context, id string, args map[string]any) Result {
	command := strings.TrimSpace(stringArg(args["command"]))
	if command == "" {
		return e.failAndReturn(id, "run_command", args, "Error running command: command must be a non-empty string.")
	}
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    id,
		"title":         "Run command: " + command,
		"kind":          "execute",
		"status":        "pending",
		"locations":     []any{},
		"rawInput":      args,
	})
	outcome := e.maybeRequestPermission(ctx, permArgs{
		ToolCallID: id, Kind: "execute", Title: "Run command: " + command,
		Locations: []any{}, RawInput: args,
	})
	switch outcome.Type {
	case permError:
		e.markFailed(id, outcome.Message)
		return Result{Content: "Error requesting permission: " + outcome.Message}
	case permCancelled:
		e.markFailed(id, "Cancelled by user.")
		return Result{Content: "Command cancelled by user."}
	case permReject:
		e.markFailed(id, "Rejected by user.")
		return Result{Content: "Command rejected by user."}
	}
	return e.runLocalCommand(ctx, id, command)
}

// CommandResult captures the structured shell output.
type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Signal   string
}

func (e *Executor) runLocalCommand(ctx context.Context, id, command string) Result {
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    id,
		"status":        "in_progress",
	})
	res, err := RunShell(ctx, command, e.SessionCwd)
	if err != nil {
		e.markFailed(id, err.Error())
		return Result{Content: "Error running command: " + err.Error()}
	}
	out := FormatCommandOutput(res)
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    id,
		"status":        "completed",
		"content":       []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": out}}},
		"rawOutput":     map[string]any{"stdout": res.Stdout, "stderr": res.Stderr, "exitCode": res.ExitCode, "signal": res.Signal},
	})
	return Result{Content: out}
}

// RunShell executes `sh -c command` in cwd and captures stdout/stderr.
func RunShell(ctx context.Context, command, cwd string) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = cwd
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return CommandResult{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return CommandResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return CommandResult{}, err
	}
	outBytes, _ := io.ReadAll(stdout)
	errBytes, _ := io.ReadAll(stderr)
	wErr := cmd.Wait()
	res := CommandResult{
		Stdout: string(outBytes),
		Stderr: string(errBytes),
	}
	if wErr == nil {
		res.ExitCode = 0
		return res, nil
	}
	var ee *exec.ExitError
	if errors.As(wErr, &ee) {
		res.ExitCode = ee.ExitCode()
		if ee.ProcessState != nil && ee.ProcessState.ExitCode() == -1 {
			// killed by signal
			res.Signal = ee.ProcessState.String()
		}
		return res, nil
	}
	return res, wErr
}

// FormatCommandOutput renders a shell result the way TypeScript does.
func FormatCommandOutput(r CommandResult) string {
	lines := []string{fmt.Sprintf("Exit code: %d", r.ExitCode)}
	if r.Signal != "" {
		lines = append(lines, "Signal: "+r.Signal)
	}
	stdout := r.Stdout
	if stdout == "" {
		stdout = "(empty)"
	}
	stderr := r.Stderr
	if stderr == "" {
		stderr = "(empty)"
	}
	lines = append(lines, "", "STDOUT:", stdout, "", "STDERR:", stderr)
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// Z.AI MCP web tools
// ---------------------------------------------------------------------------

func (e *Executor) webSearch(ctx context.Context, id string, args map[string]any) Result {
	query := strings.TrimSpace(stringArg(args["query"]))
	if query == "" {
		return e.failAndReturn(id, "web_search", args, "Error: `query` is required.")
	}
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    id,
		"title":         "Web search: " + query,
		"kind":          "fetch",
		"status":        "in_progress",
		"locations":     []any{},
		"rawInput":      args,
	})
	apiKey, err := requireKey()
	if err != nil {
		e.markFailed(id, err.Error())
		return Result{Content: "Error performing web search: " + err.Error()}
	}
	if e.MCP == nil {
		e.MCP = zaimcp.New()
	}
	toolArgs := map[string]any{"query": query}
	if c, ok := args["count"]; ok {
		toolArgs["count"] = c
	}
	raw, err := e.MCP.CallTool(ctx, zaimcp.CallToolInput{
		Endpoint: zaimcp.WebSearchEndpoint, APIKey: apiKey,
		ToolName: "webSearchPrime", Arguments: toolArgs,
	})
	if err != nil {
		e.markFailed(id, err.Error())
		return Result{Content: "Error performing web search: " + err.Error()}
	}
	out, count := formatSearchOutput(raw)
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    id,
		"status":        "completed",
		"content":       []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": out}}},
		"rawOutput":     map[string]any{"resultCount": count},
	})
	return Result{Content: out}
}

func (e *Executor) webReader(ctx context.Context, id string, args map[string]any) Result {
	url := strings.TrimSpace(stringArg(args["url"]))
	rf := stringArg(args["return_format"])
	if rf == "" {
		rf = "markdown"
	}
	if url == "" {
		return e.failAndReturn(id, "web_reader", args, "Error: `url` is required.")
	}
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    id,
		"title":         "Read URL: " + url,
		"kind":          "fetch",
		"status":        "in_progress",
		"locations":     []any{map[string]any{"path": url}},
		"rawInput":      args,
	})
	apiKey, err := requireKey()
	if err != nil {
		e.markFailed(id, err.Error())
		return Result{Content: "Error reading URL: " + err.Error()}
	}
	if e.MCP == nil {
		e.MCP = zaimcp.New()
	}
	raw, err := e.MCP.CallTool(ctx, zaimcp.CallToolInput{
		Endpoint: zaimcp.WebReaderEndpoint, APIKey: apiKey,
		ToolName: "webReader", Arguments: map[string]any{"url": url, "return_format": rf},
	})
	if err != nil {
		e.markFailed(id, err.Error())
		return Result{Content: "Error reading URL: " + err.Error()}
	}
	out, title, resultURL := formatReaderOutput(raw)
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    id,
		"status":        "completed",
		"content":       []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": out}}},
		"rawOutput":     map[string]any{"title": title, "url": resultURL},
	})
	return Result{Content: out}
}

func (e *Executor) imageAnalysis(ctx context.Context, id string, args map[string]any) Result {
	source := strings.TrimSpace(stringArg(args["image_source"]))
	if source == "" {
		return e.failAndReturn(id, "image_analysis", args, "Error: `image_source` is required.")
	}
	if e.Vision == nil {
		return e.failAndReturn(id, "image_analysis", args, "Error: vision is not configured on this agent process.")
	}
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    id,
		"title":         "Analyze image: " + source,
		"kind":          "fetch",
		"status":        "in_progress",
		"locations":     []any{map[string]any{"path": source}},
		"rawInput":      args,
	})
	visionArgs := map[string]any{"image_source": source}
	if p := stringArg(args["prompt"]); p != "" {
		visionArgs["prompt"] = p
	}
	text, err := e.Vision.CallTool(ctx, "image_analysis", visionArgs)
	if err != nil {
		e.markFailed(id, err.Error())
		return Result{Content: "Error analyzing image: " + err.Error()}
	}
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    id,
		"status":        "completed",
		"content":       []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": text}}},
		"rawOutput":     map[string]any{"text": text},
	})
	return Result{Content: text}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (e *Executor) sendUpdate(update map[string]any) {
	_ = e.Conn.SendNotification("session/update", acp.SessionUpdateParams{
		SessionID: e.SessionID,
		Update:    update,
	})
}

func (e *Executor) requestPermission(ctx context.Context, toolCall map[string]any, options []acp.PermissionOption) (acp.RequestPermissionResponse, error) {
	var resp acp.RequestPermissionResponse
	err := e.Conn.Call(ctx, "session/request_permission", acp.RequestPermissionParams{
		SessionID: e.SessionID, ToolCall: toolCall, Options: options,
	}, &resp)
	return resp, err
}

// permOutcomeType enumerates the mode-aware permission outcomes.
type permOutcomeType int

const (
	permAllow permOutcomeType = iota
	permReject
	permCancelled
	permError
)

type permOutcome struct {
	Type    permOutcomeType
	Message string
}

type permArgs struct {
	ToolCallID string
	// Kind is "write" or "execute"; selects the ACP tool-call kind to surface.
	Kind      string
	Title     string
	Locations []any
	RawInput  map[string]any
}

// maybeRequestPermission gates a write/execute tool call on the current
// session mode and on the user's permission decision. Transport errors are
// returned as permError so the caller can fail the tool call cleanly instead
// of silently allowing the operation.
func (e *Executor) maybeRequestPermission(ctx context.Context, a permArgs) permOutcome {
	switch e.Mode {
	case "bypass_permissions":
		return permOutcome{Type: permAllow}
	case "accept_edits":
		if a.Kind == "write" {
			return permOutcome{Type: permAllow}
		}
	}
	acpKind := "edit"
	if a.Kind == "execute" {
		acpKind = "execute"
	}
	resp, err := e.requestPermission(ctx, map[string]any{
		"toolCallId": a.ToolCallID,
		"title":      a.Title,
		"kind":       acpKind,
		"status":     "pending",
		"locations":  a.Locations,
		"rawInput":   a.RawInput,
	}, []acp.PermissionOption{
		{Kind: "allow_once", Name: "Allow", OptionID: "allow"},
		{Kind: "reject_once", Name: "Skip", OptionID: "reject"},
	})
	if err != nil {
		return permOutcome{Type: permError, Message: err.Error()}
	}
	if resp.Outcome.Outcome == "cancelled" {
		return permOutcome{Type: permCancelled}
	}
	if resp.Outcome.Outcome == "selected" && resp.Outcome.OptionID == "reject" {
		return permOutcome{Type: permReject}
	}
	return permOutcome{Type: permAllow}
}

func (e *Executor) markFailed(id, message string) {
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    id,
		"status":        "failed",
		"rawOutput":     map[string]any{"error": message},
	})
}

func (e *Executor) failedToolCall(id, name string, args map[string]any, message string) {
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    id,
		"title":         name,
		"kind":          "other",
		"status":        "failed",
		"locations":     []any{},
		"rawInput":      args,
		"rawOutput":     map[string]any{"error": message},
	})
}

func (e *Executor) failAndReturn(id, name string, args map[string]any, message string) Result {
	e.failedToolCall(id, name, args, message)
	return Result{Content: message}
}

func (e *Executor) resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	cwd := e.SessionCwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	return filepath.Join(cwd, path)
}

func stringArg(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func requireKey() (string, error) {
	k := credentials.Resolve()
	if k == "" {
		return "", errors.New("No API key found. Set Z_AI_API_KEY, or run `agentbridge --setup` to store one.")
	}
	return k, nil
}

// ---------------------------------------------------------------------------
// MCP payload formatters (mirror the TS helpers).
// ---------------------------------------------------------------------------

func formatSearchOutput(raw json.RawMessage) (string, int) {
	payload := unwrapMcpPayload(raw)
	results := payloadList(payload, "search_result")
	if len(results) == 0 {
		if s, ok := payload.(string); ok && s != "" {
			return s, 0
		}
		return "No results found.", 0
	}
	var sections []string
	for i, raw := range results {
		r, _ := raw.(map[string]any)
		lines := []string{fmt.Sprintf("[%d] %s", i+1, valueOr(r, "title", "(no title)"))}
		if v := stringValue(r, "link"); v != "" {
			lines = append(lines, "URL: "+v)
		}
		if v := stringValue(r, "media"); v != "" {
			lines = append(lines, "Source: "+v)
		}
		if v := stringValue(r, "publish_date"); v != "" {
			lines = append(lines, "Date: "+v)
		}
		if v := stringValue(r, "content"); v != "" {
			lines = append(lines, "Summary: "+v)
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}
	return strings.Join(sections, "\n\n"), len(results)
}

func formatReaderOutput(raw json.RawMessage) (string, string, string) {
	payload := unwrapMcpPayload(raw)
	root, _ := payload.(map[string]any)
	if root == nil {
		if s, ok := payload.(string); ok && s != "" {
			return s, "", ""
		}
		return "No content returned.", "", ""
	}
	result, _ := root["reader_result"].(map[string]any)
	if result == nil {
		if s, ok := payload.(string); ok && s != "" {
			return s, "", ""
		}
		return "No content returned.", "", ""
	}
	title := stringValue(result, "title")
	url := stringValue(result, "url")
	desc := stringValue(result, "description")
	body := stringValue(result, "content")
	var lines []string
	if title != "" {
		lines = append(lines, "# "+title)
	}
	if url != "" {
		lines = append(lines, "URL: "+url)
	}
	if desc != "" {
		lines = append(lines, "\n"+desc)
	}
	if body != "" {
		lines = append(lines, "\n"+body)
	}
	if len(lines) == 0 {
		return "No content returned.", title, url
	}
	return strings.Join(lines, "\n"), title, url
}

func unwrapMcpPayload(raw json.RawMessage) any {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	root, ok := v.(map[string]any)
	if !ok {
		return v
	}
	contents, ok := root["content"].([]any)
	if !ok {
		return v
	}
	var texts []string
	for _, c := range contents {
		if m, ok := c.(map[string]any); ok {
			if s, ok := m["text"].(string); ok && s != "" {
				texts = append(texts, s)
			}
		}
	}
	if len(texts) == 0 {
		return v
	}
	if len(texts) == 1 {
		var inner any
		if err := json.Unmarshal([]byte(texts[0]), &inner); err == nil {
			return inner
		}
		return texts[0]
	}
	return strings.Join(texts, "\n")
}

func payloadList(payload any, key string) []any {
	root, ok := payload.(map[string]any)
	if !ok {
		return nil
	}
	list, _ := root[key].([]any)
	return list
}

func stringValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func valueOr(m map[string]any, key, fallback string) string {
	if v := stringValue(m, key); v != "" {
		return v
	}
	return fallback
}

// ---------------------------------------------------------------------------
// Session MCP tools
// ---------------------------------------------------------------------------

func (e *Executor) mcpTool(ctx context.Context, id string, toolName string, args map[string]any) Result {
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    id,
		"title":         "Call MCP tool: " + toolName,
		"kind":          "mcp",
		"status":        "in_progress",
		"locations":     []any{},
		"rawInput":      args,
	})
	result, err := e.SessionMCP.CallTool(ctx, toolName, args)
	metrics.ObserveToolCall("mcp", toolName, err == nil)
	if err != nil {
		e.markFailed(id, err.Error())
		return Result{Content: "Error calling MCP tool: " + err.Error()}
	}
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    id,
		"status":        "completed",
		"content":       []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": result}}},
		"rawOutput":     map[string]any{"content": result},
	})
	return Result{Content: result}
}

// pluginTool dispatches a `plugin__<name>__<tool>` call through the
// configured PluginDispatcher and reports tool_call updates to the client.
func (e *Executor) pluginTool(ctx context.Context, id string, toolName string, args map[string]any, rawArgs string) Result {
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    id,
		"title":         "Call plugin tool: " + toolName,
		"kind":          "plugin",
		"status":        "in_progress",
		"locations":     []any{},
		"rawInput":      args,
	})
	raw := json.RawMessage(rawArgs)
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	result, ok, err := e.Plugins.Dispatch(ctx, toolName, raw)
	if !ok {
		// Should never happen because the prefix check matched, but be
		// defensive: fall through to the generic failure branch.
		msg := fmt.Sprintf("Error: plugin not active for %q", toolName)
		metrics.ObserveToolCall("plugin", toolName, false)
		e.markFailed(id, msg)
		return Result{Content: msg}
	}
	metrics.ObserveToolCall("plugin", toolName, err == nil)
	if err != nil {
		e.markFailed(id, err.Error())
		return Result{Content: "Error calling plugin tool: " + err.Error()}
	}
	e.sendUpdate(map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    id,
		"status":        "completed",
		"content":       []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": result}}},
		"rawOutput":     map[string]any{"content": result},
	})
	return Result{Content: result}
}
