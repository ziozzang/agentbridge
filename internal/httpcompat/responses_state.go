package httpcompat

import (
	"errors"
	"strings"

	"github.com/ziozzang/agentbridge/internal/provider"
)

func (h *handler) responsesMessages(req responsesRequest) ([]provider.Message, error) {
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
	messages = append(messages, responsesInput(req.Input)...)
	return messages, nil
}

func (h *handler) storeResponse(rec responseRecord) {
	if rec.ID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.responseStore[rec.ID] = rec
}

func shouldStoreResponse(req responsesRequest) bool {
	if req.Store == nil {
		return true
	}
	return *req.Store
}

func firstBool(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}
