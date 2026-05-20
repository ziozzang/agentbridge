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
	"time"

	"github.com/ziozzang/glm-acp/internal/config"
	"github.com/ziozzang/glm-acp/internal/credentials"
	codexoauth "github.com/ziozzang/glm-acp/internal/oauth/codex"
	"github.com/ziozzang/glm-acp/internal/provider"
	_ "github.com/ziozzang/glm-acp/internal/provider/anthropic"
	_ "github.com/ziozzang/glm-acp/internal/provider/glmprov"
	_ "github.com/ziozzang/glm-acp/internal/provider/ollama"
	_ "github.com/ziozzang/glm-acp/internal/provider/openaichat"
	_ "github.com/ziozzang/glm-acp/internal/provider/openairesp"
)

// NewHandler returns an HTTP handler for OpenAI/Anthropic-style compatibility
// endpoints backed by the configured harness provider.
func NewHandler() http.Handler {
	mux := http.NewServeMux()
	h := &handler{}
	mux.HandleFunc("/health", h.health)
	mux.HandleFunc("/v1/chat/completions", h.chatCompletions)
	mux.HandleFunc("/chat/completions", h.chatCompletions)
	mux.HandleFunc("/v1/responses", h.responses)
	mux.HandleFunc("/responses", h.responses)
	mux.HandleFunc("/v1/messages", h.messages)
	mux.HandleFunc("/messages", h.messages)
	return mux
}

type handler struct{}

type requestMeta struct {
	RequestID   string         `json:"request_id,omitempty"`
	Cache       map[string]any `json:"cache,omitempty"`
	CacheStatus string         `json:"cache_status,omitempty"`
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
	Model  string `json:"model"`
	Input  any    `json:"input"`
	Stream bool   `json:"stream"`
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
	text, usage, stop, err := runProvider(r.Context(), req.Model, req.Messages)
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
	messages := responsesInput(req.Input)
	if len(messages) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("input is required"))
		return
	}
	meta := newRequestMeta(r, req.commonRequest)
	w.Header().Set("X-Request-Id", meta.RequestID)
	text, usage, stop, err := runProvider(r.Context(), req.Model, messages)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if req.Stream {
		writeSSE(w, []map[string]any{
			{"type": "response.output_text.delta", "delta": text},
			{"type": "response.completed", "response": map[string]any{"status": "completed"}},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":          responseID("resp", meta.RequestID),
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
		"metadata":    meta,
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
	text, usage, stop, err := runProvider(r.Context(), req.Model, req.Messages)
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

func runProvider(ctx context.Context, model string, messages []provider.Message) (string, provider.Usage, string, error) {
	p, err := buildProvider()
	if err != nil {
		return "", provider.Usage{}, "", err
	}
	chunks, errs := p.StreamChat(ctx, messages, provider.StreamOptions{Model: model, Tools: nil})
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
	if resolved, err := resolveOAuthKey(cfg.APIKey); err != nil {
		return nil, err
	} else {
		cfg.APIKey = resolved
	}
	if cfg.APIKey == "" && cfg.Kind != "ollama" {
		return nil, errors.New("no API key configured")
	}
	return provider.Build(cfg)
}

func resolveOAuthKey(key string) (string, error) {
	if !strings.HasPrefix(key, "oauth:") {
		return key, nil
	}
	flavour := strings.TrimPrefix(key, "oauth:")
	switch flavour {
	case "codex", "openai":
		return codexoauth.NewForFlavour(flavour, "").Resolve(context.Background())
	default:
		return "", fmt.Errorf("oauth resolver for %q is not registered", flavour)
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
				content := m["content"]
				if role == "" {
					role = "user"
				}
				out = append(out, provider.Message{Role: role, Content: content})
			}
		}
		return out
	default:
		return nil
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
