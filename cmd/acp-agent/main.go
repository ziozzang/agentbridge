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
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ziozzang/agentbridge/internal/acp"
	"github.com/ziozzang/agentbridge/internal/agent"
	"github.com/ziozzang/agentbridge/internal/harness/filecontext"
	"github.com/ziozzang/agentbridge/internal/protocol/systemprompt"
	"github.com/ziozzang/agentbridge/internal/tools/clienttools"
)

const usage = `acp-agent - terminal ACP client for AgentBridge

Usage:
  acp-agent --addr 127.0.0.1:8765 --model glm-5.1
  acp-agent --addr 127.0.0.1:8765 --model codex-agent -p "inspect this repo"
  acp-agent --addr 127.0.0.1:8765 "list files in the current directory"

Flags:
  --addr ADDR          ACP TCP address (default "127.0.0.1:8765")
  --cwd DIR            session working directory (default current directory)
  --model MODEL        model/profile id to select after session creation
  --mode MODE          session mode: default, accept_edits, bypass_permissions
  -p, --prompt TEXT    send one prompt and exit
  --permission MODE    permission handling: prompt, allow, reject, cancel (default prompt)
  --yolo               shorthand for --mode bypass_permissions --permission allow
  --read-only          shorthand for --mode default --permission reject
  --show-thinking      print ACP agent_thought_chunk updates to stderr
  --hide-tools         hide ACP tool_call/tool_call_update status messages
  --raw-updates        print raw non-text session/update payloads to stderr
  --plain              use the minimal line-oriented fallback
  --json-events        print ACP UI events as newline-delimited JSON for debugging
  --json               alias for --json-events
  --version            print version and exit

Interactive commands:
  /help                show commands
  /status              show current connection/session settings
  /sessions            list sessions for the current cwd
  /resume SESSION_ID   resume a persisted session without replay
  /session-load ID     load a persisted session and replay messages
  /save NAME           save a session checkpoint
  /list                list session checkpoints
  /load NAME|ID        roll back to a session checkpoint
  /compact [TOKENS]    compact the current session transcript
  /context             show estimated context usage
  /attach PATH [...]    extract local files and attach them to the next prompt
  /files               list queued file attachments
  /clear-files         clear queued file attachments
  /structure           show session/context/attachment structure
  /lua FILE [args...]  run a local Lua controller script
  /goal [CMD]          local Lua goal harness: status, set TEXT, run, clear
  /new                 start a new session in the current cwd
  /stop                cancel the current session prompt
  /queue               show queued prompts waiting behind current prompt
  /skill COMMAND       run server-side skill commands
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
	promptShort := flag.String("p", "", "send one prompt and exit")
	permission := flag.String("permission", "prompt", "permission handling: prompt, allow, reject, cancel")
	yolo := flag.Bool("yolo", false, "allow edits and commands without prompting")
	readOnly := flag.Bool("read-only", false, "reject edit and command permission requests")
	showThinking := flag.Bool("show-thinking", false, "print agent_thought_chunk updates")
	hideTools := flag.Bool("hide-tools", false, "hide tool_call/tool_call_update status messages")
	rawUpdates := flag.Bool("raw-updates", false, "print raw non-text session/update payloads")
	plain := flag.Bool("plain", false, "use the minimal line-oriented fallback")
	jsonEvents := flag.Bool("json-events", false, "print ACP UI events as newline-delimited JSON")
	jsonEventsAlias := flag.Bool("json", false, "alias for --json-events")
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
	if text == "" {
		text = strings.TrimSpace(*promptShort)
	}
	if text == "" && flag.NArg() > 0 {
		text = strings.Join(flag.Args(), " ")
	}
	debugJSON := *jsonEvents || *jsonEventsAlias
	interactiveTUI := text == "" && !*plain && !debugJSON && isTerminalWriter(os.Stderr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	cli, err := dialClient(ctx, *addr, os.Stdin, os.Stdout, os.Stderr, clientOptions{
		Permission:   strings.ToLower(strings.TrimSpace(*permission)),
		ShowThinking: *showThinking || interactiveTUI,
		ShowTools:    !*hideTools,
		RawUpdates:   *rawUpdates,
		LegacyUI:     !interactiveTUI && !debugJSON,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect failed:", err)
		os.Exit(1)
	}
	defer cli.Close()
	var jsonSink *jsonEventSink
	if debugJSON {
		jsonSink = startJSONEventSink(cli, os.Stdout)
		defer jsonSink.Close()
	}
	if !interactiveTUI {
		go func() {
			for {
				<-sigCh
				if cli.Interrupt(ctx) {
					fmt.Fprintln(os.Stderr, "\ninterrupt: cancelled current session prompt")
					continue
				}
				cancel()
				fmt.Fprintln(os.Stderr, "\ninterrupted")
				_ = cli.Close()
				os.Exit(130)
			}
		}()
	}

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
	if !interactiveTUI {
		fmt.Fprintf(os.Stderr, "session %s cwd=%s\n", sessionID, *cwd)
	}
	if *model != "" {
		if err := cli.SetModel(ctx, sessionID, *model); err != nil {
			fmt.Fprintln(os.Stderr, "set model failed:", err)
			os.Exit(1)
		}
		if !interactiveTUI {
			fmt.Fprintf(os.Stderr, "model %s\n", *model)
		}
	}
	if *mode != "" {
		if err := cli.SetMode(ctx, sessionID, *mode); err != nil {
			fmt.Fprintln(os.Stderr, "set mode failed:", err)
			os.Exit(1)
		}
		if !interactiveTUI {
			fmt.Fprintf(os.Stderr, "mode %s\n", *mode)
		}
	}

	if text != "" {
		if err := cli.Prompt(ctx, sessionID, text); err != nil {
			fmt.Fprintln(os.Stderr, "prompt failed:", err)
			os.Exit(1)
		}
		return
	}
	if debugJSON {
		if err := jsonEventRepl(ctx, cli); err != nil {
			fmt.Fprintln(os.Stderr, "json repl failed:", err)
			os.Exit(1)
		}
		return
	}
	if interactiveTUI {
		if err := runBubbleTUI(ctx, cli); err != nil {
			fmt.Fprintln(os.Stderr, "tui failed:", err)
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
	LegacyUI     bool
}

type clientState struct {
	Addr      string
	Cwd       string
	SessionID string
	Model     string
	Mode      string
	Worker    workerState
	Context   contextState
	Limits    limitState
	Busy      bool
	QueueLen  int
	Tools     int
	Subagents int
	LastTool  string
}

type workerState struct {
	ID           string
	Kind         string
	Capabilities []string
	Permission   string
	Cancellable  bool
}

type contextState struct {
	Tokens       int
	Window       int
	UsedPercent  float64
	LeftPercent  float64
	Messages     int
	Checkpoints  int
	CacheEpoch   int
	CompactionOn bool
}

type limitState struct {
	FiveHourPercent float64
	WeeklyPercent   float64
	MonthlyPercent  float64
	Refreshing      bool
}

type attachment struct {
	Resource filecontext.Resource
}

type client struct {
	conn          net.Conn
	dec           *json.Decoder
	stdin         *bufio.Reader
	stdout        io.Writer
	stderr        io.Writer
	stream        *streamBuffer
	opts          clientOptions
	state         clientState
	files         []attachment
	allowCommands map[string]bool
	promptQueue   []string
	activeTools   map[string]string
	activeAgents  map[string]string
	thinkingPlain bool
	events        chan uiEvent
	promptCtx     context.Context
	promptCancel  context.CancelFunc
	choiceCancel  context.CancelFunc

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
	var stream *streamBuffer
	if opts.LegacyUI {
		stream = newStreamBuffer(stdout)
	}
	c := &client{
		conn:          conn,
		dec:           json.NewDecoder(bufio.NewReader(conn)),
		stdin:         bufio.NewReader(stdin),
		stdout:        stdout,
		stderr:        stderr,
		stream:        stream,
		opts:          opts,
		allowCommands: map[string]bool{},
		activeTools:   map[string]string{},
		activeAgents:  map[string]string{},
		pending:       map[int64]*pendingResponse{},
		done:          make(chan struct{}),
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
		ClientCapabilities: c.capabilities(),
	}, &out)
}

func (c *client) capabilities() map[string]any {
	return map[string]any{
		"terminal":     true,
		"lua":          true,
		"clientRunLua": true,
		"clientTools": []clienttools.AdvertisedTool{
			{
				Name:        "run_lua",
				Description: "Run a Lua orchestration script inside the ACP terminal client. Use this when client-side placement is required for attaching files, steering CLI flow, transforming text, running CLI commands, or coordinating local orchestration factors.",
				Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "code": {"type": "string", "description": "Lua source code to execute on the client."},
    "path": {"type": "string", "description": "Optional client-local Lua file path. Relative paths resolve against the client session cwd."},
    "args": {"type": "array", "items": {"type": "string"}}
  }
}`),
			},
			{
				Name:        "run_command",
				Description: "Execute a shell command via 'sh -c' in the ACP terminal client's session working directory after CLI-side permission handling. Use this for local shell execution; the AgentBridge server does not run shell commands.",
				Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "The shell command line to execute in the client session cwd."}
  },
  "required": ["command"]
}`),
			},
		},
	}
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
	c.emitState()
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
	c.emitState()
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
	c.emitState()
	return nil
}

func (c *client) Cancel(ctx context.Context, sessionID string) error {
	return c.Notify(ctx, "session/cancel", acp.CancelParams{SessionID: sessionID})
}

func (c *client) Interrupt(ctx context.Context) bool {
	c.mu.Lock()
	sessionID := c.state.SessionID
	busy := c.state.Busy
	cancel := c.promptCancel
	choiceCancel := c.choiceCancel
	c.mu.Unlock()
	if !busy {
		if choiceCancel != nil {
			choiceCancel()
		}
		return false
	}
	if choiceCancel != nil {
		choiceCancel()
	}
	if cancel != nil {
		cancel()
	}
	if sessionID != "" {
		_ = c.Cancel(ctx, sessionID)
	}
	return true
}

func (c *client) Prompt(ctx context.Context, sessionID, text string) error {
	promptCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.state.Busy = true
	c.promptCtx = promptCtx
	c.promptCancel = cancel
	c.mu.Unlock()
	c.emitState()
	defer func() {
		cancel()
		c.finishThinkingOutput()
		c.mu.Lock()
		c.state.Busy = false
		c.promptCtx = nil
		c.promptCancel = nil
		c.mu.Unlock()
		c.emitState()
	}()
	c.emit(uiUserEvent{Text: text})
	if c.stream != nil {
		c.stream.start()
		defer c.stream.finish()
	}
	blocks := []acp.ContentBlock{{Type: "text", Text: text}}
	files := c.takeAttachments()
	if len(files) > 0 {
		blocks = make([]acp.ContentBlock, 0, len(files)+1)
		for _, f := range files {
			res := f.Resource
			blocks = append(blocks, acp.ContentBlock{
				Type: "resource",
				Resource: &acp.EmbeddedResource{
					URI:      "file://" + res.Path,
					MimeType: res.MimeType,
					Text:     renderAttachedResource(res),
				},
			})
		}
		blocks = append(blocks, acp.ContentBlock{Type: "text", Text: text})
	}
	var out acp.PromptResponse
	err := c.Call(promptCtx, "session/prompt", acp.PromptParams{
		SessionID: sessionID,
		MessageID: "msg_" + time.Now().Format("20060102150405.000000000"),
		Prompt:    blocks,
	}, &out)
	if err != nil {
		c.restoreAttachments(files)
		return err
	}
	if c.events == nil {
		fmt.Fprintln(c.stdout)
	}
	if out.StopReason != "" && out.StopReason != "end_turn" && out.StopReason != "stop" {
		if c.emitInfo("stop", out.StopReason) {
			return nil
		}
		fmt.Fprintf(c.stderr, "stop: %s\n", out.StopReason)
	}
	return nil
}

func (c *client) SubmitPrompt(ctx context.Context, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	c.mu.Lock()
	if c.state.Busy {
		c.promptQueue = append(c.promptQueue, text)
		c.state.QueueLen = len(c.promptQueue)
		queued := c.state.QueueLen
		c.mu.Unlock()
		c.emitState()
		if c.emitInfo("queued", fmt.Sprintf("%d prompt(s) waiting", queued)) {
			return
		}
		fmt.Fprintf(c.stderr, "queued %d prompt(s)\n", queued)
		return
	}
	c.mu.Unlock()
	go c.runPromptQueue(ctx, text)
}

func (c *client) runPromptQueue(ctx context.Context, first string) {
	next := first
	for strings.TrimSpace(next) != "" {
		c.mu.Lock()
		sessionID := c.state.SessionID
		c.mu.Unlock()
		if err := c.Prompt(ctx, sessionID, next); err != nil {
			if errors.Is(err, context.Canceled) {
				c.emitInfo("cancelled", "current turn cancelled")
			} else {
				c.emitError("prompt failed: " + err.Error())
			}
			if errors.Is(err, context.Canceled) {
				// Cancellation is user-directed; keep the terminal ready for the next input.
			} else {
				fmt.Fprintln(c.stderr, "prompt failed:", err)
			}
		}
		c.mu.Lock()
		if len(c.promptQueue) == 0 {
			c.state.QueueLen = 0
			c.mu.Unlock()
			return
		}
		next = c.promptQueue[0]
		c.promptQueue = c.promptQueue[1:]
		c.state.QueueLen = len(c.promptQueue)
		c.mu.Unlock()
		c.emitState()
	}
}

func renderAttachedResource(res filecontext.Resource) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<attached_file name=%q path=%q mime=%q size=%d truncated=%v>\n", res.Name, res.Path, res.MimeType, res.Size, res.Truncated)
	b.WriteString(res.Text)
	b.WriteString("\n</attached_file>")
	return b.String()
}

func (c *client) takeAttachments() []attachment {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := append([]attachment(nil), c.files...)
	c.files = nil
	return out
}

func (c *client) restoreAttachments(files []attachment) {
	if len(files) == 0 {
		return
	}
	c.mu.Lock()
	c.files = append(files, c.files...)
	c.mu.Unlock()
}

func (c *client) setSessionState(addr, cwd string, session acp.NewSessionResponse) {
	state := clientState{Addr: addr, Cwd: cwd, SessionID: session.SessionID, Worker: workerStateForOptions(c.opts)}
	if session.Models != nil {
		state.Model = session.Models.CurrentModelID
	}
	if session.Modes != nil {
		state.Mode = session.Modes.CurrentModeID
	}
	c.mu.Lock()
	c.state = state
	c.mu.Unlock()
	c.emitState()
}

func (c *client) snapshotState() (clientState, clientOptions) {
	c.mu.Lock()
	state := c.state
	opts := c.opts
	state.Worker = workerStateForOptions(opts)
	c.mu.Unlock()
	return state, opts
}

func workerStateForOptions(opts clientOptions) workerState {
	return workerState{
		ID:           "acp-agent:local",
		Kind:         "terminal",
		Capabilities: []string{"run_command", "run_lua", "attach_files", "cli_memory", "queue", "goal"},
		Permission:   firstNonEmpty(opts.Permission, "prompt"),
		Cancellable:  true,
	}
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

func (c *client) Notify(ctx context.Context, method string, params any) error {
	rawParams, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if err := c.write(rpcMessage{JSONRPC: "2.0", Method: method, Params: rawParams}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		if c.err != nil {
			return c.err
		}
		return errors.New("connection closed")
	default:
		return nil
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
	case "client/run_lua":
		var p runLuaParams
		_ = json.Unmarshal(msg.Params, &p)
		go func() {
			resp, err := c.runLua(c.activeRequestContext(), p)
			c.respond(msg.ID, resp, err)
		}()
	case "client/call_tool":
		var p clientToolCallParams
		_ = json.Unmarshal(msg.Params, &p)
		go func() {
			resp, err := c.callClientTool(c.activeRequestContext(), p)
			c.respond(msg.ID, resp, err)
		}()
	default:
		respond(nil, fmt.Errorf("method not found: %s", msg.Method))
	}
}

func (c *client) activeRequestContext() context.Context {
	c.mu.Lock()
	ctx := c.promptCtx
	c.mu.Unlock()
	if ctx != nil {
		return ctx
	}
	return context.Background()
}

func (c *client) respond(id json.RawMessage, result any, err error) {
	out := rpcMessage{JSONRPC: "2.0", ID: id}
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
	command := commandFromToolCall(p.ToolCall)
	c.mu.Lock()
	mode := c.opts.Permission
	if command != "" && c.allowCommands[command] {
		c.mu.Unlock()
		return acp.RequestPermissionResponse{Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: "allow"}}, nil
	}
	c.mu.Unlock()
	switch mode {
	case "", "prompt":
		options := []choiceOption{{Key: "1", Label: "yes"}}
		if command != "" {
			options = append(options, choiceOption{Key: "2", Label: "yes (same command)"})
		}
		options = append(options,
			choiceOption{Key: "3", Label: "no"},
			choiceOption{Key: "0", Label: "yolo"},
		)
		detail := ""
		if command != "" {
			detail = "command: " + command
		}
		choice, err := c.choose("permission requested: "+title, detail, options)
		if err != nil {
			return acp.RequestPermissionResponse{}, err
		}
		switch choice {
		case "1", "y", "yes":
			mode = "allow"
		case "2":
			if command != "" {
				c.rememberAllowedCommand(command)
				mode = "allow"
			} else {
				mode = "reject"
			}
		case "0":
			c.setPermissionMode("allow")
			mode = "allow"
		default:
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

func commandFromToolCall(toolCall map[string]any) string {
	raw, ok := toolCall["rawInput"].(map[string]any)
	if !ok {
		return ""
	}
	return strings.TrimSpace(stringFromAny(raw["command"]))
}

func (c *client) rememberAllowedCommand(command string) {
	command = strings.TrimSpace(command)
	if command == "" {
		return
	}
	c.mu.Lock()
	if c.allowCommands == nil {
		c.allowCommands = map[string]bool{}
	}
	c.allowCommands[command] = true
	c.mu.Unlock()
}

func (c *client) commandAllowed(command string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.allowCommands != nil && c.allowCommands[strings.TrimSpace(command)]
}

func (c *client) setPermissionMode(mode string) {
	c.mu.Lock()
	c.opts.Permission = mode
	c.mu.Unlock()
}

func repl(ctx context.Context, c *client) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		fmt.Fprint(c.stderr, "\nacp> ")
		line, err := c.stdin.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				fmt.Fprintln(c.stderr)
				return nil
			}
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "/exit" || line == "/quit" {
			return nil
		}
		if err := c.runCommand(ctx, line); err != nil {
			return err
		}
	}
}

func (c *client) runCommand(ctx context.Context, line string) error {
	if strings.HasPrefix(line, "/") && !serverPromptCommandLine(line) {
		c.emit(uiCommandEvent{Text: line})
	}
	switch {
	case line == "/help":
		c.printHelp()
	case line == "/status":
		c.printStatus()
	case line == "/sessions":
		c.commandSessions(ctx)
	case line == "/list":
		c.commandServerPrompt(ctx, "/btw list")
	case line == "/new":
		c.commandNew(ctx)
	case line == "/stop":
		c.commandStop(ctx)
	case line == "/queue":
		c.printQueue()
	case line == "/compact" || strings.HasPrefix(line, "/compact "):
		c.commandServerPrompt(ctx, line)
	case line == "/context":
		c.commandServerPrompt(ctx, line)
	case line == "/files":
		c.commandFiles()
	case line == "/clear-files":
		c.commandClearFiles()
	case line == "/structure":
		c.commandStructure()
	case line == "/attach" || strings.HasPrefix(line, "/attach "):
		c.commandAttach(strings.TrimSpace(strings.TrimPrefix(line, "/attach")))
	case line == "/lua" || strings.HasPrefix(line, "/lua "):
		c.commandLua(ctx, strings.Fields(line))
	case line == "/goal" || strings.HasPrefix(line, "/goal "):
		c.commandGoal(ctx, strings.TrimSpace(strings.TrimPrefix(line, "/goal")))
	case line == "/save" || strings.HasPrefix(line, "/save "):
		name := strings.TrimSpace(strings.TrimPrefix(line, "/save"))
		if name == "" {
			if c.emitInfo("save", "usage: /save NAME") {
				return nil
			}
			fmt.Fprintln(c.stderr, "usage: /save NAME")
			return nil
		}
		c.commandServerPrompt(ctx, "/btw mark "+name)
	case line == "/load" || strings.HasPrefix(line, "/load "):
		name := strings.TrimSpace(strings.TrimPrefix(line, "/load"))
		if name == "" {
			if c.emitInfo("load", "usage: /load CHECKPOINT_NAME_OR_ID") {
				return nil
			}
			fmt.Fprintln(c.stderr, "usage: /load CHECKPOINT_NAME_OR_ID")
			return nil
		}
		c.commandServerPrompt(ctx, "/btw back "+name)
	case line == "/model" || strings.HasPrefix(line, "/model "):
		c.commandModel(ctx, strings.TrimSpace(strings.TrimPrefix(line, "/model")))
	case line == "/mode" || strings.HasPrefix(line, "/mode "):
		c.commandMode(ctx, strings.TrimSpace(strings.TrimPrefix(line, "/mode")))
	case strings.HasPrefix(line, "/resume "):
		c.commandResume(ctx, strings.TrimSpace(strings.TrimPrefix(line, "/resume ")), false)
	case strings.HasPrefix(line, "/session-load "):
		c.commandResume(ctx, strings.TrimSpace(strings.TrimPrefix(line, "/session-load ")), true)
	case line == "/permission" || strings.HasPrefix(line, "/permission "):
		c.commandPermission(strings.TrimSpace(strings.TrimPrefix(line, "/permission")))
	case line == "/thinking" || strings.HasPrefix(line, "/thinking "):
		c.commandBool("thinking", strings.TrimSpace(strings.TrimPrefix(line, "/thinking")), &c.opts.ShowThinking)
	case line == "/tools" || strings.HasPrefix(line, "/tools "):
		c.commandBool("tools", strings.TrimSpace(strings.TrimPrefix(line, "/tools")), &c.opts.ShowTools)
	case line == "/raw" || strings.HasPrefix(line, "/raw "):
		c.commandBool("raw", strings.TrimSpace(strings.TrimPrefix(line, "/raw")), &c.opts.RawUpdates)
	default:
		c.SubmitPrompt(ctx, line)
	}
	return nil
}

func serverPromptCommandLine(line string) bool {
	switch {
	case line == "/list":
		return true
	case line == "/compact" || strings.HasPrefix(line, "/compact "):
		return true
	case line == "/context":
		return true
	case line == "/save" || strings.HasPrefix(line, "/save "):
		return true
	case line == "/load" || strings.HasPrefix(line, "/load "):
		return true
	default:
		return false
	}
}

func (c *client) printHelp() {
	body := `commands:
  /status
  /sessions
  /resume SESSION_ID
  /session-load SESSION_ID
  /save NAME
  /list
  /load CHECKPOINT_NAME_OR_ID
  /compact [TARGET_TOKENS]
  /context
  /attach PATH [...]
  /files
  /clear-files
  /structure
  /lua FILE [args...]
  /goal [status|set TEXT|run|clear]
  /new
  /stop
  /queue
  /skill list|status|clear|NAME
  /model [MODEL]
  /mode [MODE]
  /permission [prompt|allow|reject|cancel]
  /thinking [on|off]
  /tools [on|off]
  /raw [on|off]
  /quit
