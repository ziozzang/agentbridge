// Package httpcompat exposes a small HTTP compatibility API over the same
// provider abstraction used by the ACP agent.
package httpcompat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ziozzang/agentbridge/internal/config"
	"github.com/ziozzang/agentbridge/internal/credentials"
	"github.com/ziozzang/agentbridge/internal/logger"
	"github.com/ziozzang/agentbridge/internal/mcpconfig"
	codexoauth "github.com/ziozzang/agentbridge/internal/oauth/codex"
	xaioauth "github.com/ziozzang/agentbridge/internal/oauth/xai"
	"github.com/ziozzang/agentbridge/internal/plugins"
	_ "github.com/ziozzang/agentbridge/internal/plugins/duckdb"
	_ "github.com/ziozzang/agentbridge/internal/plugins/jina"
	_ "github.com/ziozzang/agentbridge/internal/plugins/ollamasearch"
	_ "github.com/ziozzang/agentbridge/internal/plugins/openaiembed"
	_ "github.com/ziozzang/agentbridge/internal/plugins/sqlite"
	_ "github.com/ziozzang/agentbridge/internal/plugins/xai"
	"github.com/ziozzang/agentbridge/internal/provider"
	_ "github.com/ziozzang/agentbridge/internal/provider/anthropic"
	_ "github.com/ziozzang/agentbridge/internal/provider/claudecode"
	_ "github.com/ziozzang/agentbridge/internal/provider/glm/preset"
	_ "github.com/ziozzang/agentbridge/internal/provider/ollama"
	_ "github.com/ziozzang/agentbridge/internal/provider/openaichat"
	_ "github.com/ziozzang/agentbridge/internal/provider/openairesp"
	_ "github.com/ziozzang/agentbridge/internal/provider/router"
	"github.com/ziozzang/agentbridge/internal/tools/sessionmcp"
)

// NewHandler returns an HTTP handler for OpenAI/Anthropic-style compatibility
// endpoints backed by the configured harness provider.
func NewHandler() http.Handler {
	mux := http.NewServeMux()
	h := &handler{
		tasks:         map[string]*a2aTask{},
		cancels:       map[string]context.CancelFunc{},
		responseStore: map[string]responseRecord{},
		plugins:       plugins.LoadActive(),
	}
	if specs, err := mcpconfig.Load(); err == nil && len(specs) > 0 {
		if client, err := sessionmcp.New(specs); err == nil {
			h.externalMCP = client
		} else {
			logger.Warnf("httpcompat: failed to connect configured MCP servers: %v", err)
		}
	} else if err != nil {
		logger.Warnf("httpcompat: failed to load MCP config: %v", err)
	}
	mux.HandleFunc("/health", h.health)
	mux.HandleFunc("/metrics", h.metrics)
	mux.HandleFunc("/metric", h.metrics)
	mux.HandleFunc("/openapi.json", h.openapi)
	mux.HandleFunc("/swagger.json", h.openapi)
	mux.HandleFunc("/swagger", h.swaggerUI)
	mux.HandleFunc("/docs", h.swaggerUI)
	mux.HandleFunc("/v1/openapi.json", h.openapi)
	mux.HandleFunc("/.well-known/agent-card.json", h.a2aAgentCard)
	mux.HandleFunc("/a2a/agent-card.json", h.a2aAgentCard)
	mux.HandleFunc("/v1/a2a/agent-card.json", h.a2aAgentCard)
	mux.HandleFunc("/a2a/rpc", h.a2aRPC)
	mux.HandleFunc("/v1/a2a/rpc", h.a2aRPC)
	mux.HandleFunc("/mcp", h.mcp)
	mux.HandleFunc("/v1/mcp", h.mcp)
	mux.HandleFunc("/tools/", h.toolHTTP)
	mux.HandleFunc("/v1/tools/", h.toolHTTP)
	mux.HandleFunc("/agui/run", h.aguiRun)
	mux.HandleFunc("/v1/agui/run", h.aguiRun)
	mux.HandleFunc("/v1/chat/completions", h.chatCompletions)
	mux.HandleFunc("/chat/completions", h.chatCompletions)
	mux.HandleFunc("/v1/responses/", h.responseByID)
	mux.HandleFunc("/responses/", h.responseByID)
	mux.HandleFunc("/v1/responses", h.responses)
	mux.HandleFunc("/responses", h.responses)
	mux.HandleFunc("/v1/messages", h.messages)
	mux.HandleFunc("/messages", h.messages)
	return h.instrument(mux)
}

