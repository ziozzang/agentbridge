// Package agent implements the GLM-backed ACP Agent: it negotiates the
// initialize handshake, owns per-session state and history, drives the GLM
// chat-completions stream, and dispatches tool calls back to the client.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ziozzang/glm-acp/internal/acp"
	"github.com/ziozzang/glm-acp/internal/credentials"
	"github.com/ziozzang/glm-acp/internal/glm"
	"github.com/ziozzang/glm-acp/internal/logger"
	"github.com/ziozzang/glm-acp/internal/protocol/imagepre"
	"github.com/ziozzang/glm-acp/internal/protocol/sessionstore"
	"github.com/ziozzang/glm-acp/internal/protocol/systemprompt"
	"github.com/ziozzang/glm-acp/internal/tools/definitions"
	"github.com/ziozzang/glm-acp/internal/tools/executor"
	"github.com/ziozzang/glm-acp/internal/tools/zaimcp"
)

// Version is reported in the initialize response.
const Version = "1.0.0"

// AgentName is reported in the initialize response.
const AgentName = "glm-acp-agent"

// MaxToolIterations caps how many tool/response cycles a single prompt may
// trigger to prevent runaway loops.
const MaxToolIterations = 32

// Agent is the GLM-backed ACP Agent.
type Agent struct {
	Conn  *acp.Conn
	Store *sessionstore.Store
	GLM   *glm.Client
	MCP   executor.MCPCaller

	mu        sync.Mutex
	sessions  map[string]*sessionState
	authed    bool
}

// sessionState is the in-memory state for a session.
type sessionState struct {
	ID            string
	Cwd           string
	Model         string
	Messages      []glm.Message
	Title         *string
	UpdatedAt     string
	cancelMu      sync.Mutex
	cancelCurrent context.CancelFunc
}

// New constructs an Agent. The GLM client is built lazily so `--setup` and
// other commands don't require an API key.
func New(store *sessionstore.Store) *Agent {
	if store == nil {
		store = sessionstore.New()
	}
	return &Agent{
		Store:    store,
		sessions: map[string]*sessionState{},
	}
}

// SetConn wires the JSON-RPC connection.
func (a *Agent) SetConn(c *acp.Conn) { a.Conn = c }

// Initialize handles the ACP `initialize` method.
func (a *Agent) Initialize(_ context.Context, p acp.InitializeParams) (acp.InitializeResponse, error) {
	resp := acp.InitializeResponse{
		ProtocolVersion: minInt(p.ProtocolVersion, acp.ProtocolVersion),
		AgentInfo:       acp.AgentInfo{Name: AgentName, Version: Version},
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession: true,
			MCPCapabilities: acp.MCPCapabilities{HTTP: true},
			PromptCapabilities: acp.PromptCapabilities{
				EmbeddedContext: true,
				Image:           true,
			},
			SessionCapabilities: acp.SessionCapabilities{
				Close: &struct{}{}, List: &struct{}{}, Fork: &struct{}{}, Resume: &struct{}{},
			},
		},
		AuthMethods: []acp.AuthMethod{
			{
				ID: "api_key", Name: "Z.AI API Key",
				Description: "Provide a Z.AI API key (Z_AI_API_KEY env var or via --setup).",
				Vars: []acp.AuthMethodVar{{Name: "api_key", Label: "Z.AI API Key", Secret: true}},
			},
		},
	}
	if resp.ProtocolVersion < 1 {
		resp.ProtocolVersion = acp.ProtocolVersion
	}
	logger.Debugf("initialize: clientProtocol=%d -> agentProtocol=%d", p.ProtocolVersion, resp.ProtocolVersion)
	return resp, nil
}

// Authenticate accepts an API key submission from the client.
func (a *Agent) Authenticate(_ context.Context, raw json.RawMessage) (any, error) {
	var body struct {
		MethodID string         `json:"methodId"`
		Vars     map[string]any `json:"vars"`
	}
	_ = json.Unmarshal(raw, &body)
	if key, ok := body.Vars["api_key"].(string); ok && strings.TrimSpace(key) != "" {
		if err := credentials.Write(strings.TrimSpace(key), ""); err != nil {
			return nil, err
		}
	}
	a.mu.Lock()
	a.authed = true
	a.mu.Unlock()
	return map[string]any{}, nil
}

// NewSession creates a new session.
func (a *Agent) NewSession(_ context.Context, p acp.NewSessionParams) (acp.NewSessionResponse, error) {
	if err := a.ensureClient(); err != nil {
		return acp.NewSessionResponse{}, err
	}
	id := newSessionID()
	model := glm.DefaultModelEnv()
	a.mu.Lock()
	a.sessions[id] = &sessionState{
		ID: id, Cwd: p.Cwd, Model: model, Messages: nil, UpdatedAt: nowRFC3339(),
	}
	a.mu.Unlock()
	logger.Debugf("session/new id=%s cwd=%s model=%s", id, p.Cwd, model)
	return acp.NewSessionResponse{
		SessionID: id,
		Models:    a.modelState(model),
	}, nil
}

