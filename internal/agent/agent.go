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

	"github.com/ziozzang/agentbridge/internal/acp"
	"github.com/ziozzang/agentbridge/internal/agentprofiles"
	contextcompact "github.com/ziozzang/agentbridge/internal/compaction"
	"github.com/ziozzang/agentbridge/internal/config"
	"github.com/ziozzang/agentbridge/internal/credentials"
	"github.com/ziozzang/agentbridge/internal/logger"
	"github.com/ziozzang/agentbridge/internal/mcpconfig"
	codexoauth "github.com/ziozzang/agentbridge/internal/oauth/codex"
	copilotoauth "github.com/ziozzang/agentbridge/internal/oauth/copilot"
	googleoauth "github.com/ziozzang/agentbridge/internal/oauth/google"
	xaioauth "github.com/ziozzang/agentbridge/internal/oauth/xai"
	"github.com/ziozzang/agentbridge/internal/observability"
	"github.com/ziozzang/agentbridge/internal/plugins"
	_ "github.com/ziozzang/agentbridge/internal/plugins/duckdb" // register duckdb stub
	_ "github.com/ziozzang/agentbridge/internal/plugins/jina"   // register Jina tools
	_ "github.com/ziozzang/agentbridge/internal/plugins/ollamasearch"
	_ "github.com/ziozzang/agentbridge/internal/plugins/openaiembed"
	_ "github.com/ziozzang/agentbridge/internal/plugins/sqlite" // register sqlite plugin
	_ "github.com/ziozzang/agentbridge/internal/plugins/xai"
	"github.com/ziozzang/agentbridge/internal/protocol/imagepre"
	"github.com/ziozzang/agentbridge/internal/protocol/sessionstore"
	"github.com/ziozzang/agentbridge/internal/protocol/systemprompt"
	"github.com/ziozzang/agentbridge/internal/provider"
	_ "github.com/ziozzang/agentbridge/internal/provider/anthropic"  // register anthropic
	_ "github.com/ziozzang/agentbridge/internal/provider/bedrock"    // register bedrock-converse
	_ "github.com/ziozzang/agentbridge/internal/provider/claudecode" // register claude-code-cli
	_ "github.com/ziozzang/agentbridge/internal/provider/codexnative"
	"github.com/ziozzang/agentbridge/internal/provider/glm"
	_ "github.com/ziozzang/agentbridge/internal/provider/glm/preset" // register glm kind
	_ "github.com/ziozzang/agentbridge/internal/provider/google"     // register google
	_ "github.com/ziozzang/agentbridge/internal/provider/llamacpp"   // register llama.cpp
	_ "github.com/ziozzang/agentbridge/internal/provider/ollama"     // register ollama
	_ "github.com/ziozzang/agentbridge/internal/provider/openaichat" // register openai-chat
	_ "github.com/ziozzang/agentbridge/internal/provider/openairesp" // register openai-responses
	"github.com/ziozzang/agentbridge/internal/provider/pipeline"
	_ "github.com/ziozzang/agentbridge/internal/provider/router" // register model router
	"github.com/ziozzang/agentbridge/internal/tools/clienttools"
	"github.com/ziozzang/agentbridge/internal/tools/definitions"
	"github.com/ziozzang/agentbridge/internal/tools/executor"
	"github.com/ziozzang/agentbridge/internal/tools/sessionmcp"
	"github.com/ziozzang/agentbridge/internal/tools/visionmcp"
	"github.com/ziozzang/agentbridge/internal/tools/zaimcp"
)

// Version is reported in the initialize response.
const Version = "1.0.0"

// AgentName is reported in the initialize response.
const AgentName = "agentbridge"

// DefaultMaxTurns is the default per-prompt iteration cap.
const DefaultMaxTurns = 20

// Session mode identifiers.
const (
	ModeDefault        = "default"
	ModeAcceptEdits    = "accept_edits"
	ModeBypassPerms    = "bypass_permissions"
	ModeProviderNative = "provider_native"
)

// ValidModes is the set of session mode IDs we accept.
var ValidModes = []string{ModeDefault, ModeAcceptEdits, ModeBypassPerms}

// Notifier is the subset of acp.Conn the agent uses for outbound traffic.
// Tests substitute a stub that records notifications and permission calls.
type Notifier interface {
	SendNotification(method string, params any) error
	Call(ctx context.Context, method string, params any, result any) error
}

