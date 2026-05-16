// Package agent implements the GLM-backed ACP Agent. It owns the
// per-session prompt loop, history, model/mode state, and dispatches tool
// calls back to the connected ACP client.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

// DefaultMaxTurns is the default per-prompt iteration cap.
const DefaultMaxTurns = 20

// Session mode identifiers.
const (
	ModeDefault      = "default"
	ModeAcceptEdits  = "accept_edits"
	ModeBypassPerms  = "bypass_permissions"
)

// ValidModes is the set of session mode IDs we accept.
var ValidModes = []string{ModeDefault, ModeAcceptEdits, ModeBypassPerms}

// Notifier is the subset of acp.Conn the agent uses for outbound traffic.
// Tests substitute a stub that records notifications and permission calls.
type Notifier interface {
	SendNotification(method string, params any) error
	Call(ctx context.Context, method string, params any, result any) error
}

// Agent is the GLM-backed ACP Agent.
type Agent struct {
	Conn     Notifier
	Store    *sessionstore.Store
	GLM      *glm.Client
	MCP      executor.MCPCaller
	Vision   executor.Vision
	MaxTurns int

	// clientCapabilities captured at `initialize` time. Used to gate the
	// agent's advertised tool surface and downstream tool behaviour.
	clientCapabilities map[string]any

	mu       sync.Mutex
	sessions map[string]*sessionState
}

// sessionState is the in-memory state for a session.
type sessionState struct {
	ID        string
	Cwd       string
	Model     string
	Mode      string
	Messages  []glm.Message
	Title     *string
	UpdatedAt string

	// Per-session locks: promptMu serializes prompts; cancelMu protects
	// cancelCurrent; promptDone unblocks waiters when a prompt finishes.
	promptMu      sync.Mutex
	cancelMu      sync.Mutex
	cancelCurrent context.CancelFunc

	// tools advertised for this session (varies with MCP discovery / caps).
	tools []definitions.Tool
}

// New constructs an Agent. The GLM client is built lazily so `--setup` and
// other commands don't require an API key.
func New(store *sessionstore.Store) *Agent {
	if store == nil {
		store = sessionstore.New()
	}
	return &Agent{
		Store:    store,
		MaxTurns: DefaultMaxTurns,
		sessions: map[string]*sessionState{},
	}
}

// SetConn wires the JSON-RPC connection.
func (a *Agent) SetConn(c *acp.Conn) { a.Conn = c }

// Initialize handles the ACP `initialize` method.
func (a *Agent) Initialize(_ context.Context, p acp.InitializeParams) (acp.InitializeResponse, error) {
	a.mu.Lock()
	a.clientCapabilities = p.ClientCapabilities
	a.mu.Unlock()

	imageAllowed := !disabledByEnv("ACP_GLM_PROMPT_IMAGES")
	negotiated := p.ProtocolVersion
	if negotiated > acp.ProtocolVersion {
		negotiated = acp.ProtocolVersion
	}
	resp := acp.InitializeResponse{
		ProtocolVersion: negotiated,
		AgentInfo:       acp.AgentInfo{Name: AgentName, Version: Version},
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession:     true,
			MCPCapabilities: acp.MCPCapabilities{HTTP: true},
			PromptCapabilities: acp.PromptCapabilities{
				EmbeddedContext: true,
				Image:           imageAllowed,
			},
			SessionCapabilities: acp.SessionCapabilities{
				Close: &struct{}{}, List: &struct{}{}, Fork: &struct{}{}, Resume: &struct{}{},
			},
		},
		AuthMethods: []acp.AuthMethod{
			{
				ID: "api_key", Name: "Z.AI API Key",
				Description: "Provide a Z.AI API key (Z_AI_API_KEY env var or via --setup).",
				Vars:        []acp.AuthMethodVar{{Name: "api_key", Label: "Z.AI API Key", Secret: true}},
			},
		},
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
	return map[string]any{}, nil
}

// NewSession creates a new session.
func (a *Agent) NewSession(_ context.Context, p acp.NewSessionParams) (acp.NewSessionResponse, error) {
	if err := a.ensureClient(); err != nil {
		return acp.NewSessionResponse{}, err
	}
	id := newSessionID()
	model := glm.DefaultModelEnv()
	tools := a.availableTools()
	a.mu.Lock()
	a.sessions[id] = &sessionState{
		ID: id, Cwd: p.Cwd, Model: model, Mode: ModeDefault,
		Messages: nil, UpdatedAt: nowRFC3339(), tools: tools,
	}
	a.mu.Unlock()
	logger.Debugf("session/new id=%s cwd=%s model=%s tools=%d", id, p.Cwd, model, len(tools))
	return acp.NewSessionResponse{
		SessionID: id,
		Models:    a.modelState(model),
		Modes:     modesState(ModeDefault),
	}, nil
}

