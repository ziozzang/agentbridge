package httpcompat

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/ziozzang/agentbridge/internal/provider"
)

type intentionRequest struct {
	Model       string             `json:"model,omitempty"`
	Prompt      string             `json:"prompt,omitempty"`
	Messages    []provider.Message `json:"messages,omitempty"`
	Choices     json.RawMessage    `json:"choices"`
	MaxTokens   int                `json:"max_tokens,omitempty"`
	TopLogprobs int                `json:"top_logprobs,omitempty"`
}

func (h *handler) experimentalIntention(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !intentionExperimentEnabled() {
		writeError(w, http.StatusNotFound, errors.New("experimental intention probe is disabled; set AGENTBRIDGE_EXPERIMENTAL_INTENTION_PROBE=1"))
		return
	}
	var req intentionRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	choices, err := decodeIntentionChoices(req.Choices)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	p, err := buildProvider()
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	prober, ok := p.(provider.IntentionProber)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("active provider does not support experimental intention probe"))
		return
	}
	result, err := prober.ProbeIntention(r.Context(), provider.IntentionProbeRequest{
		Model:       req.Model,
		Prompt:      req.Prompt,
		Messages:    req.Messages,
		Choices:     choices,
		MaxTokens:   req.MaxTokens,
		TopLogprobs: req.TopLogprobs,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object":       "experimental.intention",
		"experimental": true,
		"model":        result.Model,
		"provider":     result.Provider,
		"answer":       result.Answer,
		"index":        result.Index,
		"confidence":   result.Confidence,
		"logprobs":     result.Logprobs,
		"text":         result.Text,
		"tokens":       result.Tokens,
	})
}

func intentionExperimentEnabled() bool {
	if envTruthy(os.Getenv("AGENTBRIDGE_EXPERIMENTAL_INTENTION_PROBE")) {
		return true
	}
	for _, part := range strings.Split(os.Getenv("AGENTBRIDGE_EXPERIMENTS"), ",") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "intention", "intention_probe", "logprob_intention":
			return true
		}
	}
	return false
}

func decodeIntentionChoices(raw json.RawMessage) ([]provider.IntentionChoice, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, errors.New("choices is required")
	}
	var choices []provider.IntentionChoice
	if err := json.Unmarshal(raw, &choices); err == nil {
		return choices, nil
	}
	var stringsOnly []string
	if err := json.Unmarshal(raw, &stringsOnly); err == nil {
		out := make([]provider.IntentionChoice, 0, len(stringsOnly))
		for i, text := range stringsOnly {
			label := ""
			if i < 26 {
				label = string(rune('A' + i))
			}
			out = append(out, provider.IntentionChoice{Label: label, Text: text})
		}
		return out, nil
	}
	return nil, errors.New("choices must be an array of strings or {label,text} objects")
}

func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "enable", "enabled":
		return true
	default:
		return false
	}
}
