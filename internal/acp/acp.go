// Package acp defines the wire-level types and the JSON-RPC 2.0 (ndjson)
// stdio transport used by the Agent Client Protocol.
//
// Types intentionally cover the subset of ACP that glm-acp-agent uses; we
// pass-through unrecognised fields so we stay forward-compatible with newer
// clients.
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// ProtocolVersion is the highest ACP protocol version we know how to speak.
// We negotiate down to the client's version when needed.
const ProtocolVersion = 1

// AgentInfo describes this agent build.
type AgentInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// AuthMethod describes one way a client can authenticate to the agent.
type AuthMethod struct {
	Type        string             `json:"type,omitempty"`
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Link        string             `json:"link,omitempty"`
	Vars        []AuthMethodVar    `json:"vars,omitempty"`
}

// AuthMethodVar describes a variable the client must collect.
type AuthMethodVar struct {
	Name     string `json:"name"`
	Label    string `json:"label,omitempty"`
	Secret   bool   `json:"secret,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

// PromptCapabilities advertises what kinds of prompt blocks we accept.
type PromptCapabilities struct {
	EmbeddedContext bool `json:"embeddedContext"`
	Image           bool `json:"image"`
}

// SessionCapabilities are the optional session lifecycle features we support.
type SessionCapabilities struct {
	Close  *struct{} `json:"close,omitempty"`
	List   *struct{} `json:"list,omitempty"`
	Fork   *struct{} `json:"fork,omitempty"`
	Resume *struct{} `json:"resume,omitempty"`
}

// MCPCapabilities advertises which MCP transports the agent can connect to.
type MCPCapabilities struct {
	HTTP bool `json:"http,omitempty"`
}

// AgentCapabilities is the full capability advertisement.
type AgentCapabilities struct {
	LoadSession         bool                `json:"loadSession"`
	MCPCapabilities     MCPCapabilities     `json:"mcpCapabilities,omitempty"`
	PromptCapabilities  PromptCapabilities  `json:"promptCapabilities,omitempty"`
	SessionCapabilities SessionCapabilities `json:"sessionCapabilities,omitempty"`
}

// InitializeParams is the argument to the `initialize` method.
type InitializeParams struct {
	ProtocolVersion    int            `json:"protocolVersion"`
	ClientCapabilities map[string]any `json:"clientCapabilities,omitempty"`
}

// InitializeResponse is returned from the agent on `initialize`.
type InitializeResponse struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentInfo         AgentInfo         `json:"agentInfo"`
	AuthMethods       []AuthMethod      `json:"authMethods,omitempty"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
}

// MCPServerSpec describes a session-scoped MCP server the client wants the
// agent to connect to. We accept it as raw JSON since glm-acp-agent doesn't
// (yet) support session MCP integration in this Go port.
type MCPServerSpec = json.RawMessage

// NewSessionParams is the argument to `session/new`.
type NewSessionParams struct {
	Cwd        string          `json:"cwd"`
	MCPServers []MCPServerSpec `json:"mcpServers,omitempty"`
}

// SessionModelState is the model picker state advertised on session/new etc.
type SessionModelState struct {
	AvailableModels []ModelInfo `json:"availableModels"`
	CurrentModelID  string      `json:"currentModelId"`
}