type handler struct {
	mu            sync.Mutex
	tasks         map[string]*a2aTask
	cancels       map[string]context.CancelFunc
	responseStore map[string]responseRecord
	plugins       *plugins.Active
	externalMCP   *sessionmcp.Client
}

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      any           `json:"id,omitempty"`
	Result  any           `json:"result,omitempty"`
	Error   *jsonRPCError `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type a2aSendParams struct {
	Message             a2aMessage     `json:"message"`
	Model               string         `json:"model,omitempty"`
	Configuration       map[string]any `json:"configuration,omitempty"`
	AcceptedOutputModes []string       `json:"acceptedOutputModes,omitempty"`
	Metadata            map[string]any `json:"metadata,omitempty"`
}

type a2aTaskQuery struct {
	TaskID        string `json:"taskId,omitempty"`
	ContextID     string `json:"contextId,omitempty"`
	HistoryLength int    `json:"historyLength,omitempty"`
	PageSize      int    `json:"pageSize,omitempty"`
	PageToken     string `json:"pageToken,omitempty"`
}

type a2aTask struct {
	TaskID    string         `json:"taskId"`
	ContextID string         `json:"contextId"`
	Status    a2aTaskStatus  `json:"status"`
	History   []a2aMessage   `json:"history,omitempty"`
	Artifacts []a2aArtifact  `json:"artifacts,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type a2aTaskStatus struct {
	State     string      `json:"state"`
	Message   *a2aMessage `json:"message,omitempty"`
	Timestamp string      `json:"timestamp,omitempty"`
}

type a2aMessage struct {
	Role      string         `json:"role"`
	Parts     []a2aPart      `json:"parts"`
	MessageID string         `json:"messageId,omitempty"`
	ContextID string         `json:"contextId,omitempty"`
	TaskID    string         `json:"taskId,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type a2aPart struct {
	Kind     string         `json:"kind,omitempty"`
	Type     string         `json:"type,omitempty"`
	Text     string         `json:"text,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
	File     map[string]any `json:"file,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type a2aArtifact struct {
	ArtifactID string    `json:"artifactId,omitempty"`
	Name       string    `json:"name,omitempty"`
	Parts      []a2aPart `json:"parts,omitempty"`
}

type requestMeta struct {
	RequestID   string         `json:"request_id,omitempty"`
	Cache       map[string]any `json:"cache,omitempty"`
	CacheStatus string         `json:"cache_status,omitempty"`
}

type responseRecord struct {
	ID        string             `json:"id"`
	Model     string             `json:"model,omitempty"`
	Messages  []provider.Message `json:"messages,omitempty"`
	Output    string             `json:"output_text,omitempty"`
	CreatedAt int64              `json:"created_at"`
	Metadata  map[string]any     `json:"metadata,omitempty"`
}

type commonRequest struct {
	Metadata     map[string]any `json:"metadata,omitempty"`
	Cache        map[string]any `json:"cache,omitempty"`
	CacheControl map[string]any `json:"cache_control,omitempty"`
}

type chatRequest struct {
	commonRequest
	Model    string             `json:"model"`
	Messages []provider.Message `json:"messages"`
	Stream   bool               `json:"stream"`
}

type responsesRequest struct {
	commonRequest
	Model                string         `json:"model"`
	Input                any            `json:"input"`
	Instructions         string         `json:"instructions,omitempty"`
	PreviousResponseID   string         `json:"previous_response_id,omitempty"`
	Conversation         any            `json:"conversation,omitempty"`
	Tools                []any          `json:"tools,omitempty"`
	ParallelToolCalls    *bool          `json:"parallel_tool_calls,omitempty"`
	MaxToolCalls         int            `json:"max_tool_calls,omitempty"`
	MaxOutputTokens      int            `json:"max_output_tokens,omitempty"`
	Prompt               map[string]any `json:"prompt,omitempty"`
	PromptCacheKey       string         `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string         `json:"prompt_cache_retention,omitempty"`
	Include              []string       `json:"include,omitempty"`
	Store                *bool          `json:"store,omitempty"`
	Stream               bool           `json:"stream"`
}

