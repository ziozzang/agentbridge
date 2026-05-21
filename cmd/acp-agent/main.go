// acp-agent is a small terminal client for AgentBridge's ACP TCP server.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ziozzang/agentbridge/internal/acp"
	"github.com/ziozzang/agentbridge/internal/agent"
)

const usage = `acp-agent - terminal ACP client for AgentBridge

Usage:
  acp-agent --addr 127.0.0.1:8765 --model glm-5.1
  acp-agent --addr 127.0.0.1:8765 --model codex-agent --prompt "inspect this repo"
  acp-agent --addr 127.0.0.1:8765 "list files in the current directory"

Flags:
  --addr ADDR          ACP TCP address (default "127.0.0.1:8765")
  --cwd DIR            session working directory (default current directory)
  --model MODEL        model/profile id to select after session creation
  --mode MODE          session mode: default, accept_edits, bypass_permissions
  --prompt TEXT        send one prompt and exit
  --permission MODE    permission handling: prompt, allow, reject, cancel (default prompt)
  --yolo               shorthand for --mode bypass_permissions --permission allow
  --read-only          shorthand for --mode default --permission reject
  --show-thinking      print ACP agent_thought_chunk updates to stderr
  --hide-tools         hide ACP tool_call/tool_call_update status messages
  --raw-updates        print raw non-text session/update payloads to stderr
  --version            print version and exit

Interactive commands:
  /help                show commands
  /status              show current connection/session settings
  /sessions            list sessions for the current cwd
  /resume SESSION_ID   resume a persisted session without replay
  /load SESSION_ID     load a persisted session and replay messages
  /exit, /quit         leave the session
  /model [MODEL]       show or switch model with session/set_model
  /mode [MODE]         show or switch mode with session/set_mode
  /permission [MODE]   show or set permission handling
  /thinking [on|off]   show or toggle thinking display
  /tools [on|off]      show or toggle tool status display
  /raw [on|off]        show or toggle raw update display
`