`
	if c.emitInfo("help", strings.TrimRight(body, "\n")) {
		return
	}
	fmt.Fprint(c.stderr, body)
}

func (c *client) commandAttach(args string) {
	if args == "" {
		if c.emitInfo("attach", "usage: /attach PATH [...]") {
			return
		}
		fmt.Fprintln(c.stderr, "usage: /attach PATH [...]")
		return
	}
	var b strings.Builder
	var added int
	for _, path := range strings.Fields(args) {
		res, err := c.attachPath(path)
		if err != nil {
			fmt.Fprintf(&b, "attach failed for %s: %v\n", path, err)
			if c.events == nil {
				fmt.Fprintf(c.stderr, "attach failed for %s: %v\n", path, err)
			}
			continue
		}
		added++
		fmt.Fprintf(&b, "attached %s chars=%d truncated=%v\n", res.Path, len(res.Text), res.Truncated)
		if c.events == nil {
			fmt.Fprintf(c.stderr, "attached %s chars=%d truncated=%v\n", res.Path, len(res.Text), res.Truncated)
		}
	}
	if added == 0 {
		if c.emitInfo("attach", strings.TrimRight(firstNonEmpty(b.String(), "no files attached"), "\n")) {
			return
		}
		fmt.Fprintln(c.stderr, "no files attached")
		return
	}
	if c.emitInfo("attach", strings.TrimRight(b.String(), "\n")) {
		return
	}
}

func (c *client) attachPath(path string) (filecontext.Resource, error) {
	if !filepath.IsAbs(path) {
		c.mu.Lock()
		cwd := c.state.Cwd
		c.mu.Unlock()
		path = filepath.Join(cwd, path)
	}
	res, err := filecontext.Extract(path)
	if err != nil {
		return filecontext.Resource{}, err
	}
	c.mu.Lock()
	c.files = append(c.files, attachment{Resource: res})
	c.mu.Unlock()
	return res, nil
}

func (c *client) commandFiles() {
	c.mu.Lock()
	files := append([]attachment(nil), c.files...)
	c.mu.Unlock()
	if len(files) == 0 {
		if c.emitInfo("files", "queued files: none") {
			return
		}
		fmt.Fprintln(c.stderr, "queued files: none")
		return
	}
	var b strings.Builder
	b.WriteString("queued files:\n")
	if c.events == nil {
		fmt.Fprintln(c.stderr, "queued files:")
	}
	for i, f := range files {
		res := f.Resource
		fmt.Fprintf(&b, "%d. %s mime=%s chars=%d truncated=%v\n", i+1, res.Path, res.MimeType, len(res.Text), res.Truncated)
		if c.events == nil {
			fmt.Fprintf(c.stderr, "%d. %s mime=%s chars=%d truncated=%v\n", i+1, res.Path, res.MimeType, len(res.Text), res.Truncated)
		}
	}
	c.emitInfo("files", strings.TrimRight(b.String(), "\n"))
}

func (c *client) commandClearFiles() {
	c.mu.Lock()
	n := len(c.files)
	c.files = nil
	c.mu.Unlock()
	if c.emitInfo("files", fmt.Sprintf("cleared %d queued files", n)) {
		return
	}
	fmt.Fprintf(c.stderr, "cleared %d queued files\n", n)
}

func (c *client) commandStructure() {
	if c.emitInfo("structure", strings.TrimRight(c.structureString(), "\n")) {
		return
	}
	fmt.Fprint(c.stderr, c.structureString())
}

func (c *client) structureString() string {
	state, _ := c.snapshotState()
	c.mu.Lock()
	files := append([]attachment(nil), c.files...)
	c.mu.Unlock()
	contextFile := systemprompt.ProjectContextPath(state.Cwd)
	if contextFile == "" {
		contextFile = "(none)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "session:\n  id: %s\n  cwd: %s\n  model: %s\n  mode: %s\ncontext:\n  project_file: %s\n  queued_files: %d\n",
		state.SessionID, state.Cwd, state.Model, state.Mode, contextFile, len(files))
	for i, f := range files {
		res := f.Resource
		fmt.Fprintf(&b, "  %d. %s (%s, chars=%d, truncated=%v)\n", i+1, res.Path, res.MimeType, len(res.Text), res.Truncated)
	}
	fmt.Fprintf(&b, "worker:\n  id: %s\n  kind: %s\n  capabilities: %s\n  permission: %s\n  cancellable: %v\n",
		state.Worker.ID, state.Worker.Kind, strings.Join(state.Worker.Capabilities, ", "), state.Worker.Permission, state.Worker.Cancellable)
	return b.String()
}

func (c *client) commandLua(ctx context.Context, fields []string) {
	if len(fields) < 2 {
		if c.emitInfo("lua", "usage: /lua FILE [args...]") {
			return
		}
		fmt.Fprintln(c.stderr, "usage: /lua FILE [args...]")
		return
	}
	result, err := c.runLua(ctx, runLuaParams{Path: fields[1], Args: fields[2:]})
	if err != nil {
		if c.emitError("lua failed: " + err.Error()) {
			return
		}
		fmt.Fprintln(c.stderr, "lua failed:", err)
		return
	}
	if strings.TrimSpace(result.Output) != "" {
		if c.emitInfo("lua", strings.TrimRight(result.Output, "\n")) {
			return
		}
		fmt.Fprintln(c.stdout, result.Output)
	}
}

func (c *client) commandGoal(ctx context.Context, args string) {
	body, _ := json.Marshal(args)
	result, err := c.runLua(ctx, runLuaParams{Code: "cli.say(cli.goal.command(" + string(body) + "))"})
	if err != nil {
		if c.emitError("goal failed: " + err.Error()) {
			return
		}
		fmt.Fprintln(c.stderr, "goal failed:", err)
		return
	}
	if strings.TrimSpace(result.Output) != "" {
		if c.emitInfo("goal", strings.TrimRight(result.Output, "\n")) {
			return
		}
		fmt.Fprintln(c.stdout, result.Output)
	}
}

func (c *client) commandServerPrompt(ctx context.Context, prompt string) {
	c.mu.Lock()
	sessionID := c.state.SessionID
	c.mu.Unlock()
	if err := c.Prompt(ctx, sessionID, prompt); err != nil {
		if c.emitError("command failed: " + err.Error()) {
			return
		}
		fmt.Fprintln(c.stderr, "command failed:", err)
	}
}

func (c *client) commandNew(ctx context.Context) {
	c.mu.Lock()
	cwd := c.state.Cwd
	c.mu.Unlock()
	session, err := c.NewSession(ctx, cwd)
	if err != nil {
		if c.emitError("session/new failed: " + err.Error()) {
			return
		}
		fmt.Fprintln(c.stderr, "session/new failed:", err)
		return
	}
	c.mu.Lock()
	addr := c.state.Addr
	c.mu.Unlock()
	c.setSessionState(addr, cwd, session)
	if c.emitInfo("session", fmt.Sprintf("new session %s cwd=%s", session.SessionID, cwd)) {
		return
	}
	fmt.Fprintf(c.stderr, "new session %s cwd=%s\n", session.SessionID, cwd)
}

func (c *client) commandStop(ctx context.Context) {
	c.mu.Lock()
	sessionID := c.state.SessionID
	c.mu.Unlock()
	if sessionID == "" {
		if c.emitInfo("stop", "no active session") {
			return
		}
		fmt.Fprintln(c.stderr, "no active session")
		return
	}
	if err := c.Cancel(ctx, sessionID); err != nil {
		if c.emitError("session/cancel failed: " + err.Error()) {
			return
		}
		fmt.Fprintln(c.stderr, "session/cancel failed:", err)
		return
	}
	if c.emitInfo("stop", "stop requested") {
		return
	}
	fmt.Fprintln(c.stderr, "stop requested")
}

func (c *client) printQueue() {
	c.mu.Lock()
	queue := append([]string(nil), c.promptQueue...)
	c.mu.Unlock()
	if len(queue) == 0 {
		if c.emitInfo("queue", "empty") {
			return
		}
		fmt.Fprintln(c.stderr, "queue: empty")
		return
	}
	var b strings.Builder
	for i, item := range queue {
		fmt.Fprintf(&b, "%d. %s\n", i+1, item)
	}
	if c.emitInfo("queue", strings.TrimRight(b.String(), "\n")) {
		return
	}
	fmt.Fprint(c.stderr, b.String())
}

func (c *client) printStatus() {
	body := c.statusString()
	if c.emitInfo("status", strings.TrimRight(body, "\n")) {
		return
	}
	fmt.Fprint(c.stderr, body)
}

func (c *client) statusString() string {
	state, opts := c.snapshotState()
	contextFile := systemprompt.ProjectContextPath(state.Cwd)
	if contextFile == "" {
		contextFile = "(none)"
	}
	return fmt.Sprintf("addr=%s\nsession=%s\ncwd=%s\ncontext=%s\ncontext_usage=%s\n5h_limit=%s\nweekly_limit=%s\nmonthly_limit=%s\nmessages=%d\ncheckpoints=%d\ncache_epoch=%d\nmodel=%s\nmode=%s\nworker=%s\nworker_kind=%s\nworker_caps=%s\nworker_permission=%s\nworker_cancellable=%v\nbusy=%v\nqueue=%d\nactive_tools=%d\nactive_subagents=%d\nlast_tool=%s\npermission=%s\nthinking=%v\ntools=%v\nraw=%v\n",
		state.Addr, state.SessionID, state.Cwd, contextFile, contextLabel(state.Context),
		percentOrUnknown(state.Limits.FiveHourPercent), percentOrUnknown(state.Limits.WeeklyPercent), percentOrUnknown(state.Limits.MonthlyPercent),
		state.Context.Messages, state.Context.Checkpoints, state.Context.CacheEpoch, state.Model, state.Mode,
		state.Worker.ID, state.Worker.Kind, strings.Join(state.Worker.Capabilities, ","), state.Worker.Permission, state.Worker.Cancellable,
		state.Busy, state.QueueLen, state.Tools, state.Subagents, state.LastTool,
		firstNonEmpty(opts.Permission, "prompt"), opts.ShowThinking, opts.ShowTools, opts.RawUpdates)
}

func percentOrUnknown(v float64) string {
	if v <= 0 {
		return "?"
	}
	return fmt.Sprintf("%.0f%%", v)
}

func (c *client) commandSessions(ctx context.Context) {
	list, err := c.ListSessions(ctx)
	if err != nil {
		if c.emitError("session/list failed: " + err.Error()) {
			return
		}
		fmt.Fprintln(c.stderr, "session/list failed:", err)
		return
	}
	if len(list.Sessions) == 0 {
		if c.emitInfo("sessions", "no sessions") {
			return
		}
		fmt.Fprintln(c.stderr, "no sessions")
		return
	}
	var b strings.Builder
	for _, s := range list.Sessions {
		title := ""
		if s.Title != nil {
			title = *s.Title
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", s.SessionID, s.UpdatedAt, s.Cwd, title)
		if c.events == nil {
			fmt.Fprintf(c.stderr, "%s\t%s\t%s\t%s\n", s.SessionID, s.UpdatedAt, s.Cwd, title)
		}
	}
	c.emitInfo("sessions", strings.TrimRight(b.String(), "\n"))
}

func (c *client) commandResume(ctx context.Context, sessionID string, replay bool) {
	if sessionID == "" {
		if replay {
			if c.emitInfo("session", "usage: /session-load SESSION_ID") {
				return
			}
			fmt.Fprintln(c.stderr, "usage: /session-load SESSION_ID")
		} else {
			if c.emitInfo("session", "usage: /resume SESSION_ID") {
				return
			}
			fmt.Fprintln(c.stderr, "usage: /resume SESSION_ID")
		}
		return
	}
	if err := c.ResumeSession(ctx, sessionID, replay); err != nil {
		if replay {
			if c.emitError("session/load failed: " + err.Error()) {
				return
			}
			fmt.Fprintln(c.stderr, "session/load failed:", err)
		} else {
			if c.emitError("session/resume failed: " + err.Error()) {
				return
			}
			fmt.Fprintln(c.stderr, "session/resume failed:", err)
		}
		return
	}
	if replay {
		if c.emitInfo("session", "loaded "+sessionID) {
			return
		}
		fmt.Fprintln(c.stderr, "loaded", sessionID)
	} else {
		if c.emitInfo("session", "resumed "+sessionID) {
			return
		}
		fmt.Fprintln(c.stderr, "resumed", sessionID)
	}
}

func (c *client) commandModel(ctx context.Context, model string) {
	if model == "" {
		c.mu.Lock()
		cur := c.state.Model
		c.mu.Unlock()
		if c.emitInfo("model", cur) {
			return
		}
		fmt.Fprintln(c.stderr, "model", cur)
		return
	}
	c.mu.Lock()
	sessionID := c.state.SessionID
	c.mu.Unlock()
	if err := c.SetModel(ctx, sessionID, model); err != nil {
		if c.emitError("set model failed: " + err.Error()) {
			return
		}
		fmt.Fprintln(c.stderr, "set model failed:", err)
		return
	}
	if c.emitInfo("model", model) {
		return
	}
	fmt.Fprintln(c.stderr, "model", model)
}

func (c *client) commandMode(ctx context.Context, mode string) {
	if mode == "" {
		c.mu.Lock()
		cur := c.state.Mode
		c.mu.Unlock()
		if c.emitInfo("mode", cur) {
			return
		}
		fmt.Fprintln(c.stderr, "mode", cur)
		return
	}
	c.mu.Lock()
	sessionID := c.state.SessionID
	c.mu.Unlock()
	if err := c.SetMode(ctx, sessionID, mode); err != nil {
		if c.emitError("set mode failed: " + err.Error()) {
			return
		}
		fmt.Fprintln(c.stderr, "set mode failed:", err)
		return
	}
	if c.emitInfo("mode", mode) {
		return
	}
	fmt.Fprintln(c.stderr, "mode", mode)
}

func (c *client) commandPermission(mode string) {
	if mode == "" {
		c.mu.Lock()
		permission := c.opts.Permission
		c.mu.Unlock()
		if c.emitInfo("permission", firstNonEmpty(permission, "prompt")) {
			return
		}
		fmt.Fprintln(c.stderr, "permission", firstNonEmpty(permission, "prompt"))
		return
	}
	switch strings.ToLower(mode) {
	case "prompt", "allow", "reject", "deny", "cancel", "cancelled":
		if strings.ToLower(mode) == "deny" {
			mode = "reject"
		}
		c.mu.Lock()
		c.opts.Permission = strings.ToLower(mode)
		permission := c.opts.Permission
		c.mu.Unlock()
		c.emitState()
		if c.emitInfo("permission", permission) {
			return
		}
		fmt.Fprintln(c.stderr, "permission", permission)
	default:
		if c.emitInfo("permission", "permission must be prompt, allow, reject, or cancel") {
			return
		}
		fmt.Fprintln(c.stderr, "permission must be prompt, allow, reject, or cancel")
	}
}

func (c *client) commandBool(name, value string, target *bool) {
	c.mu.Lock()
	if value == "" {
		out := fmt.Sprintf("%s %v", name, *target)
		c.mu.Unlock()
		if c.emitInfo(name, out) {
			return
		}
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
		c.mu.Unlock()
		if c.emitInfo(name, fmt.Sprintf("%s must be on, off, or toggle", name)) {
			return
		}
		fmt.Fprintf(c.stderr, "%s must be on, off, or toggle\n", name)
		return
	}
	out := fmt.Sprintf("%s %v", name, *target)
	c.mu.Unlock()
	c.emitState()
	if c.emitInfo(name, out) {
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