type messagesRequest struct {
	commonRequest
	Model    string             `json:"model"`
	Messages []provider.Message `json:"messages"`
	Stream   bool               `json:"stream"`
}

func (h *handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (h *handler) a2aAgentCard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	base := "http://" + r.Host
	if r.TLS != nil {
		base = "https://" + r.Host
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"protocolVersion":    "1.0",
		"name":               "agentbridge",
		"description":        "A2A bridge backed by the configured AgentBridge provider",
		"url":                base + "/a2a/rpc",
		"preferredTransport": "JSONRPC",
		"capabilities": map[string]any{
			"streaming":              true,
			"pushNotifications":      false,
			"stateTransitionHistory": true,
		},
		"defaultInputModes":  []string{"text/plain", "application/json"},
		"defaultOutputModes": []string{"text/plain", "application/json"},
		"skills": []map[string]any{{
			"id":          "chat",
			"name":        "Chat",
			"description": "Send prompts to the configured provider",
			"inputModes":  []string{"text/plain", "application/json"},
			"outputModes": []string{"text/plain", "application/json"},
		}},
	})
}

func (h *handler) a2aRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req jsonRPCRequest
	if err := decodeBody(r, &req); err != nil {
		writeJSONRPC(w, http.StatusBadRequest, jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   rpcError(-32700, err.Error(), nil),
		})
		return
	}
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		writeJSONRPC(w, http.StatusBadRequest, jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   rpcError(-32600, "jsonrpc must be 2.0", nil),
		})
		return
	}

	switch normalizeA2AMethod(req.Method) {
	case "SendMessage":
		task, err := h.a2aSendMessage(r.Context(), req.Params)
		if err != nil {
			writeJSONRPC(w, http.StatusOK, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcError(-32602, err.Error(), nil)})
			return
		}
		writeJSONRPC(w, http.StatusOK, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: task})
	case "SendStreamingMessage":
		h.a2aSendStreamingMessage(w, r, req)
	case "GetTask":
		task, err := h.a2aGetTask(req.Params)
		if err != nil {
			writeJSONRPC(w, http.StatusOK, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcError(-32001, err.Error(), nil)})
			return
		}
		writeJSONRPC(w, http.StatusOK, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: task})
	case "ListTasks":
		result, err := h.a2aListTasks(req.Params)
		if err != nil {
			writeJSONRPC(w, http.StatusOK, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcError(-32602, err.Error(), nil)})
			return
		}
		writeJSONRPC(w, http.StatusOK, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
	case "CancelTask":
		task, err := h.a2aCancelTask(req.Params)
		if err != nil {
			writeJSONRPC(w, http.StatusOK, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcError(-32001, err.Error(), nil)})
			return
		}
		writeJSONRPC(w, http.StatusOK, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: task})
	case "SubscribeToTask":
		h.a2aSubscribeToTask(w, req)
	default:
		writeJSONRPC(w, http.StatusOK, jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   rpcError(-32601, "method not found: "+req.Method, nil),
		})
	}
}

func (h *handler) chatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req chatRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("messages is required"))
		return
	}
	meta := newRequestMeta(r, req.commonRequest)
	w.Header().Set("X-Request-Id", meta.RequestID)
	text, usage, stop, err := RunProvider(r.Context(), req.Model, req.Messages)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if req.Stream {
		writeSSE(w, []map[string]any{
			{"choices": []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant", "content": text}}}},
			{"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": stopReason(stop)}}},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         responseID("chatcmpl", meta.RequestID),
		"request_id": meta.RequestID,
		"object":     "chat.completion",
		"created":    time.Now().Unix(),
		"model":      firstNonEmpty(req.Model, defaultModel()),
		"choices": []map[string]any{{
			"index":         0,
			"finish_reason": stopReason(stop),
			"message":       map[string]any{"role": "assistant", "content": text},
		}},
		"usage":    openAIUsage(usage),
		"metadata": meta,
	})
}