// LoadSession rehydrates a previously saved session and replays user/assistant
// text turns back to the client so its UI can rehydrate.
func (a *Agent) LoadSession(ctx context.Context, p acp.LoadSessionParams) (acp.LoadSessionResponse, error) {
	if err := a.ensureClient(); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	persisted, _ := a.Store.Load(p.SessionID)
	if persisted == nil {
		return acp.LoadSessionResponse{}, &acp.RPCError{Code: -32001, Message: "session not found: " + p.SessionID}
	}
	model := persisted.Model
	if model == "" {
		model = glm.DefaultModelEnv()
	}
	mode := persisted.Mode
	if mode == "" {
		mode = ModeDefault
	}
	a.mu.Lock()
	a.sessions[p.SessionID] = &sessionState{
		ID: p.SessionID, Cwd: p.Cwd, Model: model, Mode: mode,
		Messages: persisted.Messages, Title: persisted.Title, UpdatedAt: persisted.UpdatedAt,
		tools: a.availableTools(),
	}
	a.mu.Unlock()

	a.replayMessages(ctx, p.SessionID, persisted.Messages)

	return acp.LoadSessionResponse{
		Models: a.modelState(model),
		Modes:  modesState(mode),
	}, nil
}

// ResumeSession rehydrates without replaying messages — the client is
// expected to keep its UI state.
func (a *Agent) ResumeSession(_ context.Context, p acp.LoadSessionParams) (acp.LoadSessionResponse, error) {
	if err := a.ensureClient(); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	persisted, _ := a.Store.Load(p.SessionID)
	if persisted == nil {
		return acp.LoadSessionResponse{}, &acp.RPCError{Code: -32001, Message: "session not found: " + p.SessionID}
	}
	model := persisted.Model
	if model == "" {
		model = glm.DefaultModelEnv()
	}
	mode := persisted.Mode
	if mode == "" {
		mode = ModeDefault
	}
	a.mu.Lock()
	a.sessions[p.SessionID] = &sessionState{
		ID: p.SessionID, Cwd: p.Cwd, Model: model, Mode: mode,
		Messages: persisted.Messages, Title: persisted.Title, UpdatedAt: persisted.UpdatedAt,
		tools: a.availableTools(),
	}
	a.mu.Unlock()
	return acp.LoadSessionResponse{Models: a.modelState(model), Modes: modesState(mode)}, nil
}

// ForkSession creates a new session from the messages of an existing one.
func (a *Agent) ForkSession(_ context.Context, p acp.LoadSessionParams) (acp.ForkSessionResponse, error) {
	if err := a.ensureClient(); err != nil {
		return acp.ForkSessionResponse{}, err
	}
	a.mu.Lock()
	source, inMem := a.sessions[p.SessionID]
	a.mu.Unlock()

	var msgs []glm.Message
	var title *string
	model := glm.DefaultModelEnv()
	mode := ModeDefault
	if inMem {
		msgs = append([]glm.Message(nil), source.Messages...)
		title = source.Title
		if source.Model != "" {
			model = source.Model
		}
		if source.Mode != "" {
			mode = source.Mode
		}
	} else {
		persisted, _ := a.Store.Load(p.SessionID)
		if persisted == nil {
			return acp.ForkSessionResponse{}, &acp.RPCError{Code: -32001, Message: "session not found: " + p.SessionID}
		}
		msgs = append([]glm.Message(nil), persisted.Messages...)
		title = persisted.Title
		if persisted.Model != "" {
			model = persisted.Model
		}
		if persisted.Mode != "" {
			mode = persisted.Mode
		}
	}
	// Tag fork title.
	if title != nil {
		t := *title + " (fork)"
		title = &t
	}

	id := newSessionID()
	now := nowRFC3339()
	a.mu.Lock()
	a.sessions[id] = &sessionState{
		ID: id, Cwd: p.Cwd, Model: model, Mode: mode,
		Messages:  msgs,
		Title:     title,
		UpdatedAt: now,
		tools:     a.availableTools(),
	}
	a.mu.Unlock()
	_ = a.Store.Save(sessionstore.PersistedSession{
		SessionID: id, Cwd: p.Cwd, Messages: msgs,
		Title: title, UpdatedAt: now, Model: model, Mode: mode,
	})
	return acp.ForkSessionResponse{SessionID: id, Models: a.modelState(model), Modes: modesState(mode)}, nil
}