// Agent is the harness's ACP Agent. It owns the per-session prompt loop,
// history, model/mode state, and dispatches tool calls back to the
// connected ACP client. The Provider field abstracts the upstream LLM and
// is selected at startup via AGENTBRIDGE_PROVIDER + provider templates.
type Agent struct {
	Conn     Notifier
	Store    *sessionstore.Store
	Provider provider.Provider // active LLM provider (built from config)
	GLM      *glm.Client       // retained for tests; nil in normal runs
	Plugins  *plugins.Active   // optional plugins (sqlite, duckdb, …)
	MCP      executor.MCPCaller
	Vision   executor.Vision
	MaxTurns int
	Profiles []agentprofiles.Profile

	// clientCapabilities captured at `initialize` time. Used to gate the
	// agent's advertised tool surface and downstream tool behaviour.
	clientCapabilities map[string]any
	clientTools        []definitions.Tool

	mu       sync.Mutex
	sessions map[string]*sessionState
}

// sessionState is the in-memory state for a session.
type sessionState struct {
	ID           string
	Cwd          string
	Model        string
	Mode         string
	Messages     []glm.Message
	Title        *string
	UpdatedAt    string
	NativeAgent  bool
	Checkpoints  []sessionstore.Checkpoint
	ActiveSkills []sessionstore.ActiveSkill
	CacheEpoch   int

	// Per-session locks: promptMu serializes prompts; cancelMu protects
	// cancelCurrent; promptDone unblocks waiters when a prompt finishes.
	promptMu      sync.Mutex
	cancelMu      sync.Mutex
	cancelCurrent context.CancelFunc

	// tools advertised for this session (varies with MCP discovery / caps).
	tools []definitions.Tool

	// sessionMcp is the client for session-scoped MCP servers (nil if none).
	sessionMcp sessionMcpClient
}

// sessionMcpClient is the interface to session-scoped MCP servers.
type sessionMcpClient interface {
	ToolDefinitions() []definitions.Tool
	CallTool(ctx context.Context, fullName string, args map[string]any) (string, error)
	Dispose()
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
		Profiles: loadProfiles(),
		sessions: map[string]*sessionState{},
	}
}

func loadProfiles() []agentprofiles.Profile {
	profiles, err := agentprofiles.Load()
	if err != nil {
		logger.Warnf("agent profiles: %v", err)
		return nil
	}
	return profiles
}

// SetConn wires the JSON-RPC connection.
func (a *Agent) SetConn(c *acp.Conn) { a.Conn = c }