// LoadSession rehydrates a previously saved session.
func (a *Agent) LoadSession(_ context.Context, p acp.LoadSessionParams) (acp.LoadSessionResponse, error) {
	if err := a.ensureClient(); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	persisted, _ := a.Store.Load(p.SessionID)
	if persisted == nil {
		return acp.LoadSessionResponse{}, &acp.RPCError{Code: -32001, Message: "session not found"}
	}
	model := persisted.Model
	if model == "" {
		model = glm.DefaultModelEnv()
	}
	a.mu.Lock()
	a.sessions[p.SessionID] = &sessionState{
		ID: p.SessionID, Cwd: persisted.Cwd, Model: model,
		Messages: persisted.Messages, Title: persisted.Title, UpdatedAt: persisted.UpdatedAt,
	}
	a.mu.Unlock()
	return acp.LoadSessionResponse{Models: a.modelState(model)}, nil
}

// ResumeSession is identical to LoadSession in this Go port.
func (a *Agent) ResumeSession(ctx context.Context, p acp.LoadSessionParams) (acp.LoadSessionResponse, error) {
	return a.LoadSession(ctx, p)
}

// ForkSession creates a new session from the messages of an existing one.
func (a *Agent) ForkSession(_ context.Context, p acp.LoadSessionParams) (acp.ForkSessionResponse, error) {
	if err := a.ensureClient(); err != nil {
		return acp.ForkSessionResponse{}, err
	}
	persisted, _ := a.Store.Load(p.SessionID)
	if persisted == nil {
		return acp.ForkSessionResponse{}, &acp.RPCError{Code: -32001, Message: "session not found"}
	}
	model := persisted.Model
	if model == "" {
		model = glm.DefaultModelEnv()
	}
	id := newSessionID()
	a.mu.Lock()
	a.sessions[id] = &sessionState{
		ID: id, Cwd: p.Cwd, Model: model,
		Messages: append([]glm.Message(nil), persisted.Messages...),
		UpdatedAt: nowRFC3339(),
	}
	a.mu.Unlock()
	return acp.ForkSessionResponse{SessionID: id, Models: a.modelState(model)}, nil
}

// CloseSession discards in-memory session state.
func (a *Agent) CloseSession(_ context.Context, p acp.CloseSessionParams) (any, error) {
	a.mu.Lock()
	delete(a.sessions, p.SessionID)
	a.mu.Unlock()
	return map[string]any{}, nil
}

// ListSessions enumerates persisted sessions.
func (a *Agent) ListSessions(_ context.Context, p acp.ListSessionsParams) (acp.ListSessionsResponse, error) {
	meta := a.Store.ListMetadata()
	sessions := make([]acp.SessionListItem, 0, len(meta))
	for _, m := range meta {
		if p.Cwd != "" && m.Cwd != p.Cwd {
			continue
		}
		sessions = append(sessions, acp.SessionListItem{
			SessionID: m.SessionID, Cwd: m.Cwd, Title: m.Title, UpdatedAt: m.UpdatedAt,
		})
	}
	return acp.ListSessionsResponse{Sessions: sessions}, nil
}

// SetSessionMode is a no-op (mode advisory).
func (a *Agent) SetSessionMode(_ context.Context, _ acp.SetModeParams) (any, error) {
	return map[string]any{}, nil
}

// SetSessionModel switches the model for a session.
func (a *Agent) SetSessionModel(_ context.Context, p acp.SetModelParams) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[p.SessionID]
	if !ok {
		return nil, &acp.RPCError{Code: -32001, Message: "session not found"}
	}
	s.Model = p.ModelID
	return map[string]any{}, nil
}

// Cancel signals the in-flight prompt for the given session to stop.
func (a *Agent) Cancel(_ context.Context, p acp.CancelParams) {
	a.mu.Lock()
	s := a.sessions[p.SessionID]
	a.mu.Unlock()
	if s == nil {
		return
	}
	s.cancelMu.Lock()
	if s.cancelCurrent != nil {
		s.cancelCurrent()
	}
	s.cancelMu.Unlock()
}