// ModelInfo is a single advertised model.
type ModelInfo struct {
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionModeInfo describes one mode the agent supports.
type SessionModeInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionModeState is the mode picker advertised on session/new etc.
type SessionModeState struct {
	AvailableModes []SessionModeInfo `json:"availableModes"`
	CurrentModeID  string            `json:"currentModeId"`
}

// NewSessionResponse is returned from `session/new`.
type NewSessionResponse struct {
	SessionID string             `json:"sessionId"`
	Models    *SessionModelState `json:"models,omitempty"`
	Modes     *SessionModeState  `json:"modes,omitempty"`
}

// LoadSessionParams is the argument to `session/load`.
type LoadSessionParams struct {
	SessionID  string          `json:"sessionId"`
	Cwd        string          `json:"cwd"`
	MCPServers []MCPServerSpec `json:"mcpServers,omitempty"`
}

// LoadSessionResponse is returned from `session/load` / `resume` / `fork`.
type LoadSessionResponse struct {
	Models *SessionModelState `json:"models,omitempty"`
	Modes  *SessionModeState  `json:"modes,omitempty"`
}

// ForkSessionResponse is returned from `session/fork`.
type ForkSessionResponse struct {
	SessionID string             `json:"sessionId"`
	Models    *SessionModelState `json:"models,omitempty"`
	Modes     *SessionModeState  `json:"modes,omitempty"`
}

// PromptParams is the argument to `session/prompt`.
type PromptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
	MessageID string         `json:"messageId,omitempty"`
}

// ContentBlock is a flexible representation of an ACP content block.
type ContentBlock struct {
	Type     string            `json:"type"`
	Text     string            `json:"text,omitempty"`
	URI      string            `json:"uri,omitempty"`
	Name     string            `json:"name,omitempty"`
	MimeType string            `json:"mimeType,omitempty"`
	Data     string            `json:"data,omitempty"`
	Resource *EmbeddedResource `json:"resource,omitempty"`
}

// EmbeddedResource is an inline resource attached to a `resource` block.
type EmbeddedResource struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// PromptResponse is returned from `session/prompt`.
type PromptResponse struct {
	StopReason    string `json:"stopReason"`
	Usage         any    `json:"usage,omitempty"`
	UserMessageID string `json:"userMessageId,omitempty"`
}

// CancelParams is the notification body for `session/cancel`.
type CancelParams struct {
	SessionID string `json:"sessionId"`
}

// CloseSessionParams is the argument to `session/close`.
type CloseSessionParams struct {
	SessionID string `json:"sessionId"`
}

// SetModeParams is the argument to `session/set_mode` (advisory; we no-op).
type SetModeParams struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId,omitempty"`
}

// SetModelParams is the argument to `session/set_model`.
type SetModelParams struct {
	SessionID string `json:"sessionId"`
	ModelID   string `json:"modelId"`
}

// ListSessionsParams is the argument to `session/list`.
type ListSessionsParams struct {
	Cwd string `json:"cwd,omitempty"`
}

// SessionListItem is one entry in the listSessions response.
type SessionListItem struct {
	SessionID string  `json:"sessionId"`
	Cwd       string  `json:"cwd"`
	Title     *string `json:"title,omitempty"`
	UpdatedAt string  `json:"updatedAt"`
}

// ListSessionsResponse is returned from `session/list`.
type ListSessionsResponse struct {
	Sessions []SessionListItem `json:"sessions"`
}

// SessionUpdateParams is the notification body for `session/update`.
type SessionUpdateParams struct {
	SessionID string         `json:"sessionId"`
	Update    map[string]any `json:"update"`
}

// PermissionOption is one selectable option presented to the user.
type PermissionOption struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	OptionID string `json:"optionId"`
}