// CloseSession persists final state, cancels any in-flight prompt, and
// discards in-memory state.
func (a *Agent) CloseSession(_ context.Context, p acp.CloseSessionParams) (any, error) {
	a.mu.Lock()
	s, ok := a.sessions[p.SessionID]
	if ok {
		delete(a.sessions, p.SessionID)
	}
	a.mu.Unlock()
	if s == nil {
		return map[string]any{}, nil
	}
	// Cancel any in-flight prompt so subsequent prompts can't keep mutating
	// session state after the client has closed the session.
	s.cancelMu.Lock()
	if s.cancelCurrent != nil {
		s.cancelCurrent()
	}
	s.cancelMu.Unlock()
	_ = a.Store.Save(sessionstore.PersistedSession{
		SessionID: s.ID, Cwd: s.Cwd, Messages: s.Messages,
		Title: s.Title, UpdatedAt: s.UpdatedAt, Model: s.Model, Mode: s.Mode,
	})
	return map[string]any{}, nil
}

// ListSessions merges in-memory sessions with persisted ones, in-memory wins.
func (a *Agent) ListSessions(_ context.Context, p acp.ListSessionsParams) (acp.ListSessionsResponse, error) {
	type item struct {
		Cwd       string
		Title     *string
		UpdatedAt string
	}
	merged := map[string]*item{}
	for _, m := range a.Store.ListMetadata() {
		merged[m.SessionID] = &item{Cwd: m.Cwd, Title: m.Title, UpdatedAt: m.UpdatedAt}
	}
	a.mu.Lock()
	for id, s := range a.sessions {
		merged[id] = &item{Cwd: s.Cwd, Title: s.Title, UpdatedAt: s.UpdatedAt}
	}
	a.mu.Unlock()

	out := make([]acp.SessionListItem, 0, len(merged))
	for id, it := range merged {
		if p.Cwd != "" && it.Cwd != p.Cwd {
			continue
		}
		out = append(out, acp.SessionListItem{
			SessionID: id, Cwd: it.Cwd, Title: it.Title, UpdatedAt: it.UpdatedAt,
		})
	}
	// Newest-first.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].UpdatedAt < out[j].UpdatedAt; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return acp.ListSessionsResponse{Sessions: out}, nil
}

// SetSessionMode validates the requested mode, persists it, and emits a
// current_mode_update notification so the client UI refreshes.
func (a *Agent) SetSessionMode(_ context.Context, p acp.SetModeParams) (any, error) {
	if !isValidMode(p.ModeID) {
		return nil, &acp.RPCError{
			Code:    -32602,
			Message: fmt.Sprintf("Invalid modeId: %s. Valid modes are: %s", p.ModeID, strings.Join(ValidModes, ", ")),
		}
	}
	a.mu.Lock()
	s, ok := a.sessions[p.SessionID]
	if !ok {
		a.mu.Unlock()
		return nil, &acp.RPCError{Code: -32001, Message: "session not found: " + p.SessionID}
	}
	s.Mode = p.ModeID
	s.UpdatedAt = nowRFC3339()
	a.mu.Unlock()
	_ = a.Store.Save(sessionstore.PersistedSession{
		SessionID: s.ID, Cwd: s.Cwd, Messages: s.Messages,
		Title: s.Title, UpdatedAt: s.UpdatedAt, Model: s.Model, Mode: s.Mode,
	})
	a.notifyUpdate(p.SessionID, map[string]any{
		"sessionUpdate": "current_mode_update",
		"currentModeId": p.ModeID,
	})
	return map[string]any{}, nil
}

