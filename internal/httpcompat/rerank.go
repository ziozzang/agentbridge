package httpcompat

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"

	"github.com/ziozzang/agentbridge/internal/plugins"
	"github.com/ziozzang/agentbridge/internal/provider"
)

const defaultJinaRerankModel = "jina-reranker-v3"

type rerankRequest struct {
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	TopN            int      `json:"top_n,omitempty"`
	ReturnDocuments *bool    `json:"return_documents,omitempty"`
}

func (h *handler) rerank(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pluginActive(h.plugins, "jina") {
		writeError(w, http.StatusBadRequest, errors.New("no rerank provider is active; enable jina in AGENTBRIDGE_PLUGINS"))
		return
	}
	var req rerankRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, errors.New("query is required"))
		return
	}
	if len(req.Documents) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("documents is required"))
		return
	}
	if req.Model == "" {
		req.Model = defaultRerankModel()
	}
	body, err := json.Marshal(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	raw, handled, err := h.plugins.Dispatch(r.Context(), plugins.ToolName("jina", "jina_rerank"), body)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if !handled {
		writeError(w, http.StatusBadRequest, errors.New("jina rerank tool is not active"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(raw))
}

func rerankHTTPModels(active *plugins.Active) []provider.ModelInfo {
	if !pluginActive(active, "jina") {
		return nil
	}
	model := defaultRerankModel()
	return []provider.ModelInfo{{
		ModelID:     model,
		Name:        model,
		Description: "Jina reranker model via AgentBridge",
		Provider:    "jina",
	}}
}

func defaultRerankModel() string {
	return firstNonEmpty(os.Getenv("AGENTBRIDGE_JINA_RERANK_MODEL"), defaultJinaRerankModel)
}