// RequestPermissionParams is the argument sent to the client.
type RequestPermissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  map[string]any     `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

// PermissionOutcome describes the user's choice.
type PermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

// RequestPermissionResponse is what the client returns.
type RequestPermissionResponse struct {
	Outcome PermissionOutcome `json:"outcome"`
}

// Agent is the interface implemented by the ACP agent. The connection
// dispatches incoming requests and notifications to these methods.
type Agent interface {
	Initialize(context.Context, InitializeParams) (InitializeResponse, error)
	Authenticate(context.Context, json.RawMessage) (any, error)
	NewSession(context.Context, NewSessionParams) (NewSessionResponse, error)
	LoadSession(context.Context, LoadSessionParams) (LoadSessionResponse, error)
	ForkSession(context.Context, LoadSessionParams) (ForkSessionResponse, error)
	ResumeSession(context.Context, LoadSessionParams) (LoadSessionResponse, error)
	Prompt(context.Context, PromptParams) (PromptResponse, error)
	Cancel(context.Context, CancelParams)
	CloseSession(context.Context, CloseSessionParams) (any, error)
	ListSessions(context.Context, ListSessionsParams) (ListSessionsResponse, error)
	SetSessionMode(context.Context, SetModeParams) (any, error)
	SetSessionModel(context.Context, SetModelParams) (any, error)
}

// ----------------------------------------------------------------------------
// JSON-RPC 2.0 transport over newline-delimited JSON.
// ----------------------------------------------------------------------------

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc %d: %s", e.Code, e.Message) }

// pendingResponse waits for a JSON-RPC response on a previously-issued
// outbound request.
type pendingResponse struct {
	result json.RawMessage
	err    *RPCError
	done   chan struct{}
}

// Conn is a JSON-RPC 2.0 connection over stdio (or any io.ReadWriter).
type Conn struct {
	br      *bufio.Reader
	w       io.Writer
	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[int64]*pendingResponse
	nextID    atomic.Int64

	agent  Agent
	ctx    context.Context
	cancel context.CancelFunc

	handlers sync.WaitGroup

	closedCh  chan struct{}
	closeOnce sync.Once
	closeErr  error
}

// NewConn wraps the provided streams in a JSON-RPC connection bound to the
// given agent.
func NewConn(in io.Reader, out io.Writer, agent Agent) *Conn {
	ctx, cancel := context.WithCancel(context.Background())
	return &Conn{
		br:       bufio.NewReader(in),
		w:        out,
		pending:  map[int64]*pendingResponse{},
		agent:    agent,
		ctx:      ctx,
		cancel:   cancel,
		closedCh: make(chan struct{}),
	}
}

// Run reads messages until the input stream closes. Returns the terminal
// error (nil for clean EOF). Blocks until all in-flight inbound handlers
// have completed.
func (c *Conn) Run() error {
	var terminal error
	dec := json.NewDecoder(c.br)
	for {
		var msg rpcMessage
		if err := dec.Decode(&msg); err != nil {
			if !errors.Is(err, io.EOF) {
				terminal = err
			}
			break
		}
		c.dispatch(msg)
	}
	c.handlers.Wait()
	c.close(terminal)
	return terminal
}

// Done is closed when the connection terminates.
func (c *Conn) Done() <-chan struct{} { return c.closedCh }

// Context returns a context that is cancelled when the connection closes.
func (c *Conn) Context() context.Context { return c.ctx }

// Err returns the terminal error, if any.
func (c *Conn) Err() error { return c.closeErr }

func (c *Conn) close(err error) {
	c.closeOnce.Do(func() {
		c.closeErr = err
		c.cancel()
		close(c.closedCh)
	})
}

// SendNotification fires a JSON-RPC notification (no id, no response).
func (c *Conn) SendNotification(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	msg := rpcMessage{JSONRPC: "2.0", Method: method, Params: raw}
	return c.write(msg)
}

// Call sends a JSON-RPC request and blocks until the response arrives.
func (c *Conn) Call(ctx context.Context, method string, params any, result any) error {
	id := c.nextID.Add(1)
	idRaw, _ := json.Marshal(id)
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	pending := &pendingResponse{done: make(chan struct{})}
	c.pendingMu.Lock()
	c.pending[id] = pending
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()
	msg := rpcMessage{JSONRPC: "2.0", ID: idRaw, Method: method, Params: raw}
	if err := c.write(msg); err != nil {
		return err
	}
	select {
	case <-pending.done:
		if pending.err != nil {
			return pending.err
		}
		if result != nil && len(pending.result) > 0 {
			return json.Unmarshal(pending.result, result)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closedCh:
		return errors.New("connection closed")
	}
}

func (c *Conn) write(msg rpcMessage) error {
	msg.JSONRPC = "2.0"
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.w.Write(body)
	return err
}

func (c *Conn) dispatch(msg rpcMessage) {
	// Response (matches a previously-issued outbound request)?
	if msg.Method == "" && len(msg.ID) > 0 {
		var id int64
		if err := json.Unmarshal(msg.ID, &id); err == nil {
			c.pendingMu.Lock()
			p, ok := c.pending[id]
			c.pendingMu.Unlock()
			if ok {
				p.result = msg.Result
				p.err = msg.Error
				close(p.done)
				return
			}
		}
		return
	}
	// Inbound request or notification.
	c.handlers.Add(1)
	go func() {
		defer c.handlers.Done()
		c.handleInbound(msg)
	}()
}

func (c *Conn) handleInbound(msg rpcMessage) {
	isRequest := len(msg.ID) > 0
	respond := func(result any, err error) {
		if !isRequest {
			return
		}
		out := rpcMessage{JSONRPC: "2.0", ID: msg.ID}
		if err != nil {
			out.Error = toRPCError(err)
		} else {
			raw, mErr := json.Marshal(result)
			if mErr != nil {
				out.Error = &RPCError{Code: -32603, Message: mErr.Error()}
			} else {
				out.Result = raw
			}
		}
		_ = c.write(out)
	}

	switch msg.Method {
	case "initialize":
		var p InitializeParams
		_ = json.Unmarshal(msg.Params, &p)
		res, err := c.agent.Initialize(c.ctx, p)
		respond(res, err)
	case "authenticate":
		res, err := c.agent.Authenticate(c.ctx, msg.Params)
		respond(res, err)
	case "session/new":
		var p NewSessionParams
		_ = json.Unmarshal(msg.Params, &p)
		res, err := c.agent.NewSession(c.ctx, p)
		respond(res, err)
	case "session/load":
		var p LoadSessionParams
		_ = json.Unmarshal(msg.Params, &p)
		res, err := c.agent.LoadSession(c.ctx, p)
		respond(res, err)
	case "session/fork":
		var p LoadSessionParams
		_ = json.Unmarshal(msg.Params, &p)
		res, err := c.agent.ForkSession(c.ctx, p)
		respond(res, err)
	case "session/resume":
		var p LoadSessionParams
		_ = json.Unmarshal(msg.Params, &p)
		res, err := c.agent.ResumeSession(c.ctx, p)
		respond(res, err)
	case "session/prompt":
		var p PromptParams
		_ = json.Unmarshal(msg.Params, &p)
		res, err := c.agent.Prompt(c.ctx, p)
		respond(res, err)
	case "session/cancel":
		var p CancelParams
		_ = json.Unmarshal(msg.Params, &p)
		c.agent.Cancel(c.ctx, p)
	case "session/close":
		var p CloseSessionParams
		_ = json.Unmarshal(msg.Params, &p)
		res, err := c.agent.CloseSession(c.ctx, p)
		respond(res, err)
	case "session/list":
		var p ListSessionsParams
		_ = json.Unmarshal(msg.Params, &p)
		res, err := c.agent.ListSessions(c.ctx, p)
		respond(res, err)
	case "session/set_mode":
		var p SetModeParams
		_ = json.Unmarshal(msg.Params, &p)
		res, err := c.agent.SetSessionMode(c.ctx, p)
		respond(res, err)
	case "session/set_model":
		var p SetModelParams
		_ = json.Unmarshal(msg.Params, &p)
		res, err := c.agent.SetSessionModel(c.ctx, p)
		respond(res, err)
	default:
		respond(nil, &RPCError{Code: -32601, Message: "method not found: " + msg.Method})
	}
}

func toRPCError(err error) *RPCError {
	if err == nil {
		return nil
	}
	var re *RPCError
	if errors.As(err, &re) {
		return re
	}
	return &RPCError{Code: -32000, Message: err.Error()}
}