func main() {
	addr := flag.String("addr", "127.0.0.1:8765", "ACP TCP address")
	cwd := flag.String("cwd", "", "session working directory")
	model := flag.String("model", "", "model/profile id")
	mode := flag.String("mode", "", "session mode")
	prompt := flag.String("prompt", "", "send one prompt and exit")
	permission := flag.String("permission", "prompt", "permission handling: prompt, allow, reject, cancel")
	yolo := flag.Bool("yolo", false, "allow edits and commands without prompting")
	readOnly := flag.Bool("read-only", false, "reject edit and command permission requests")
	showThinking := flag.Bool("show-thinking", false, "print agent_thought_chunk updates")
	hideTools := flag.Bool("hide-tools", false, "hide tool_call/tool_call_update status messages")
	rawUpdates := flag.Bool("raw-updates", false, "print raw non-text session/update payloads")
	version := flag.Bool("version", false, "print version and exit")
	help := flag.Bool("help", false, "show help")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()

	if *help {
		fmt.Fprint(os.Stdout, usage)
		return
	}
	if *version {
		fmt.Println(agent.Version)
		return
	}
	if *yolo {
		*mode = "bypass_permissions"
		*permission = "allow"
	}
	if *readOnly {
		*mode = "default"
		*permission = "reject"
	}
	if *cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			*cwd = wd
		}
	}
	text := strings.TrimSpace(*prompt)
	if text == "" && flag.NArg() > 0 {
		text = strings.Join(flag.Args(), " ")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cli, err := dialClient(ctx, *addr, os.Stdin, os.Stdout, os.Stderr, clientOptions{
		Permission:   strings.ToLower(strings.TrimSpace(*permission)),
		ShowThinking: *showThinking,
		ShowTools:    !*hideTools,
		RawUpdates:   *rawUpdates,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect failed:", err)
		os.Exit(1)
	}
	defer cli.Close()

	if err := cli.Initialize(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "initialize failed:", err)
		os.Exit(1)
	}
	session, err := cli.NewSession(ctx, *cwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "session/new failed:", err)
		os.Exit(1)
	}
	sessionID := session.SessionID
	cli.setSessionState(*addr, *cwd, session)
	fmt.Fprintf(os.Stderr, "session %s cwd=%s\n", sessionID, *cwd)
	if *model != "" {
		if err := cli.SetModel(ctx, sessionID, *model); err != nil {
			fmt.Fprintln(os.Stderr, "set model failed:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "model %s\n", *model)
	}
	if *mode != "" {
		if err := cli.SetMode(ctx, sessionID, *mode); err != nil {
			fmt.Fprintln(os.Stderr, "set mode failed:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "mode %s\n", *mode)
	}

	if text != "" {
		if err := cli.Prompt(ctx, sessionID, text); err != nil {
			fmt.Fprintln(os.Stderr, "prompt failed:", err)
			os.Exit(1)
		}
		return
	}
	if err := repl(ctx, cli); err != nil {
		fmt.Fprintln(os.Stderr, "repl failed:", err)
		os.Exit(1)
	}
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *acp.RPCError   `json:"error,omitempty"`
}

type pendingResponse struct {
	result json.RawMessage
	err    *acp.RPCError
	done   chan struct{}
}

type clientOptions struct {
	Permission   string
	ShowThinking bool
	ShowTools    bool
	RawUpdates   bool
}

type clientState struct {
	Addr      string
	Cwd       string
	SessionID string
	Model     string
	Mode      string
}

type client struct {
	conn   net.Conn
	dec    *json.Decoder
	stdin  *bufio.Reader
	stdout io.Writer
	stderr io.Writer
	opts   clientOptions
	state  clientState

	writeMu sync.Mutex
	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[int64]*pendingResponse
	done    chan struct{}
	once    sync.Once
	err     error
}

func dialClient(ctx context.Context, addr string, stdin io.Reader, stdout, stderr io.Writer, opts clientOptions) (*client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	c := &client{
		conn:    conn,
		dec:     json.NewDecoder(bufio.NewReader(conn)),
		stdin:   bufio.NewReader(stdin),
		stdout:  stdout,
		stderr:  stderr,
		opts:    opts,
		pending: map[int64]*pendingResponse{},
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

func (c *client) Close() error {
	c.close(nil)
	return c.conn.Close()
}

func (c *client) Initialize(ctx context.Context) error {
	var out acp.InitializeResponse
	return c.Call(ctx, "initialize", acp.InitializeParams{
		ProtocolVersion:    acp.ProtocolVersion,
		ClientCapabilities: map[string]any{"terminal": true},
	}, &out)
}

func (c *client) NewSession(ctx context.Context, cwd string) (acp.NewSessionResponse, error) {
	var out acp.NewSessionResponse
	if err := c.Call(ctx, "session/new", acp.NewSessionParams{Cwd: cwd}, &out); err != nil {
		return acp.NewSessionResponse{}, err
	}
	return out, nil
}

func (c *client) SetModel(ctx context.Context, sessionID, model string) error {
	var out any
	if err := c.Call(ctx, "session/set_model", acp.SetModelParams{SessionID: sessionID, ModelID: model}, &out); err != nil {
		return err
	}
	c.mu.Lock()
	c.state.Model = model
	c.mu.Unlock()
	return nil
}

func (c *client) SetMode(ctx context.Context, sessionID, mode string) error {
	var out any
	if err := c.Call(ctx, "session/set_mode", acp.SetModeParams{SessionID: sessionID, ModeID: mode}, &out); err != nil {
		return err
	}
	c.mu.Lock()
	c.state.Mode = mode
	c.mu.Unlock()
	return nil
}

func (c *client) ListSessions(ctx context.Context) (acp.ListSessionsResponse, error) {
	c.mu.Lock()
	cwd := c.state.Cwd
	c.mu.Unlock()
	var out acp.ListSessionsResponse
	err := c.Call(ctx, "session/list", acp.ListSessionsParams{Cwd: cwd}, &out)
	return out, err
}

func (c *client) ResumeSession(ctx context.Context, sessionID string, replay bool) error {
	c.mu.Lock()
	cwd := c.state.Cwd
	c.mu.Unlock()
	var out acp.LoadSessionResponse
	method := "session/resume"
	if replay {
		method = "session/load"
	}
	if err := c.Call(ctx, method, acp.LoadSessionParams{SessionID: sessionID, Cwd: cwd}, &out); err != nil {
		return err
	}
	c.mu.Lock()
	c.state.SessionID = sessionID
	if out.Models != nil {
		c.state.Model = out.Models.CurrentModelID
	}
	if out.Modes != nil {
		c.state.Mode = out.Modes.CurrentModeID
	}
	c.mu.Unlock()
	return nil
}

func (c *client) Prompt(ctx context.Context, sessionID, text string) error {
	var out acp.PromptResponse
	err := c.Call(ctx, "session/prompt", acp.PromptParams{
		SessionID: sessionID,
		MessageID: "msg_" + time.Now().Format("20060102150405.000000000"),
		Prompt:    []acp.ContentBlock{{Type: "text", Text: text}},
	}, &out)
	if err != nil {
		return err
	}
	fmt.Fprintln(c.stdout)
	if out.StopReason != "" && out.StopReason != "end_turn" && out.StopReason != "stop" {
		fmt.Fprintf(c.stderr, "stop: %s\n", out.StopReason)
	}
	return nil
}

func (c *client) setSessionState(addr, cwd string, session acp.NewSessionResponse) {
	state := clientState{Addr: addr, Cwd: cwd, SessionID: session.SessionID}
	if session.Models != nil {
		state.Model = session.Models.CurrentModelID
	}
	if session.Modes != nil {
		state.Mode = session.Modes.CurrentModeID
	}
	c.mu.Lock()
	c.state = state
	c.mu.Unlock()
}

func (c *client) Call(ctx context.Context, method string, params any, result any) error {
	id := c.nextID.Add(1)
	rawID, _ := json.Marshal(id)
	rawParams, err := json.Marshal(params)
	if err != nil {
		return err
	}
	p := &pendingResponse{done: make(chan struct{})}
	c.mu.Lock()
	c.pending[id] = p
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := c.write(rpcMessage{JSONRPC: "2.0", ID: rawID, Method: method, Params: rawParams}); err != nil {
		return err
	}
	select {
	case <-p.done:
		if p.err != nil {
			return p.err
		}
		if result != nil && len(p.result) > 0 {
			return json.Unmarshal(p.result, result)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		if c.err != nil {
			return c.err
		}
		return errors.New("connection closed")
	}
}

func (c *client) readLoop() {
	var terminal error
	for {
		var msg rpcMessage
		if err := c.dec.Decode(&msg); err != nil {
			if !errors.Is(err, io.EOF) {
				terminal = err
			}
			break
		}
		c.dispatch(msg)
	}
	c.close(terminal)
}

func (c *client) close(err error) {
	c.once.Do(func() {
		c.err = err
		close(c.done)
	})
}

func (c *client) dispatch(msg rpcMessage) {
	if msg.Method == "" && len(msg.ID) > 0 {
		var id int64
		if err := json.Unmarshal(msg.ID, &id); err == nil {
			c.mu.Lock()
			p := c.pending[id]
			c.mu.Unlock()
			if p != nil {
				p.result = msg.Result
				p.err = msg.Error
				close(p.done)
			}
		}
		return
	}
	c.handleInbound(msg)
}

func (c *client) handleInbound(msg rpcMessage) {
	isRequest := len(msg.ID) > 0
	respond := func(result any, err error) {
		if !isRequest {
			return
		}
		out := rpcMessage{JSONRPC: "2.0", ID: msg.ID}
		if err != nil {
			out.Error = &acp.RPCError{Code: -32000, Message: err.Error()}
		} else if result != nil {
			raw, mErr := json.Marshal(result)
			if mErr != nil {
				out.Error = &acp.RPCError{Code: -32603, Message: mErr.Error()}
			} else {
				out.Result = raw
			}
		} else {
			out.Result = json.RawMessage(`null`)
		}
		_ = c.write(out)
	}
	switch msg.Method {
	case "session/update":
		var p acp.SessionUpdateParams
		_ = json.Unmarshal(msg.Params, &p)
		c.printUpdate(p)
		respond(nil, nil)
	case "session/request_permission":
		var p acp.RequestPermissionParams
		_ = json.Unmarshal(msg.Params, &p)
		resp, err := c.permission(p)
		respond(resp, err)
	default:
		respond(nil, fmt.Errorf("method not found: %s", msg.Method))
	}
}

func (c *client) write(msg rpcMessage) error {
	msg.JSONRPC = "2.0"
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.conn.Write(body)
	return err
}

func (c *client) permission(p acp.RequestPermissionParams) (acp.RequestPermissionResponse, error) {
	title, _ := p.ToolCall["title"].(string)
	if title == "" {
		title = "tool permission"
	}
	c.mu.Lock()
	mode := c.opts.Permission
	c.mu.Unlock()
	switch mode {
	case "", "prompt":
		fmt.Fprintf(c.stderr, "\npermission requested: %s\n", title)
		fmt.Fprint(c.stderr, "allow? [y/N] ")
		line, err := c.stdin.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return acp.RequestPermissionResponse{}, err
		}
		if strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes") {
			mode = "allow"
		} else {
			mode = "reject"
		}
	}
	switch mode {
	case "allow", "y", "yes":
		return acp.RequestPermissionResponse{Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: "allow"}}, nil
	case "cancel", "cancelled":
		return acp.RequestPermissionResponse{Outcome: acp.PermissionOutcome{Outcome: "cancelled"}}, nil
	default:
		return acp.RequestPermissionResponse{Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: "reject"}}, nil
	}
}

func (c *client) printUpdate(p acp.SessionUpdateParams) {
	update := p.Update
	switch update["sessionUpdate"] {
	case "agent_message_chunk":
		if text := updateText(update); text != "" {
			fmt.Fprint(c.stdout, text)
			flush(c.stdout)
		}
	case "agent_thought_chunk":
		c.mu.Lock()
		show := c.opts.ShowThinking
		c.mu.Unlock()
		if show {
			if text := updateText(update); text != "" {
				fmt.Fprintf(c.stderr, "\n[thinking] %s\n", text)
				flush(c.stderr)
			}
		}
	case "tool_call":
		c.mu.Lock()
		show := c.opts.ShowTools
		c.mu.Unlock()
		if !show {
			return
		}
		title, _ := update["title"].(string)
		status, _ := update["status"].(string)
		if title != "" {
			fmt.Fprintf(c.stderr, "\n[tool:%s] %s\n", firstNonEmpty(status, "start"), title)
			flush(c.stderr)
		}
	case "tool_call_update":
		c.mu.Lock()
		show := c.opts.ShowTools
		c.mu.Unlock()
		if !show {
			return
		}
		status, _ := update["status"].(string)
		if status != "" {
			fmt.Fprintf(c.stderr, "[tool:%s]\n", status)
			flush(c.stderr)
		}
	case "session_info_update":
		return
	default:
		c.mu.Lock()
		rawUpdates := c.opts.RawUpdates
		c.mu.Unlock()
		if rawUpdates {
			raw, _ := json.Marshal(update)
			fmt.Fprintf(c.stderr, "\n[update] %s\n", raw)
			flush(c.stderr)
		}
	}
}

func flush(w io.Writer) {
	if f, ok := w.(interface{ Flush() error }); ok {
		_ = f.Flush()
	}
}

func updateText(update map[string]any) string {
	content, ok := update["content"].(map[string]any)
	if !ok {
		return ""
	}
	if text, _ := content["text"].(string); text != "" {
		return text
	}
	nested, ok := content["content"].(map[string]any)
	if !ok {
		return ""
	}
	text, _ := nested["text"].(string)
	return text
}

func repl(ctx context.Context, c *client) error {
	for {
		fmt.Fprint(c.stderr, "\nacp> ")
		line, err := c.stdin.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch {
		case line == "/help":
			c.printHelp()
		case line == "/status":
			c.printStatus()
		case line == "/sessions":
			c.commandSessions(ctx)
		case line == "/exit" || line == "/quit":
			return nil
		case line == "/model" || strings.HasPrefix(line, "/model "):
			c.commandModel(ctx, strings.TrimSpace(strings.TrimPrefix(line, "/model")))
		case line == "/mode" || strings.HasPrefix(line, "/mode "):
			c.commandMode(ctx, strings.TrimSpace(strings.TrimPrefix(line, "/mode")))
		case strings.HasPrefix(line, "/resume "):
			c.commandResume(ctx, strings.TrimSpace(strings.TrimPrefix(line, "/resume ")), false)
		case strings.HasPrefix(line, "/load "):
			c.commandResume(ctx, strings.TrimSpace(strings.TrimPrefix(line, "/load ")), true)
		case line == "/permission" || strings.HasPrefix(line, "/permission "):
			c.commandPermission(strings.TrimSpace(strings.TrimPrefix(line, "/permission")))
		case line == "/thinking" || strings.HasPrefix(line, "/thinking "):
			c.commandBool("thinking", strings.TrimSpace(strings.TrimPrefix(line, "/thinking")), &c.opts.ShowThinking)
		case line == "/tools" || strings.HasPrefix(line, "/tools "):
			c.commandBool("tools", strings.TrimSpace(strings.TrimPrefix(line, "/tools")), &c.opts.ShowTools)
		case line == "/raw" || strings.HasPrefix(line, "/raw "):
			c.commandBool("raw", strings.TrimSpace(strings.TrimPrefix(line, "/raw")), &c.opts.RawUpdates)
		default:
			c.mu.Lock()
			sessionID := c.state.SessionID
			c.mu.Unlock()
			if err := c.Prompt(ctx, sessionID, line); err != nil {
				return err
			}
		}
	}
}

func (c *client) printHelp() {
	fmt.Fprint(c.stderr, `commands:
  /status
  /sessions
  /resume SESSION_ID
  /load SESSION_ID
  /model [MODEL]
  /mode [MODE]
  /permission [prompt|allow|reject|cancel]
  /thinking [on|off]
  /tools [on|off]
  /raw [on|off]
  /quit
`)
}

func (c *client) printStatus() {
	c.mu.Lock()
	state := c.state
	opts := c.opts
	c.mu.Unlock()
	fmt.Fprintf(c.stderr, "addr=%s\nsession=%s\ncwd=%s\nmodel=%s\nmode=%s\npermission=%s\nthinking=%v\ntools=%v\nraw=%v\n",
		state.Addr, state.SessionID, state.Cwd, state.Model, state.Mode,
		firstNonEmpty(opts.Permission, "prompt"), opts.ShowThinking, opts.ShowTools, opts.RawUpdates)
}

func (c *client) commandSessions(ctx context.Context) {
	list, err := c.ListSessions(ctx)
	if err != nil {
		fmt.Fprintln(c.stderr, "session/list failed:", err)
		return
	}
	if len(list.Sessions) == 0 {
		fmt.Fprintln(c.stderr, "no sessions")
		return
	}
	for _, s := range list.Sessions {
		title := ""
		if s.Title != nil {
			title = *s.Title
		}
		fmt.Fprintf(c.stderr, "%s\t%s\t%s\t%s\n", s.SessionID, s.UpdatedAt, s.Cwd, title)
	}
}

func (c *client) commandResume(ctx context.Context, sessionID string, replay bool) {
	if sessionID == "" {
		if replay {
			fmt.Fprintln(c.stderr, "usage: /load SESSION_ID")
		} else {
			fmt.Fprintln(c.stderr, "usage: /resume SESSION_ID")
		}
		return
	}
	if err := c.ResumeSession(ctx, sessionID, replay); err != nil {
		if replay {
			fmt.Fprintln(c.stderr, "session/load failed:", err)
		} else {
			fmt.Fprintln(c.stderr, "session/resume failed:", err)
		}
		return
	}
	if replay {
		fmt.Fprintln(c.stderr, "loaded", sessionID)
	} else {
		fmt.Fprintln(c.stderr, "resumed", sessionID)
	}
}

func (c *client) commandModel(ctx context.Context, model string) {
	if model == "" {
		c.mu.Lock()
		cur := c.state.Model
		c.mu.Unlock()
		fmt.Fprintln(c.stderr, "model", cur)
		return
	}
	c.mu.Lock()
	sessionID := c.state.SessionID
	c.mu.Unlock()
	if err := c.SetModel(ctx, sessionID, model); err != nil {
		fmt.Fprintln(c.stderr, "set model failed:", err)
		return
	}
	fmt.Fprintln(c.stderr, "model", model)
}

func (c *client) commandMode(ctx context.Context, mode string) {
	if mode == "" {
		c.mu.Lock()
		cur := c.state.Mode
		c.mu.Unlock()
		fmt.Fprintln(c.stderr, "mode", cur)
		return
	}
	c.mu.Lock()
	sessionID := c.state.SessionID
	c.mu.Unlock()
	if err := c.SetMode(ctx, sessionID, mode); err != nil {
		fmt.Fprintln(c.stderr, "set mode failed:", err)
		return
	}
	fmt.Fprintln(c.stderr, "mode", mode)
}

func (c *client) commandPermission(mode string) {
	if mode == "" {
		c.mu.Lock()
		permission := c.opts.Permission
		c.mu.Unlock()
		fmt.Fprintln(c.stderr, "permission", firstNonEmpty(permission, "prompt"))
		return
	}
	switch strings.ToLower(mode) {
	case "prompt", "allow", "reject", "cancel", "cancelled":
		c.mu.Lock()
		c.opts.Permission = strings.ToLower(mode)
		permission := c.opts.Permission
		c.mu.Unlock()
		fmt.Fprintln(c.stderr, "permission", permission)
	default:
		fmt.Fprintln(c.stderr, "permission must be prompt, allow, reject, or cancel")
	}
}

func (c *client) commandBool(name, value string, target *bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if value == "" {
		fmt.Fprintf(c.stderr, "%s %v\n", name, *target)
		return
	}
	switch strings.ToLower(value) {
	case "on", "true", "1", "yes":
		*target = true
	case "off", "false", "0", "no":
		*target = false
	case "toggle":
		*target = !*target
	default:
		fmt.Fprintf(c.stderr, "%s must be on, off, or toggle\n", name)
		return
	}
	fmt.Fprintf(c.stderr, "%s %v\n", name, *target)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