// Prompt drives the GLM chat-completions loop until the model finishes.
func (a *Agent) Prompt(ctx context.Context, p acp.PromptParams) (acp.PromptResponse, error) {
	if err := a.ensureClient(); err != nil {
		return acp.PromptResponse{}, err
	}
	a.mu.Lock()
	s, ok := a.sessions[p.SessionID]
	a.mu.Unlock()
	if !ok {
		return acp.PromptResponse{}, &acp.RPCError{Code: -32001, Message: "session not found"}
	}

	// Per-prompt cancellable context registered for session/cancel.
	promptCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.cancelMu.Lock()
	s.cancelCurrent = cancel
	s.cancelMu.Unlock()

	pre := imagepre.Preprocess(promptCtx, p.Prompt, nil)
	defer func() {
		for _, fn := range pre.Cleanups {
			fn()
		}
	}()
	userText := imagepre.RenderToString(pre.Blocks)
	logger.Debugf("session/prompt sessionId=%s blocks=%d userTextLen=%d", p.SessionID, len(p.Prompt), len(userText))
	s.Messages = append(s.Messages, glm.Message{Role: "user", Content: userText})

	exec := &executor.Executor{
		Conn: a.Conn, SessionID: p.SessionID, SessionCwd: s.Cwd,
		MCP: a.MCP,
	}

	// Prepare the system prompt once per turn.
	tools := definitions.All()
	toolNames := make([]string, len(tools))
	for i, t := range tools {
		toolNames[i] = t.Function.Name
	}
	system := systemprompt.Build(systemprompt.Input{
		Cwd: s.Cwd, Tools: toolNames,
		AgentsMD: systemprompt.LoadProjectContext(s.Cwd),
	})
	messages := append([]glm.Message{{Role: "system", Content: system}}, s.Messages...)

	stop := "end_turn"
	var lastUsage *glm.Usage
	for iter := 0; iter < MaxToolIterations; iter++ {
		if promptCtx.Err() != nil {
			stop = "cancelled"
			break
		}
		chunks, errs := a.GLM.StreamChat(promptCtx, messages, glm.StreamOptions{Model: s.Model, Tools: tools})

		var assistantText, assistantThought string
		var toolCalls []glm.ToolCall
		var streamStop string
		for c := range chunks {
			if c.Text != "" {
				assistantText += c.Text
				_ = a.Conn.SendNotification("session/update", acp.SessionUpdateParams{
					SessionID: p.SessionID,
					Update: map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": c.Text},
					},
				})
			}
			if c.Thinking != "" {
				assistantThought += c.Thinking
				_ = a.Conn.SendNotification("session/update", acp.SessionUpdateParams{
					SessionID: p.SessionID,
					Update: map[string]any{
						"sessionUpdate": "agent_thought_chunk",
						"content":       map[string]any{"type": "text", "text": c.Thinking},
					},
				})
			}
			if c.ToolCall != nil {
				toolCalls = append(toolCalls, *c.ToolCall)
			}
			if c.Usage != nil {
				lastUsage = c.Usage
			}
			if c.Done && c.StopReason != "" {
				streamStop = c.StopReason
			}
		}
		if err := <-errs; err != nil {
			if errors.Is(err, context.Canceled) {
				stop = "cancelled"
				break
			}
			return acp.PromptResponse{}, fmt.Errorf("GLM stream failed: %w", err)
		}

		// Persist assistant turn.
		assistantMsg := glm.Message{Role: "assistant", Content: assistantText}
		if len(toolCalls) > 0 {
			tcs := make([]glm.ToolCallMsg, len(toolCalls))
			for i, t := range toolCalls {
				tcs[i] = glm.ToolCallMsg{
					ID: t.ID, Type: "function",
					Function: glm.ToolCallMsgFunction{Name: t.Name, Arguments: t.Arguments},
				}
			}
			assistantMsg.ToolCalls = tcs
		}
		s.Messages = append(s.Messages, assistantMsg)
		messages = append(messages, assistantMsg)

		if len(toolCalls) == 0 {
			if streamStop != "" {
				stop = mapStopReason(streamStop)
			}
			break
		}
		// Execute every tool call, append tool messages, and loop.
		for _, tc := range toolCalls {
			res := exec.Execute(promptCtx, tc.ID, tc.Name, tc.Arguments)
			toolMsg := glm.Message{Role: "tool", ToolCallID: tc.ID, Content: res.Content}
			s.Messages = append(s.Messages, toolMsg)
			messages = append(messages, toolMsg)
		}
	}

	s.UpdatedAt = nowRFC3339()
	_ = a.Store.Save(sessionstore.PersistedSession{
		SessionID: s.ID, Cwd: s.Cwd, Messages: s.Messages,
		Title: s.Title, UpdatedAt: s.UpdatedAt, Model: s.Model,
	})

	resp := acp.PromptResponse{StopReason: stop}
	if lastUsage != nil {
		resp.Usage = lastUsage
	}
	return resp, nil
}

// ensureClient lazily constructs the GLM client.
func (a *Agent) ensureClient() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.GLM != nil {
		return nil
	}
	c, err := glm.New()
	if err != nil {
		return err
	}
	a.GLM = c
	if a.MCP == nil {
		a.MCP = zaimcp.New()
	}
	return nil
}

func (a *Agent) modelState(current string) *acp.SessionModelState {
	models := glm.AvailableModels()
	out := make([]acp.ModelInfo, len(models))
	for i, m := range models {
		out[i] = acp.ModelInfo{ModelID: m.ModelID, Name: m.Name, Description: m.Description}
	}
	return &acp.SessionModelState{AvailableModels: out, CurrentModelID: current}
}

func mapStopReason(s string) string {
	switch s {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "end_turn"
	default:
		if s == "" {
			return "end_turn"
		}
		return s
	}
}

func newSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

func minInt(a, b int) int {
	if a == 0 {
		return b
	}
	if a < b {
		return a
	}
	return b
}