func (h *handler) responses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req responsesRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	messages, err := h.responsesMessages(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(messages) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("input is required"))
		return
	}
	meta := newRequestMeta(r, req.commonRequest)
	w.Header().Set("X-Request-Id", meta.RequestID)
	text, usage, stop, err := RunProvider(r.Context(), req.Model, messages)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	id := responseID("resp", meta.RequestID)
	resp := map[string]any{
		"id":          id,
		"request_id":  meta.RequestID,
		"object":      "response",
		"created_at":  time.Now().Unix(),
		"model":       firstNonEmpty(req.Model, defaultModel()),
		"status":      "completed",
		"output_text": text,
		"output": []map[string]any{{
			"type":    "message",
			"role":    "assistant",
			"content": []map[string]any{{"type": "output_text", "text": text}},
		}},
		"usage":       openAIUsage(usage),
		"stop_reason": stopReason(stop),
		"metadata": map[string]any{
			"request_id":             meta.RequestID,
			"cache":                  meta.Cache,
			"cache_status":           meta.CacheStatus,
			"previous_response_id":   req.PreviousResponseID,
			"parallel_tool_calls":    req.ParallelToolCalls,
			"max_tool_calls":         req.MaxToolCalls,
			"prompt_cache_key":       req.PromptCacheKey,
			"prompt_cache_retention": req.PromptCacheRetention,
		},
		"parallel_tool_calls":  firstBool(req.ParallelToolCalls, true),
		"previous_response_id": req.PreviousResponseID,
		"tools":                req.Tools,
	}
	if shouldStoreResponse(req) {
		h.storeResponse(responseRecord{ID: id, Model: req.Model, Messages: append(messages, provider.Message{Role: "assistant", Content: text}), Output: text, CreatedAt: time.Now().Unix(), Metadata: req.Metadata})
	}
	if req.Stream {
		writeSSE(w, []map[string]any{
			{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress"}},
			{"type": "response.output_text.delta", "item_id": responseID("item", meta.RequestID), "delta": text},
			{"type": "response.completed", "response": resp},
		})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) responseByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/responses/")
	id = strings.TrimPrefix(id, "/responses/")
	if id == "" {
		writeError(w, http.StatusBadRequest, errors.New("response id is required"))
		return
	}
	h.mu.Lock()
	rec, ok := h.responseStore[id]
	h.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("response not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":          rec.ID,
		"object":      "response",
		"created_at":  rec.CreatedAt,
		"model":       firstNonEmpty(rec.Model, defaultModel()),
		"status":      "completed",
		"output_text": rec.Output,
		"output": []map[string]any{{
			"type":    "message",
			"role":    "assistant",
			"content": []map[string]any{{"type": "output_text", "text": rec.Output}},
		}},
		"metadata": rec.Metadata,
	})
}

func (h *handler) messages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req messagesRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("messages is required"))
		return
	}
	meta := newRequestMeta(r, req.commonRequest)
	w.Header().Set("X-Request-Id", meta.RequestID)
	text, usage, stop, err := RunProvider(r.Context(), req.Model, req.Messages)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if req.Stream {
		writeSSE(w, []map[string]any{
			{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}},
			{"type": "message_stop"},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":          responseID("msg", meta.RequestID),
		"request_id":  meta.RequestID,
		"type":        "message",
		"role":        "assistant",
		"model":       firstNonEmpty(req.Model, defaultModel()),
		"content":     []map[string]any{{"type": "text", "text": text}},
		"stop_reason": stopReason(stop),
		"usage": map[string]any{
			"input_tokens":  usage.InputTokens,
			"output_tokens": usage.OutputTokens,
		},
		"metadata": meta,
	})
}

func newRequestMeta(r *http.Request, req commonRequest) requestMeta {
	id := strings.TrimSpace(r.Header.Get("X-Request-Id"))
	if id == "" {
		if v, ok := req.Metadata["request_id"].(string); ok {
			id = strings.TrimSpace(v)
		}
	}
	if id == "" {
		id = generateID("req")
	}
	return requestMeta{
		RequestID:   id,
		Cache:       cacheHints(req),
		CacheStatus: "bypass",
	}
}

