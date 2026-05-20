package httpcompat

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/ziozzang/agentbridge/internal/provider"
)

func (h *handler) aguiRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req map[string]any
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	model, messages := genericMessages(req)
	if len(messages) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("input is required"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	runID := generateID("run")
	msgID := generateID("msg")
	writeAGUIEvent(w, map[string]any{"type": "RUN_STARTED", "runId": runID})
	writeAGUIEvent(w, map[string]any{"type": "TEXT_MESSAGE_START", "messageId": msgID, "role": "assistant"})

	chunks, errs, err := StreamProvider(r.Context(), model, messages)
	if err != nil {
		writeAGUIEvent(w, map[string]any{"type": "RUN_ERROR", "runId": runID, "message": err.Error()})
		return
	}
	var b strings.Builder
	for ch := range chunks {
		if ch.Text == "" {
			continue
		}
		b.WriteString(ch.Text)
		writeAGUIEvent(w, map[string]any{"type": "TEXT_MESSAGE_CONTENT", "messageId": msgID, "delta": ch.Text})
	}
	if err := <-errs; err != nil {
		writeAGUIEvent(w, map[string]any{"type": "RUN_ERROR", "runId": runID, "message": err.Error()})
		return
	}
	writeAGUIEvent(w, map[string]any{"type": "TEXT_MESSAGE_END", "messageId": msgID})
	writeAGUIEvent(w, map[string]any{"type": "RUN_FINISHED", "runId": runID, "messages": []provider.Message{{Role: "assistant", Content: b.String()}}})
}

func writeAGUIEvent(w http.ResponseWriter, event map[string]any) {
	b, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = io.WriteString(w, "data: ")
	_, _ = w.Write(b)
	_, _ = io.WriteString(w, "\n\n")
	flushSSE(w)
}
