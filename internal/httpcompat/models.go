package httpcompat

import (
	"net/http"

	"github.com/ziozzang/agentbridge/internal/agentprofiles"
	"github.com/ziozzang/agentbridge/internal/provider"
	"github.com/ziozzang/agentbridge/internal/provider/glm"
)

func (h *handler) models(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	models, _ := h.availableHTTPModels()
	data := make([]map[string]any, 0, len(models))
	created := modelCreatedAt()
	for _, m := range models {
		owner := m.Provider
		if owner == "" {
			owner = "agentbridge"
		}
		data = append(data, map[string]any{
			"id":          m.ModelID,
			"object":      "model",
			"created":     created,
			"owned_by":    owner,
			"description": m.Description,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (h *handler) availableHTTPModels() ([]provider.ModelInfo, error) {
	var out []provider.ModelInfo
	if p, err := buildProvider(); err == nil {
		out = append(out, p.AvailableModels()...)
		if len(out) == 0 && p.DefaultModel() != "" {
			out = append(out, provider.ModelInfo{ModelID: p.DefaultModel(), Name: p.DefaultModel()})
		}
	} else {
		for _, m := range glm.AvailableModels() {
			out = append(out, provider.ModelInfo{ModelID: m.ModelID, Name: m.Name, Description: m.Description})
		}
	}
	profiles, err := agentprofiles.Load()
	if err != nil {
		return out, err
	}
	for _, p := range profiles {
		desc := p.Description
		if desc == "" {
			desc = "Agent profile"
		}
		out = append(out, provider.ModelInfo{ModelID: p.Name, Name: p.Name, Description: desc})
	}
	out = append(out, embeddingHTTPModels(h.plugins)...)
	out = append(out, rerankHTTPModels(h.plugins)...)
	return dedupeModels(out), nil
}