func cacheHints(req commonRequest) map[string]any {
	out := map[string]any{}
	for k, v := range req.Cache {
		out[k] = v
	}
	for k, v := range req.CacheControl {
		out[k] = v
	}
	if nested, ok := req.Metadata["cache"].(map[string]any); ok {
		for k, v := range nested {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (h *handler) a2aSendMessage(ctx context.Context, params json.RawMessage) (*a2aTask, error) {
	req, msg, err := parseA2ASendParams(params)
	if err != nil {
		return nil, err
	}
	task, err := h.prepareA2ATask(msg, req.Metadata)
	if err != nil {
		return nil, err
	}
	taskCtx, cancel := context.WithCancel(ctx)
	h.registerTaskCancel(task.TaskID, cancel)
	defer h.unregisterTaskCancel(task.TaskID)
	defer cancel()

	h.updateA2ATask(task.TaskID, func(t *a2aTask) {
		t.Status = newA2AStatus("TASK_STATE_WORKING", nil)
	})
	text, _, _, err := RunProvider(taskCtx, a2aModel(req), []provider.Message{{Role: "user", Content: a2aMessageText(msg)}})
	if err != nil {
		h.updateA2ATask(task.TaskID, func(t *a2aTask) {
			failed := a2aAgentMessage(t, err.Error())
			t.Status = newA2AStatus("TASK_STATE_FAILED", &failed)
			t.History = append(t.History, failed)
		})
		failedTask, _ := h.getTask(task.TaskID, "")
		return failedTask, err
	}
	h.updateA2ATask(task.TaskID, func(t *a2aTask) {
		answer := a2aAgentMessage(t, text)
		t.History = append(t.History, answer)
		t.Artifacts = []a2aArtifact{{
			ArtifactID: generateID("artifact"),
			Name:       "response",
			Parts:      []a2aPart{{Kind: "text", Type: "text", Text: text}},
		}}
		t.Status = newA2AStatus("TASK_STATE_COMPLETED", &answer)
	})
	return h.getTask(task.TaskID, "")
}

func (h *handler) a2aSendStreamingMessage(w http.ResponseWriter, r *http.Request, rpc jsonRPCRequest) {
	req, msg, err := parseA2ASendParams(rpc.Params)
	if err != nil {
		writeJSONRPC(w, http.StatusOK, jsonRPCResponse{JSONRPC: "2.0", ID: rpc.ID, Error: rpcError(-32602, err.Error(), nil)})
		return
	}
	task, err := h.prepareA2ATask(msg, req.Metadata)
	if err != nil {
		writeJSONRPC(w, http.StatusOK, jsonRPCResponse{JSONRPC: "2.0", ID: rpc.ID, Error: rpcError(-32602, err.Error(), nil)})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flushSSE(w)
	writeA2ASSE(w, map[string]any{"statusUpdate": map[string]any{"taskId": task.TaskID, "contextId": task.ContextID, "status": task.Status}})

	taskCtx, cancel := context.WithCancel(r.Context())
	h.registerTaskCancel(task.TaskID, cancel)
	defer h.unregisterTaskCancel(task.TaskID)
	defer cancel()

	h.updateA2ATask(task.TaskID, func(t *a2aTask) {
		t.Status = newA2AStatus("TASK_STATE_WORKING", nil)
	})
	writeA2ASSE(w, map[string]any{"statusUpdate": map[string]any{"taskId": task.TaskID, "contextId": task.ContextID, "status": newA2AStatus("TASK_STATE_WORKING", nil)}})

	chunks, errs, err := StreamProvider(taskCtx, a2aModel(req), []provider.Message{{Role: "user", Content: a2aMessageText(msg)}})
	if err != nil {
		h.finishA2AStreamWithError(w, task.TaskID, err)
		return
	}
	var b strings.Builder
	for ch := range chunks {
		if ch.Text == "" {
			continue
		}
		b.WriteString(ch.Text)
		writeA2ASSE(w, map[string]any{"artifactUpdate": map[string]any{
			"taskId":    task.TaskID,
			"contextId": task.ContextID,
			"artifact": map[string]any{
				"artifactId": "response",
				"name":       "response",
				"parts":      []a2aPart{{Kind: "text", Type: "text", Text: ch.Text}},
			},
		}})
	}
	if err := <-errs; err != nil {
		h.finishA2AStreamWithError(w, task.TaskID, err)
		return
	}
	text := b.String()
	h.updateA2ATask(task.TaskID, func(t *a2aTask) {
		answer := a2aAgentMessage(t, text)
		t.History = append(t.History, answer)
		t.Artifacts = []a2aArtifact{{ArtifactID: "response", Name: "response", Parts: []a2aPart{{Kind: "text", Type: "text", Text: text}}}}
		t.Status = newA2AStatus("TASK_STATE_COMPLETED", &answer)
	})
	writeA2ASSE(w, map[string]any{"statusUpdate": map[string]any{"taskId": task.TaskID, "contextId": task.ContextID, "status": newA2AStatus("TASK_STATE_COMPLETED", nil)}})
}

func (h *handler) finishA2AStreamWithError(w http.ResponseWriter, taskID string, err error) {
	h.updateA2ATask(taskID, func(t *a2aTask) {
		failed := a2aAgentMessage(t, err.Error())
		t.Status = newA2AStatus("TASK_STATE_FAILED", &failed)
		t.History = append(t.History, failed)
	})
	task, _ := h.getTask(taskID, "")
	writeA2ASSE(w, map[string]any{"statusUpdate": map[string]any{"taskId": taskID, "status": task.Status}})
}

func (h *handler) a2aGetTask(params json.RawMessage) (*a2aTask, error) {
	var q a2aTaskQuery
	if err := json.Unmarshal(params, &q); err != nil {
		return nil, err
	}
	return h.getTask(q.TaskID, q.ContextID)
}

func (h *handler) a2aListTasks(params json.RawMessage) (map[string]any, error) {
	var q a2aTaskQuery
	if len(params) > 0 {
		if err := json.Unmarshal(params, &q); err != nil {
			return nil, err
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	tasks := make([]*a2aTask, 0, len(h.tasks))
	for _, task := range h.tasks {
		if q.ContextID != "" && task.ContextID != q.ContextID {
			continue
		}
		tasks = append(tasks, cloneA2ATask(task))
		if q.PageSize > 0 && len(tasks) >= q.PageSize {
			break
		}
	}
	return map[string]any{"tasks": tasks}, nil
}

func (h *handler) a2aCancelTask(params json.RawMessage) (*a2aTask, error) {
	var q a2aTaskQuery
	if err := json.Unmarshal(params, &q); err != nil {
		return nil, err
	}
	if strings.TrimSpace(q.TaskID) == "" {
		return nil, errors.New("taskId is required")
	}
	h.mu.Lock()
	cancel := h.cancels[q.TaskID]
	task := h.tasks[q.TaskID]
	if task == nil {
		h.mu.Unlock()
		return nil, errors.New("task not found")
	}
	if q.ContextID != "" && q.ContextID != task.ContextID {
		h.mu.Unlock()
		return nil, errors.New("task context mismatch")
	}
	if cancel != nil {
		cancel()
	}
	if task.Status.State == "TASK_STATE_SUBMITTED" || task.Status.State == "TASK_STATE_WORKING" {
		task.Status = newA2AStatus("TASK_STATE_CANCELED", nil)
	}
	out := cloneA2ATask(task)
	h.mu.Unlock()
	return out, nil
}

func (h *handler) a2aSubscribeToTask(w http.ResponseWriter, rpc jsonRPCRequest) {
	task, err := h.a2aGetTask(rpc.Params)
	if err != nil {
		writeJSONRPC(w, http.StatusOK, jsonRPCResponse{JSONRPC: "2.0", ID: rpc.ID, Error: rpcError(-32001, err.Error(), nil)})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	writeA2ASSE(w, map[string]any{"statusUpdate": map[string]any{"taskId": task.TaskID, "contextId": task.ContextID, "status": task.Status}})
}

func (h *handler) prepareA2ATask(msg a2aMessage, metadata map[string]any) (*a2aTask, error) {
	msg.Role = firstNonEmpty(msg.Role, "user")
	if msg.MessageID == "" {
		msg.MessageID = generateID("msg")
	}
	if a2aMessageText(msg) == "" {
		return nil, errors.New("message text part is required")
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if msg.TaskID != "" {
		task := h.tasks[msg.TaskID]
		if task == nil {
			return nil, errors.New("task not found")
		}
		if msg.ContextID != "" && msg.ContextID != task.ContextID {
			return nil, errors.New("task context mismatch")
		}
		msg.ContextID = task.ContextID
		task.History = append(task.History, msg)
		task.Status = newA2AStatus("TASK_STATE_SUBMITTED", nil)
		return cloneA2ATask(task), nil
	}
	if msg.ContextID == "" {
		msg.ContextID = generateID("ctx")
	}
	msg.TaskID = generateID("task")
	task := &a2aTask{
		TaskID:    msg.TaskID,
		ContextID: msg.ContextID,
		Status:    newA2AStatus("TASK_STATE_SUBMITTED", nil),
		History:   []a2aMessage{msg},
		Metadata:  metadata,
	}
	h.tasks[task.TaskID] = task
	return cloneA2ATask(task), nil
}

func (h *handler) getTask(taskID, contextID string) (*a2aTask, error) {
	if strings.TrimSpace(taskID) == "" {
		return nil, errors.New("taskId is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	task := h.tasks[taskID]
	if task == nil {
		return nil, errors.New("task not found")
	}
	if contextID != "" && contextID != task.ContextID {
		return nil, errors.New("task context mismatch")
	}
	return cloneA2ATask(task), nil
}

func (h *handler) updateA2ATask(taskID string, fn func(*a2aTask)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if task := h.tasks[taskID]; task != nil {
		fn(task)
	}
}

func (h *handler) registerTaskCancel(taskID string, cancel context.CancelFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cancels[taskID] = cancel
}

func (h *handler) unregisterTaskCancel(taskID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.cancels, taskID)
}

func RunProvider(ctx context.Context, model string, messages []provider.Message) (string, provider.Usage, string, error) {
	chunks, errs, err := StreamProvider(ctx, model, messages)
	if err != nil {
		return "", provider.Usage{}, "", err
	}
	var b strings.Builder
	var usage provider.Usage
	var stop string
	for ch := range chunks {
		b.WriteString(ch.Text)
		if ch.Usage != nil {
			usage = *ch.Usage
		}
		if ch.StopReason != "" {
			stop = ch.StopReason
		}
	}
	if err := <-errs; err != nil {
		return "", usage, stop, err
	}
	return b.String(), usage, stop, nil
}

func StreamProvider(ctx context.Context, model string, messages []provider.Message) (<-chan provider.Chunk, <-chan error, error) {
	p, err := buildProvider()
	if err != nil {
		return nil, nil, err
	}
	chunks, errs := p.StreamChat(ctx, messages, provider.StreamOptions{Model: model, Tools: nil})
	return chunks, errs, nil
}

func buildProvider() (provider.Provider, error) {
	m, err := config.Load()
	if err != nil {
		return nil, err
	}
	cfg, err := m.Resolve("")
	if err != nil {
		return nil, err
	}
	if cfg.APIKey == "" && (cfg.Kind == "glm" || cfg.Kind == "" || cfg.Kind == "openai-chat") {
		cfg.APIKey = credentials.Resolve()
	}
	if resolved, accountID, err := resolveOAuthKey(cfg.APIKey); err != nil {
		return nil, err
	} else {
		cfg.APIKey = resolved
		if accountID != "" {
			if cfg.Headers == nil {
				cfg.Headers = map[string]string{}
			}
			if cfg.Headers["ChatGPT-Account-ID"] == "" {
				cfg.Headers["ChatGPT-Account-ID"] = accountID
			}
		}
	}
	if cfg.APIKey == "" && cfg.Kind != "ollama" && cfg.Kind != "claude-code-cli" {
		return nil, errors.New("no API key configured")
	}
	return provider.Build(cfg)
}

func resolveOAuthKey(key string) (string, string, error) {
	if !strings.HasPrefix(key, "oauth:") {
		return key, "", nil
	}
	flavour := strings.TrimPrefix(key, "oauth:")
	switch flavour {
	case "codex", "openai":
		tok, err := codexoauth.NewForFlavour(flavour, "").ResolveToken(context.Background())
		if err != nil {
			return "", "", err
		}
		return tok.AccessToken, tok.AccountID, nil
	case "xai", "xai-oauth", "grok-oauth":
		tok, err := xaioauth.New("").ResolveToken(context.Background())
		if err != nil {
			return "", "", err
		}
		return tok.AccessToken, "", nil
	default:
		return "", "", fmt.Errorf("oauth resolver for %q is not registered", flavour)
	}
}

func defaultModel() string {
	p, err := buildProvider()
	if err != nil {
		return ""
	}
	return p.DefaultModel()
}

func responsesInput(input any) []provider.Message {
	switch v := input.(type) {
	case string:
		return []provider.Message{{Role: "user", Content: v}}
	case []any:
		out := make([]provider.Message, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				role, _ := m["role"].(string)
				content := responseContentText(m["content"])
				if content == "" {
					content = responseContentText(m["text"])
				}
				if role == "" {
					role = "user"
				}
				if content != "" {
					out = append(out, provider.Message{Role: role, Content: content})
				}
			}
		}
		return out
	default:
		return nil
	}
}

func responseContentText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				text, _ := m["text"].(string)
				if text == "" {
					text, _ = m["input_text"].(string)
				}
				if text == "" {
					continue
				}
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(text)
			}
		}
		return b.String()
	default:
		return ""
	}
}

func decodeBody(r *http.Request, dst any) error {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 8*1024*1024))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return errors.New("empty request body")
	}
	return json.Unmarshal(body, dst)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": err.Error()}})
}

func writeJSONRPC(w http.ResponseWriter, status int, resp jsonRPCResponse) {
	if resp.JSONRPC == "" {
		resp.JSONRPC = "2.0"
	}
	writeJSON(w, status, resp)
}

func rpcError(code int, message string, data any) *jsonRPCError {
	return &jsonRPCError{Code: code, Message: message, Data: data}
}

func writeSSE(w http.ResponseWriter, events []map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	for _, ev := range events {
		_, _ = io.WriteString(w, "data: ")
		_ = enc.Encode(ev)
		_, _ = io.WriteString(w, "\n")
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func writeA2ASSE(w http.ResponseWriter, event any) {
	b, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = io.WriteString(w, "data: ")
	_, _ = w.Write(b)
	_, _ = io.WriteString(w, "\n\n")
	flushSSE(w)
}

func flushSSE(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func normalizeA2AMethod(method string) string {
	switch method {
	case "message/send":
		return "SendMessage"
	case "message/stream":
		return "SendStreamingMessage"
	case "tasks/get":
		return "GetTask"
	case "tasks/list":
		return "ListTasks"
	case "tasks/cancel":
		return "CancelTask"
	case "tasks/resubscribe":
		return "SubscribeToTask"
	default:
		return method
	}
}

func parseA2ASendParams(raw json.RawMessage) (a2aSendParams, a2aMessage, error) {
	var req a2aSendParams
	if len(raw) == 0 {
		return req, a2aMessage{}, errors.New("params is required")
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return req, a2aMessage{}, err
	}
	if len(req.Message.Parts) > 0 || req.Message.Role != "" {
		return req, req.Message, nil
	}
	var msg a2aMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return req, a2aMessage{}, err
	}
	if len(msg.Parts) == 0 && msg.Role == "" {
		return req, a2aMessage{}, errors.New("message is required")
	}
	req.Message = msg
	return req, msg, nil
}

func a2aModel(req a2aSendParams) string {
	if req.Model != "" {
		return req.Model
	}
	if v, ok := req.Metadata["model"].(string); ok {
		return v
	}
	if v, ok := req.Configuration["model"].(string); ok {
		return v
	}
	return ""
}

func a2aMessageText(msg a2aMessage) string {
	var b strings.Builder
	for _, part := range msg.Parts {
		if part.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(part.Text)
	}
	return b.String()
}

func a2aAgentMessage(task *a2aTask, text string) a2aMessage {
	return a2aMessage{
		Role:      "agent",
		MessageID: generateID("msg"),
		ContextID: task.ContextID,
		TaskID:    task.TaskID,
		Parts:     []a2aPart{{Kind: "text", Type: "text", Text: text}},
	}
}

func newA2AStatus(state string, msg *a2aMessage) a2aTaskStatus {
	return a2aTaskStatus{
		State:     state,
		Message:   msg,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func cloneA2ATask(task *a2aTask) *a2aTask {
	if task == nil {
		return nil
	}
	out := *task
	out.History = append([]a2aMessage(nil), task.History...)
	out.Artifacts = append([]a2aArtifact(nil), task.Artifacts...)
	if task.Metadata != nil {
		out.Metadata = map[string]any{}
		for k, v := range task.Metadata {
			out.Metadata[k] = v
		}
	}
	return &out
}

func openAIUsage(u provider.Usage) map[string]any {
	return map[string]any{
		"prompt_tokens":     u.InputTokens,
		"completion_tokens": u.OutputTokens,
		"total_tokens":      u.TotalTokens,
	}
}

func stopReason(stop string) string {
	if stop == "" || stop == "end_turn" {
		return "stop"
	}
	return stop
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func responseID(prefix, requestID string) string {
	if requestID != "" {
		return prefix + "-" + requestID
	}
	return generateID(prefix)
}

func generateID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "-" + time.Now().UTC().Format("20060102150405.000000000")
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}