// Initialize handles the ACP `initialize` method.
func (a *Agent) Initialize(_ context.Context, p acp.InitializeParams) (acp.InitializeResponse, error) {
	a.mu.Lock()
	a.clientCapabilities = p.ClientCapabilities
	a.clientTools = parseClientTools(p.ClientCapabilities)
	a.mu.Unlock()

	imageAllowed := !disabledByEnv("AGENTBRIDGE_GLM_PROMPT_IMAGES", "ACP_GLM_PROMPT_IMAGES")
	negotiated := p.ProtocolVersion
	if negotiated > acp.ProtocolVersion {
		negotiated = acp.ProtocolVersion
	}
	resp := acp.InitializeResponse{
		ProtocolVersion: negotiated,
		AgentInfo:       acp.AgentInfo{Name: AgentName, Version: Version},
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession:     true,
			MCPCapabilities: acp.MCPCapabilities{HTTP: true, Stdio: true},
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

// streamChat dispatches the streaming call to the active provider, falling
// back to the legacy *glm.Client when tests pre-populate Agent.GLM
// directly. Both paths produce the same Chunk/error stream types because
// they share aliased shapes via internal/provider.
func (a *Agent) streamChat(ctx context.Context, msgs []glm.Message, opts glm.StreamOptions) (<-chan glm.Chunk, <-chan error) {
	if a.Provider != nil {
		opts = provider.PrepareStreamOptions(a.Provider, opts)
		return a.Provider.StreamChat(ctx, msgs, opts)
	}
	return a.GLM.StreamChat(ctx, msgs, opts)
}

// defaultModel returns the active provider's default model, falling back
// to the legacy GLM default when no provider is configured (test-only
// path).
func (a *Agent) defaultModel() string {
	if a.Provider != nil {
		return a.Provider.DefaultModel()
	}
	return glm.DefaultModelEnv()
}

// contextWindow returns the per-model context window for the active
// provider, falling back to the legacy GLM table for test paths.
func (a *Agent) contextWindow(model string) int {
	if a.Provider != nil {
		return a.Provider.ContextWindow(model)
	}
	return glm.ContextWindow(model)
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
	model := a.defaultModel()
	mode := ModeDefault
	nativeAgent := provider.UsesNativeAgentLoop(a.Provider)
	if nativeAgent {
		mode = ModeProviderNative
	}
	tools := a.availableTools()

	// Connect to session-scoped MCP servers.
	var mcpClient sessionMcpClient
	if nativeAgent {
		tools = nil
	} else if specs, err := configuredMCPServers(p.MCPServers); err != nil {
		logger.Debugf("session/new: failed to parse MCP servers: %v", err)
	} else if len(specs) > 0 {
		client, err := sessionmcp.New(specs)
		if err != nil {
			logger.Debugf("session/new: failed to connect MCP servers: %v", err)
		} else {
			mcpClient = client
			tools = append(tools, client.ToolDefinitions()...)
		}
	}

	a.mu.Lock()
	a.sessions[id] = &sessionState{
		ID: id, Cwd: p.Cwd, Model: model, Mode: mode,
		Messages: nil, UpdatedAt: nowRFC3339(), tools: tools, sessionMcp: mcpClient, NativeAgent: nativeAgent,
	}
	a.mu.Unlock()
	a.recordSessionSnapshot(id)
	logger.Debugf("session/new id=%s cwd=%s model=%s tools=%d", id, p.Cwd, model, len(tools))
	return acp.NewSessionResponse{
		SessionID: id,
		Models:    a.modelState(model),
		Modes:     a.sessionModes(mode),
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
		model = a.defaultModel()
	}
	mode := persisted.Mode
	nativeAgent := provider.UsesNativeAgentLoop(a.Provider)
	if mode == "" {
		if nativeAgent {
			mode = ModeProviderNative
		} else {
			mode = ModeDefault
		}
	}
	if nativeAgent {
		mode = ModeProviderNative
	} else if mode == "" {
		mode = ModeDefault
	}
	tools := a.availableTools()

	// Connect to session-scoped MCP servers.
	var mcpClient sessionMcpClient
	if nativeAgent {
		tools = nil
	} else if specs, err := configuredMCPServers(p.MCPServers); err != nil {
		logger.Debugf("session/load: failed to parse MCP servers: %v", err)
	} else if len(specs) > 0 {
		client, err := sessionmcp.New(specs)
		if err != nil {
			logger.Debugf("session/load: failed to connect MCP servers: %v", err)
		} else {
			mcpClient = client
			tools = append(tools, client.ToolDefinitions()...)
		}
	}

	a.mu.Lock()
	a.sessions[p.SessionID] = &sessionState{
		ID: p.SessionID, Cwd: p.Cwd, Model: model, Mode: mode,
		Messages: persisted.Messages, Title: persisted.Title, UpdatedAt: persisted.UpdatedAt,
		Checkpoints: persisted.Checkpoints, ActiveSkills: persisted.ActiveSkills, CacheEpoch: persisted.CacheEpoch,
		tools: tools, sessionMcp: mcpClient, NativeAgent: nativeAgent,
	}
	a.mu.Unlock()
	a.recordSessionSnapshot(p.SessionID)

	a.replayMessages(ctx, p.SessionID, persisted.Messages)

	return acp.LoadSessionResponse{
		Models: a.modelState(model),
		Modes:  a.sessionModes(mode),
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
		model = a.defaultModel()
	}
	mode := persisted.Mode
	nativeAgent := provider.UsesNativeAgentLoop(a.Provider)
	if mode == "" {
		if nativeAgent {
			mode = ModeProviderNative
		} else {
			mode = ModeDefault
		}
	}
	if nativeAgent {
		mode = ModeProviderNative
	} else if mode == "" {
		mode = ModeDefault
	}
	tools := a.availableTools()

	// Connect to session-scoped MCP servers.
	var mcpClient sessionMcpClient
	if nativeAgent {
		tools = nil
	} else if specs, err := configuredMCPServers(p.MCPServers); err != nil {
		logger.Debugf("session/resume: failed to parse MCP servers: %v", err)
	} else if len(specs) > 0 {
		client, err := sessionmcp.New(specs)
		if err != nil {
			logger.Debugf("session/resume: failed to connect MCP servers: %v", err)
		} else {
			mcpClient = client
			tools = append(tools, client.ToolDefinitions()...)
		}
	}

	a.mu.Lock()
	a.sessions[p.SessionID] = &sessionState{
		ID: p.SessionID, Cwd: p.Cwd, Model: model, Mode: mode,
		Messages: persisted.Messages, Title: persisted.Title, UpdatedAt: persisted.UpdatedAt,
		Checkpoints: persisted.Checkpoints, ActiveSkills: persisted.ActiveSkills, CacheEpoch: persisted.CacheEpoch,
		tools: tools, sessionMcp: mcpClient, NativeAgent: nativeAgent,
	}
	a.mu.Unlock()
	a.recordSessionSnapshot(p.SessionID)
	return acp.LoadSessionResponse{Models: a.modelState(model), Modes: a.sessionModes(mode)}, nil
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
	var checkpoints []sessionstore.Checkpoint
	var activeSkills []sessionstore.ActiveSkill
	var cacheEpoch int
	model := a.defaultModel()
	mode := ModeDefault
	nativeAgent := provider.UsesNativeAgentLoop(a.Provider)
	if inMem {
		msgs = append([]glm.Message(nil), source.Messages...)
		title = source.Title
		checkpoints = append([]sessionstore.Checkpoint(nil), source.Checkpoints...)
		activeSkills = append([]sessionstore.ActiveSkill(nil), source.ActiveSkills...)
		cacheEpoch = source.CacheEpoch
		if source.Model != "" {
			model = source.Model
		}
		if source.Mode != "" {
			mode = source.Mode
		}
		nativeAgent = source.NativeAgent
	} else {
		persisted, _ := a.Store.Load(p.SessionID)
		if persisted == nil {
			return acp.ForkSessionResponse{}, &acp.RPCError{Code: -32001, Message: "session not found: " + p.SessionID}
		}
		msgs = append([]glm.Message(nil), persisted.Messages...)
		title = persisted.Title
		checkpoints = append([]sessionstore.Checkpoint(nil), persisted.Checkpoints...)
		activeSkills = append([]sessionstore.ActiveSkill(nil), persisted.ActiveSkills...)
		cacheEpoch = persisted.CacheEpoch
		if persisted.Model != "" {
			model = persisted.Model
		}
		if persisted.Mode != "" {
			mode = persisted.Mode
		}
	}
	if nativeAgent {
		mode = ModeProviderNative
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
		Messages:     msgs,
		Title:        title,
		UpdatedAt:    now,
		Checkpoints:  checkpoints,
		ActiveSkills: activeSkills,
		CacheEpoch:   cacheEpoch,
		tools: func() []definitions.Tool {
			if nativeAgent {
				return nil
			}
			return a.availableTools()
		}(),
		NativeAgent: nativeAgent,
	}
	a.mu.Unlock()
	a.recordSessionSnapshot(id)
	_ = a.persistSession(a.sessions[id])
	return acp.ForkSessionResponse{SessionID: id, Models: a.modelState(model), Modes: a.sessionModes(mode)}, nil
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
	observability.DeleteSession(p.SessionID)
	// Cancel any in-flight prompt so subsequent prompts can't keep mutating
	// session state after the client has closed the session.
	s.cancelMu.Lock()
	if s.cancelCurrent != nil {
		s.cancelCurrent()
	}
	s.cancelMu.Unlock()
	// Dispose session MCP client if present.
	if s.sessionMcp != nil {
		s.sessionMcp.Dispose()
	}
	_ = a.persistSession(s)
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
	if !isValidMode(p.ModeID) && p.ModeID != ModeProviderNative {
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
	if s.NativeAgent && p.ModeID != ModeProviderNative {
		a.mu.Unlock()
		return nil, &acp.RPCError{Code: -32602, Message: "native-agent sessions only support modeId=provider_native"}
	}
	s.Mode = p.ModeID
	s.UpdatedAt = nowRFC3339()
	a.mu.Unlock()
	a.recordSessionSnapshot(p.SessionID)
	_ = a.persistSession(s)
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
	a.recordSessionSnapshot(p.SessionID)
	a.notifyUpdate(p.SessionID, map[string]any{
		"sessionUpdate": "session_info_update",
		"updatedAt":     updatedAt,
		"context":       a.contextSnapshot(s),
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
	if handled, resp, err := a.handleRuntimeCommand(ctx, s, p); handled || err != nil {
		return resp, err
	}
	if s.NativeAgent {
		return a.promptWithNativeProvider(ctx, s, p)
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
		Vision:     a.visionClient(),
		Mode:       s.Mode,
		SessionMCP: s.sessionMcp,
		Plugins:    a.Plugins,
	}

	// Prepare the system prompt once per turn.
	tools := s.tools
	if len(tools) == 0 {
		tools = a.availableTools()
	}
	tools = a.profileTools(s.Model, tools)
	toolNames := make([]string, len(tools))
	for i, t := range tools {
		toolNames[i] = t.Function.Name
	}
	system := systemprompt.Build(systemprompt.Input{
		Cwd: s.Cwd, Tools: toolNames,
		AgentsMD: systemprompt.LoadProjectContext(s.Cwd),
		Profile:  a.profilePrompt(s.Model),
	})
	if skillPrompt := a.activeSkillPrompt(s); skillPrompt != "" {
		system += "\n\n" + skillPrompt
	}
	messages := append([]glm.Message{{Role: "system", Content: system}}, s.Messages...)

	maxTurns := a.MaxTurns
	if maxTurns <= 0 {
		maxTurns = DefaultMaxTurns
	}

	stop := "max_turn_requests"
	var lastUsage *glm.Usage
	overflowRetries := 0
	compactSettings := loadCompactionSettings()
	for iter := 0; iter < maxTurns; iter++ {
		if promptCtx.Err() != nil {
			stop = "cancelled"
			break
		}
		// Proactive compaction: threshold and fallback behavior are runtime-configurable.
		window := a.contextWindow(a.effectiveModel(s.Model))
		if compactSettings.Enabled && contextcompact.EstimateTokens(messages) > compactSettings.ProactiveThreshold(window) {
			result := a.compactPromptMessages(promptCtx, messages, a.effectiveModel(s.Model), tools, compactSettings, compactSettings.TargetTokens(window), "proactive context compaction")
			if result.Compacted {
				logger.Debugf("prompt: compacted context tokens_before=%d tokens_after=%d", result.TokensBefore, contextcompact.EstimateTokens(result.Messages))
				messages = result.Messages
			} else if compactSettings.PruneFallbackEnabled {
				messages = contextcompact.PruneMessages(messages, compactSettings.TargetTokens(window), compactSettings.PreserveTurns)
			}
			if len(messages) > 0 {
				s.Messages = append([]glm.Message(nil), messages[1:]...)
			}
		}

		// Sync the executor's mode so changes mid-turn take effect immediately.
		exec.Mode = s.Mode

		chunks, errs := a.streamChat(promptCtx, messages, glm.StreamOptions{Model: a.effectiveModel(s.Model), Tools: tools, SessionID: p.SessionID})

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
			isOverflow := provider.IsContextOverflow(err) || (errors.As(err, &apiErr) && apiErr.IsContextOverflow())
			if isOverflow && overflowRetries < 1 {
				// Emergency compaction: aggressive (~70%) then retry once.
				logger.Debugf("prompt: context overflow detected; emergency compaction")
				window := a.contextWindow(a.effectiveModel(s.Model))
				result := a.compactPromptMessages(promptCtx, messages, a.effectiveModel(s.Model), tools, compactSettings, compactSettings.OverflowTargetTokens(window), "context overflow retry")
				if result.Compacted {
					logger.Debugf("prompt: emergency compacted context tokens_before=%d tokens_after=%d", result.TokensBefore, contextcompact.EstimateTokens(result.Messages))
					messages = result.Messages
				} else if compactSettings.PruneFallbackEnabled {
					messages = contextcompact.PruneMessages(messages, compactSettings.OverflowTargetTokens(window), compactSettings.PreserveTurns)
				}
				if len(messages) > 0 {
					s.Messages = append([]glm.Message(nil), messages[1:]...)
				}
				overflowRetries++
				iter--
				continue
			}
			return acp.PromptResponse{}, fmt.Errorf("model stream failed: %w", err)
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
		"context":       a.contextSnapshot(s),
	}
	for k, v := range titleUpdate {
		infoUpdate[k] = v
	}
	a.notifyUpdate(p.SessionID, infoUpdate)

	_ = a.persistSession(s)

	resp := acp.PromptResponse{StopReason: stop, UserMessageID: p.MessageID}
	if lastUsage != nil {
		resp.Usage = lastUsage
	}
	a.recordSessionSnapshot(p.SessionID)
	return resp, nil
}

func (a *Agent) promptWithNativeProvider(ctx context.Context, s *sessionState, p acp.PromptParams) (acp.PromptResponse, error) {
	s.promptMu.Lock()
	defer s.promptMu.Unlock()

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

	pre := imagepre.Preprocess(promptCtx, p.Prompt, a.visionClient())
	defer func() {
		for _, fn := range pre.Cleanups {
			fn()
		}
	}()
	userText := imagepre.RenderToString(pre.Blocks)
	logger.Debugf("session/prompt native sessionId=%s blocks=%d userTextLen=%d", p.SessionID, len(p.Prompt), len(userText))

	s.Messages = append(s.Messages, glm.Message{Role: "user", Content: userText})
	msgs := append([]glm.Message(nil), s.Messages...)
	streamOpts := provider.StreamOptions{Model: a.effectiveModel(s.Model), SessionID: p.SessionID}

	var lastUsage *glm.Usage
	stop := "stop"
	for attempt := 0; attempt < 2; attempt++ {
		chunks, errs := a.streamChat(promptCtx, msgs, streamOpts)
		var assistantText, assistantThought string
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
			if c.Usage != nil {
				lastUsage = c.Usage
			}
			if c.Done && c.StopReason != "" {
				stop = mapStopReason(c.StopReason)
			}
		}
		if err := <-errs; err != nil {
			if errors.Is(err, context.Canceled) || promptCtx.Err() != nil {
				stop = "cancelled"
				break
			}
			if attempt == 0 && provider.IsContextOverflow(err) {
				if compactor, ok := a.Provider.(provider.ConversationCompactor); ok {
					out, compactErr := compactor.CompactConversation(promptCtx, s.Messages, provider.PrepareCompactOptions(a.Provider, provider.CompactOptions{
						Model:     a.effectiveModel(s.Model),
						SessionID: p.SessionID,
						Reason:    "native-agent context overflow retry",
					}))
					if compactErr == nil && len(out) > 0 {
						s.Messages = append([]glm.Message(nil), out...)
						msgs = append([]glm.Message(nil), s.Messages...)
						continue
					}
				}
			}
			return acp.PromptResponse{}, fmt.Errorf("native provider stream failed: %w", err)
		}
		s.Messages = append(s.Messages, glm.Message{Role: "assistant", Content: assistantText})
		s.UpdatedAt = nowRFC3339()
		if s.Title == nil {
			derived := deriveTitle(userText)
			if derived == "" {
				derived = "New conversation"
			}
			s.Title = &derived
		}
		a.notifyUpdate(p.SessionID, map[string]any{
			"sessionUpdate": "session_info_update",
			"updatedAt":     s.UpdatedAt,
			"title":         s.Title,
			"context":       a.contextSnapshot(s),
		})
		_ = a.persistSession(s)
		a.recordSessionSnapshot(p.SessionID)
		resp := acp.PromptResponse{StopReason: stop, UserMessageID: p.MessageID}
		if lastUsage != nil {
			resp.Usage = lastUsage
		}
		return resp, nil
	}
	resp := acp.PromptResponse{StopReason: stop, UserMessageID: p.MessageID}
	if lastUsage != nil {
		resp.Usage = lastUsage
	}
	a.recordSessionSnapshot(p.SessionID)
	return resp, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ensureClient lazily constructs the active provider from configuration.
// If tests set Agent.GLM or Agent.Provider directly, that path is reused.
func (a *Agent) ensureClient() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Provider != nil || a.GLM != nil {
		if a.MCP == nil {
			a.MCP = zaimcp.New()
		}
		return nil
	}
	mfst, err := config.Load()
	if err != nil {
		return err
	}
	cfg, err := mfst.Resolve("")
	if err != nil {
		return err
	}
	// Back-compat: when no key is set, fall back to the credentials file
	// for the GLM provider (the original behaviour of glm.New).
	if cfg.APIKey == "" && (cfg.Kind == "glm" || cfg.Kind == "" || cfg.Kind == "openai-chat") {
		if k := credentials.Resolve(); k != "" {
			cfg.APIKey = k
		}
	}
	// Resolve any oauth:* tokens and attach non-secret auth metadata such as
	// ChatGPT account id when the token cache carries it.
	if rerr := resolveOAuthConfig(&cfg); rerr != nil {
		return rerr
	}
	if cfg.APIKey == "" && cfg.Kind != "ollama" && cfg.Kind != "llama.cpp" && cfg.Kind != "llamacpp" && cfg.Kind != "claude-code-cli" && cfg.Kind != "codex-app-server" && cfg.Kind != "router" {
		return errors.New("No API key configured. Set an API key via AGENTBRIDGE_API_KEY, AGENTBRIDGE_<PROVIDER>_API_KEY, a provider-specific env var (Z_AI_API_KEY/OPENAI_API_KEY/…), or run `agentbridge --setup`. Legacy ACP_HARNESS_* variables are still accepted.")
	}
	p, err := provider.Build(cfg)
	if err != nil {
		return err
	}
	a.Provider = pipeline.WrapFromConfig(p)
	observability.SetProvider(observability.ProviderState{
		Name:        cfg.Name,
		Kind:        cfg.Kind,
		Model:       a.Provider.DefaultModel(),
		BaseURL:     cfg.BaseURL,
		NativeAgent: provider.UsesNativeAgentLoop(a.Provider),
	})
	logger.Infof("active provider: %s (kind=%s, model=%s, base=%s)",
		cfg.Name, cfg.Kind, a.Provider.DefaultModel(), cfg.BaseURL)
	if a.MCP == nil {
		a.MCP = zaimcp.New()
	}
	if a.Plugins == nil {
		a.Plugins = plugins.LoadActive()
	}
	return nil
}

// resolveOAuthConfig resolves any `oauth:*` marker on cfg.APIKey.
var resolveOAuthConfig = func(cfg *provider.Config) error {
	if cfg == nil || !strings.HasPrefix(cfg.APIKey, "oauth:") {
		return nil
	}
	flavour := strings.TrimPrefix(cfg.APIKey, "oauth:")
	switch flavour {
	case "codex", "openai":
		r := codexoauth.NewForFlavour(flavour, "")
		tok, err := r.ResolveToken(context.Background())
		if err != nil {
			return err
		}
		cfg.APIKey = tok.AccessToken
		if tok.AccountID != "" {
			if cfg.Headers == nil {
				cfg.Headers = map[string]string{}
			}
			if cfg.Headers["ChatGPT-Account-ID"] == "" {
				cfg.Headers["ChatGPT-Account-ID"] = tok.AccountID
			}
		}
		return nil
	case "xai", "xai-oauth", "grok-oauth":
		tok, err := xaioauth.New("").ResolveToken(context.Background())
		if err != nil {
			return err
		}
		cfg.APIKey = tok.AccessToken
		return nil
	case "github-copilot", "copilot":
		tok, baseURL, err := copilotoauth.New("").ResolveToken(context.Background())
		if err != nil {
			return err
		}
		cfg.APIKey = tok
		if cfg.BaseURL == "" || cfg.BaseURL == copilotoauth.DefaultBaseURL {
			cfg.BaseURL = baseURL
		}
		if cfg.Headers == nil {
			cfg.Headers = map[string]string{}
		}
		for k, v := range copilotoauth.DefaultHeaders() {
			if cfg.Headers[k] == "" {
				cfg.Headers[k] = v
			}
		}
		return nil
	case "google", "google-vertex", "vertex":
		tok, err := googleoauth.New().Resolve(context.Background())
		if err != nil {
			return err
		}
		cfg.APIKey = tok
		if cfg.AuthHeader == "" {
			cfg.AuthHeader = "Authorization"
		}
		if cfg.AuthPrefix == "" {
			cfg.AuthPrefix = "Bearer "
		}
		return nil
	default:
		return fmt.Errorf("oauth resolver for %q is not registered", flavour)
	}
}

func (a *Agent) modelState(current string) *acp.SessionModelState {
	var models []glm.ModelInfo
	if a.Provider != nil {
		models = a.Provider.AvailableModels()
	}
	if len(models) == 0 {
		models = glm.AvailableModels()
	}
	out := make([]acp.ModelInfo, len(models))
	for i, m := range models {
		out[i] = acp.ModelInfo{ModelID: m.ModelID, Name: m.Name, Description: m.Description, Provider: m.Provider}
	}
	for _, p := range a.Profiles {
		name := p.Name
		desc := p.Description
		if desc == "" {
			desc = "Agent profile"
		}
		out = append(out, acp.ModelInfo{ModelID: name, Name: name, Description: desc})
	}
	return &acp.SessionModelState{AvailableModels: out, CurrentModelID: current}
}

func (a *Agent) sessionModes(current string) *acp.SessionModeState {
	if a.Provider != nil && provider.UsesNativeAgentLoop(a.Provider) {
		return &acp.SessionModeState{
			AvailableModes: []acp.SessionModeInfo{
				{ID: ModeProviderNative, Name: "Provider-native agent", Description: "The upstream provider owns the agentic loop and session runtime."},
			},
			CurrentModeID: ModeProviderNative,
		}
	}
	return modesState(current)
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
//   - web_search / web_reader: always included.
//   - image_analysis: included when a Vision client is configured.
//   - client_run_lua: included when client capabilities advertise lua support.
//   - client__*: included when the ACP client advertises client-owned tools.
func (a *Agent) availableTools() []definitions.Tool {
	wantImage := a.Vision != nil
	wantWrite := true
	wantLua := false
	if cap, ok := a.clientCapabilities["fs"].(map[string]any); ok {
		if v, ok := cap["writeTextFile"].(bool); ok && !v {
			wantWrite = false
		}
	}
	if v, ok := a.clientCapabilities["lua"].(bool); ok && v {
		wantLua = true
	}
	if v, ok := a.clientCapabilities["clientRunLua"].(bool); ok && v {
		wantLua = true
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
		if name == "client_run_lua" && !wantLua {
			continue
		}
		out = append(out, t)
	}
	if a.Plugins != nil {
		out = append(out, a.Plugins.Tools()...)
	}
	out = append(out, a.clientTools...)
	return out
}

func parseClientTools(caps map[string]any) []definitions.Tool {
	if len(caps) == 0 {
		return nil
	}
	raw, ok := caps["tools"]
	if !ok {
		raw, ok = caps["clientTools"]
	}
	if !ok {
		return nil
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var advertised []clienttools.AdvertisedTool
	if err := json.Unmarshal(body, &advertised); err != nil {
		return nil
	}
	return clienttools.ToolDefinitions(advertised)
}

func (a *Agent) recordSessionSnapshot(sessionID string) {
	a.mu.Lock()
	s := a.sessions[sessionID]
	a.mu.Unlock()
	if s == nil {
		return
	}
	observability.UpsertSession(observability.SessionState{
		SessionID:    s.ID,
		Cwd:          s.Cwd,
		Model:        s.Model,
		Mode:         s.Mode,
		UpdatedAt:    s.UpdatedAt,
		NativeAgent:  s.NativeAgent,
		MessageCount: len(s.Messages),
	})
}

func (a *Agent) profileByName(name string) *agentprofiles.Profile {
	for i := range a.Profiles {
		if a.Profiles[i].Name == name {
			return &a.Profiles[i]
		}
	}
	return nil
}

func (a *Agent) effectiveModel(model string) string {
	if p := a.profileByName(model); p != nil && p.TargetModel != "" {
		return p.TargetModel
	}
	return model
}

func (a *Agent) profilePrompt(model string) string {
	if p := a.profileByName(model); p != nil {
		return p.Prompt()
	}
	return ""
}

func (a *Agent) profileTools(model string, tools []definitions.Tool) []definitions.Tool {
	p := a.profileByName(model)
	if p == nil || len(p.Tools) == 0 {
		return tools
	}
	out := make([]definitions.Tool, 0, len(tools))
	for _, t := range tools {
		if matchesAny(t.Function.Name, p.Tools) {
			out = append(out, t)
		}
	}
	return out
}

func matchesAny(name string, patterns []string) bool {
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p == name {
			return true
		}
		if strings.HasSuffix(p, "*") && strings.HasPrefix(name, strings.TrimSuffix(p, "*")) {
			return true
		}
	}
	return false
}

func (a *Agent) visionClient() imagepre.VisionClient {
	if a.Vision == nil {
		// Lazy-init stdio Vision MCP client if API key is present.
		apiKey := credentials.Resolve()
		if apiKey != "" {
			a.Vision = visionmcp.New(apiKey)
		}
	}
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

func disabledByEnv(names ...string) bool {
	for _, name := range names {
		if v, ok := os.LookupEnv(name); ok {
			lv := strings.ToLower(strings.TrimSpace(v))
			return lv == "false" || lv == "0"
		}
	}
	return false
}

// parseMcpServers decodes an array of MCPServerSpec (json.RawMessage) into []acp.McpServer.
func parseMcpServers(specs []acp.MCPServerSpec) ([]acp.McpServer, error) {
	out := make([]acp.McpServer, 0, len(specs))
	for _, raw := range specs {
		var srv acp.McpServer
		if err := json.Unmarshal(raw, &srv); err != nil {
			return nil, fmt.Errorf("invalid MCP server spec: %w", err)
		}
		out = append(out, srv)
	}
	return out, nil
}

func configuredMCPServers(specs []acp.MCPServerSpec) ([]acp.McpServer, error) {
	out, err := mcpconfig.Load()
	if err != nil {
		return nil, err
	}
	if len(specs) == 0 {
		return out, nil
	}
	sessionSpecs, err := parseMcpServers(specs)
	if err != nil {
		return nil, err
	}
	return append(out, sessionSpecs...), nil
}
