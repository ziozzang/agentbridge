package httpcompat

import (
	"errors"
	"net/http"
	"strings"
	"time"

	contextcompact "github.com/ziozzang/agentbridge/internal/compaction"
	"github.com/ziozzang/agentbridge/internal/provider"
	"github.com/ziozzang/agentbridge/internal/tools/definitions"
)

type compactRequest struct {
	commonRequest
	Model                string             `json:"model,omitempty"`
	Input                any                `json:"input,omitempty"`
	Messages             []provider.Message `json:"messages,omitempty"`
	Instructions         string             `json:"instructions,omitempty"`
	PreviousResponseID   string             `json:"previous_response_id,omitempty"`
	TargetTokens         int                `json:"target_tokens,omitempty"`
	Reason               string             `json:"reason,omitempty"`
	Strategy             string             `json:"strategy,omitempty"`
	SessionID            string             `json:"session_id,omitempty"`
	PromptCacheKey       string             `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string             `json:"prompt_cache_retention,omitempty"`
	ServiceTier          string             `json:"service_tier,omitempty"`
	Reasoning            compactReasoning   `json:"reasoning,omitempty"`
	Settings             compactSettings    `json:"settings,omitempty"`
}

type compactReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type compactSettings struct {
	Enabled          *bool `json:"enabled,omitempty"`
	Native           *bool `json:"native,omitempty"`
	Summary          *bool `json:"summary,omitempty"`
	PruneFallback    *bool `json:"prune_fallback,omitempty"`
	PreserveTurns    int   `json:"preserve_turns,omitempty"`
	KeepRecentTokens int   `json:"keep_recent_tokens,omitempty"`
}

func (h *handler) responsesCompact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req compactRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	messages, err := h.compactMessages(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(messages) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("input or messages is required"))
		return
	}
	p, err := buildProvider()
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	model := firstNonEmpty(req.Model, p.DefaultModel(), defaultModel())
	settings := compactSettingsForRequest(req)
	targetTokens := req.TargetTokens
	if targetTokens <= 0 {
		targetTokens = settings.TargetTokens(p.ContextWindow(model))
	}
	meta := newRequestMeta(r, req.commonRequest)
	w.Header().Set("X-Request-Id", meta.RequestID)
	opts := provider.CompactOptions{
		Model:                model,
		TargetTokens:         targetTokens,
		Reason:               req.Reason,
		SessionID:            firstNonEmpty(req.SessionID, meta.RequestID),
		PromptCacheKey:       req.PromptCacheKey,
		PromptCacheRetention: req.PromptCacheRetention,
		ServiceTier:          req.ServiceTier,
		ReasoningEffort:      req.Reasoning.Effort,
		ReasoningSummary:     req.Reasoning.Summary,
	}
	result := compactHTTPMessagesWithOptions(r.Context(), p, messages, model, h.compactionTools(), settings, opts)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":                     responseID("compact", meta.RequestID),
		"request_id":             meta.RequestID,
		"object":                 "conversation.compaction",
		"created_at":             time.Now().Unix(),
		"model":                  model,
		"status":                 "completed",
		"strategy":               result.Strategy,
		"compacted":              result.Compacted,
		"target_tokens":          targetTokens,
		"tokens_before_estimate": result.TokensBefore,
		"tokens_after_estimate":  result.TokensAfter,
		"messages":               result.Messages,
		"output":                 result.Messages,
		"metadata": map[string]any{
			"provider":               p.Name(),
			"reason":                 req.Reason,
			"previous_response_id":   req.PreviousResponseID,
			"prompt_cache_key":       req.PromptCacheKey,
			"prompt_cache_retention": req.PromptCacheRetention,
			"cache":                  meta.Cache,
			"cache_status":           meta.CacheStatus,
		},
	})
}

func (h *handler) compactMessages(req compactRequest) ([]provider.Message, error) {
	var messages []provider.Message
	if req.PreviousResponseID != "" {
		h.mu.Lock()
		prev, ok := h.responseStore[req.PreviousResponseID]
		h.mu.Unlock()
		if !ok {
			return nil, errors.New("previous_response_id not found")
		}
		messages = append(messages, prev.Messages...)
	}
	if s := strings.TrimSpace(req.Instructions); s != "" {
		messages = append(messages, provider.Message{Role: "system", Content: s})
	}
	messages = append(messages, req.Messages...)
	messages = append(messages, responsesInput(req.Input)...)
	return messages, nil
}

func compactSettingsForRequest(req compactRequest) contextcompact.Settings {
	settings := loadHTTPCompactionSettings()
	settings.Enabled = true
	if req.Settings.Enabled != nil {
		settings.Enabled = *req.Settings.Enabled
	}
	if req.Settings.Native != nil {
		settings.NativeEnabled = *req.Settings.Native
	}
	if req.Settings.Summary != nil {
		settings.SummaryEnabled = *req.Settings.Summary
	}
	if req.Settings.PruneFallback != nil {
		settings.PruneFallbackEnabled = *req.Settings.PruneFallback
	}
	if req.Settings.PreserveTurns > 0 {
		settings.PreserveTurns = req.Settings.PreserveTurns
	}
	if req.Settings.KeepRecentTokens > 0 {
		settings.KeepRecentTokens = req.Settings.KeepRecentTokens
	}
	switch strings.ToLower(strings.TrimSpace(req.Strategy)) {
	case "native":
		settings.NativeEnabled = true
		settings.SummaryEnabled = false
		settings.PruneFallbackEnabled = false
	case "summary":
		settings.NativeEnabled = false
		settings.SummaryEnabled = true
		settings.PruneFallbackEnabled = false
	case "prune":
		settings.NativeEnabled = false
		settings.SummaryEnabled = false
		settings.PruneFallbackEnabled = true
	case "off", "none", "disabled":
		settings.NativeEnabled = false
		settings.SummaryEnabled = false
		settings.PruneFallbackEnabled = false
	}
	if !settings.Enabled {
		settings.NativeEnabled = false
		settings.SummaryEnabled = false
		settings.PruneFallbackEnabled = false
	}
	return settings
}

func (h *handler) compactionTools() []definitions.Tool {
	tools := definitions.All()
	if h == nil {
		return tools
	}
	if h.plugins != nil {
		tools = append(tools, h.plugins.Tools()...)
	}
	if h.externalMCP != nil {
		tools = append(tools, h.externalMCP.ToolDefinitions()...)
	}
	return tools
}