// SetSessionModel switches the model for a session and emits a
// session_info_update notification.
func (a *Agent) SetSessionModel(_ context.Context, p acp.SetModelParams) (any, error) {
	a.mu.Lock()
	s, ok := a.sessions[p.SessionID]
	if !ok {
		a.mu.Unlock()
		return nil, &acp.RPCError{Code: -32001, Message: "session not found: " + p.SessionID}
	}
	s.Model = p.ModelID
	s.UpdatedAt = nowRFC3339()
	updatedAt := s.UpdatedAt
	a.mu.Unlock()
	a.notifyUpdate(p.SessionID, map[string]any{
		"sessionUpdate": "session_info_update",
		"updatedAt":     updatedAt,
	})
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

// Prompt drives the GLM chat-completions loop until the model finishes,
// returns a tool call we run locally, or the user cancels.
func (a *Agent) Prompt(ctx context.Context, p acp.PromptParams) (acp.PromptResponse, error) {
	if err := a.ensureClient(); err != nil {
		return acp.PromptResponse{}, err
	}
	a.mu.Lock()
	s, ok := a.sessions[p.SessionID]
	a.mu.Unlock()
	if !ok {
		return acp.PromptResponse{}, &acp.RPCError{Code: -32001, Message: "session not found: " + p.SessionID}
	}

	// Per-session serialization: a follow-up prompt waits for the previous
	// loop to fully unwind before mutating shared session state.
	s.promptMu.Lock()
	defer s.promptMu.Unlock()

	// Per-prompt cancellable context registered for session/cancel.
	promptCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.cancelMu.Lock()
	s.cancelCurrent = cancel
	s.cancelMu.Unlock()
	defer func() {
		s.cancelMu.Lock()
		s.cancelCurrent = nil
		s.cancelMu.Unlock()
	}()

	if logger.IsDebug() {
		for _, line := range imagepre.BuildPromptBlockDiagnosticLines(p.Prompt) {
			logger.Debugf("%s", line)
		}
	}

	pre := imagepre.Preprocess(promptCtx, p.Prompt, a.visionClient())
	defer func() {
		for _, fn := range pre.Cleanups {
			fn()
		}
	}()
	userText := imagepre.RenderToString(pre.Blocks)
	logger.Debugf("session/prompt sessionId=%s blocks=%d userTextLen=%d", p.SessionID, len(p.Prompt), len(userText))
	s.Messages = append(s.Messages, glm.Message{Role: "user", Content: userText})

	exec := &executor.Executor{
		Conn:       a.Conn,
		SessionID:  p.SessionID,
		SessionCwd: s.Cwd,
		MCP:        a.MCP,
		Vision:     a.Vision,
		Mode:       s.Mode,
	}

	// Prepare the system prompt once per turn.
	tools := s.tools
	if len(tools) == 0 {
		tools = a.availableTools()
	}
	toolNames := make([]string, len(tools))
	for i, t := range tools {
		toolNames[i] = t.Function.Name
	}
	system := systemprompt.Build(systemprompt.Input{
		Cwd: s.Cwd, Tools: toolNames,
		AgentsMD: systemprompt.LoadProjectContext(s.Cwd),
	})
	messages := append([]glm.Message{{Role: "system", Content: system}}, s.Messages...)

	maxTurns := a.MaxTurns
	if maxTurns <= 0 {
		maxTurns = DefaultMaxTurns
	}

	stop := "max_turn_requests"
	var lastUsage *glm.Usage
	overflowRetries := 0
	for iter := 0; iter < maxTurns; iter++ {
		if promptCtx.Err() != nil {
			stop = "cancelled"
			break
		}
		// Proactive compaction: if history exceeds ~90% of the model's window.
		window := glm.ContextWindow(s.Model)
		if glm.EstimateTokens(messages) > (window*9)/10 {
			messages = glm.Compact(messages, (window*8)/10, 10)
		}

		// Sync the executor's mode so changes mid-turn take effect immediately.
		exec.Mode = s.Mode

		chunks, errs := a.GLM.StreamChat(promptCtx, messages, glm.StreamOptions{Model: s.Model, Tools: tools})

		var assistantText, assistantThought string
		var toolCalls []glm.ToolCall
		var streamStop string
		for c := range chunks {
			if c.Text != "" {
				assistantText += c.Text
				a.notifyUpdate(p.SessionID, map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"content":       map[string]any{"type": "text", "text": c.Text},
				})
			}
			if c.Thinking != "" {
				assistantThought += c.Thinking
				a.notifyUpdate(p.SessionID, map[string]any{
					"sessionUpdate": "agent_thought_chunk",
					"content":       map[string]any{"type": "text", "text": c.Thinking},
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
			if errors.Is(err, context.Canceled) || promptCtx.Err() != nil {
				stop = "cancelled"
				break
			}
			var apiErr *glm.APIError
			if errors.As(err, &apiErr) && apiErr.IsContextOverflow() && overflowRetries < 1 {
				// Emergency compaction: aggressive (~70%) then retry once.
				logger.Debugf("prompt: context overflow detected; emergency compaction")
				window := glm.ContextWindow(s.Model)
				messages = glm.Compact(messages, (window*7)/10, 10)
				overflowRetries++
				iter--
				continue
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
			stop = mapStopReason(streamStop)
			break
		}
		// Execute every tool call, append tool messages, and loop.
		for _, tc := range toolCalls {
			if promptCtx.Err() != nil {
				stop = "cancelled"
				break
			}
			res := exec.Execute(promptCtx, tc.ID, tc.Name, tc.Arguments)
			toolMsg := glm.Message{Role: "tool", ToolCallID: tc.ID, Content: res.Content}
			s.Messages = append(s.Messages, toolMsg)
			messages = append(messages, toolMsg)
		}
		if stop == "cancelled" {
			break
		}
	}

	s.UpdatedAt = nowRFC3339()

	// Derive a title from the first user message if we don't have one yet.
	titleUpdate := map[string]any{}
	if s.Title == nil {
		derived := deriveTitle(userText)
		if derived == "" {
			derived = "New conversation"
		}
		s.Title = &derived
		titleUpdate["title"] = derived
	}

	// Emit session_info_update so clients refresh metadata after each prompt.
	infoUpdate := map[string]any{
		"sessionUpdate": "session_info_update",
		"updatedAt":     s.UpdatedAt,
	}
	for k, v := range titleUpdate {
		infoUpdate[k] = v
	}
	a.notifyUpdate(p.SessionID, infoUpdate)

	_ = a.Store.Save(sessionstore.PersistedSession{
		SessionID: s.ID, Cwd: s.Cwd, Messages: s.Messages,
		Title: s.Title, UpdatedAt: s.UpdatedAt, Model: s.Model, Mode: s.Mode,
	})

	resp := acp.PromptResponse{StopReason: stop, UserMessageID: p.MessageID}
	if lastUsage != nil {
		resp.Usage = lastUsage
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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

func modesState(current string) *acp.SessionModeState {
	return &acp.SessionModeState{
		AvailableModes: []acp.SessionModeInfo{
			{ID: ModeDefault, Name: "Ask for permission", Description: "Prompt before edits and commands."},
			{ID: ModeAcceptEdits, Name: "Auto-approve edits", Description: "Edits run without prompting. Commands still prompt."},
			{ID: ModeBypassPerms, Name: "Bypass all permissions", Description: "Edits and commands run without prompting."},
		},
		CurrentModeID: current,
	}
}

// availableTools returns the tool defs surfaced to GLM, gated by client caps.
//
//   - read_file / list_files: always included.
//   - write_file: included unless fs.writeTextFile == false in clientCaps.
//   - run_command: always included (we run locally, no client need).
//   - web_search / web_reader: always included.
//   - image_analysis: included when a Vision client is configured.
func (a *Agent) availableTools() []definitions.Tool {
	wantImage := a.Vision != nil
	wantWrite := true
	if cap, ok := a.clientCapabilities["fs"].(map[string]any); ok {
		if v, ok := cap["writeTextFile"].(bool); ok && !v {
			wantWrite = false
		}
	}
	all := definitions.All()
	out := make([]definitions.Tool, 0, len(all))
	for _, t := range all {
		name := t.Function.Name
		if name == "image_analysis" && !wantImage {
			continue
		}
		if name == "write_file" && !wantWrite {
			continue
		}
		out = append(out, t)
	}
	return out
}

func (a *Agent) visionClient() imagepre.VisionClient {
	if a.Vision == nil {
		return nil
	}
	// executor.Vision and imagepre.VisionClient share the same method shape.
	return a.Vision
}

func (a *Agent) notifyUpdate(sessionID string, update map[string]any) {
	if a.Conn == nil {
		return
	}
	_ = a.Conn.SendNotification("session/update", acp.SessionUpdateParams{
		SessionID: sessionID, Update: update,
	})
}

func (a *Agent) replayMessages(_ context.Context, sessionID string, msgs []glm.Message) {
	for _, m := range msgs {
		var text string
		switch m.Role {
		case "user":
			text = stringContent(m.Content)
			if text == "" {
				continue
			}
			a.notifyUpdate(sessionID, map[string]any{
				"sessionUpdate": "user_message_chunk",
				"content":       map[string]any{"type": "text", "text": text},
			})
		case "assistant":
			text = stringContent(m.Content)
			if text == "" {
				continue
			}
			a.notifyUpdate(sessionID, map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content":       map[string]any{"type": "text", "text": text},
			})
		}
	}
}

func mapStopReason(s string) string {
	switch s {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "end_turn"
	case "content_filter":
		return "refusal"
	default:
		if s == "" {
			return "end_turn"
		}
		return s
	}
}

func deriveTitle(text string) string {
	// Collapse whitespace and clip to 80 chars.
	out := strings.Join(strings.Fields(text), " ")
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}

func stringContent(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, p := range c {
			if mp, ok := p.(map[string]any); ok {
				if s, ok := mp["text"].(string); ok {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "")
	}
	return ""
}

func isValidMode(id string) bool {
	for _, v := range ValidModes {
		if v == id {
			return true
		}
	}
	return false
}

func newSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func disabledByEnv(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return v == "false" || v == "0"
}
