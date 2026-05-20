package httpcompat

import (
	"net/http"
	"time"

	"github.com/ziozzang/agentbridge/internal/agentprofiles"
	"github.com/ziozzang/agentbridge/internal/provider"
	"github.com/ziozzang/agentbridge/internal/provider/glm"
)

func (h *handler) models(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	models, _ := availableHTTPModels()
	data := make([]map[string]any, 0, len(models))
	created := time.Now().Unix()
	for _, m := range models {
		data = append(data, map[string]any{
			"id":          m.ModelID,
			"object":      "model",
			"created":     created,
			"owned_by":    "agentbridge",
			"description": m.Description,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func availableHTTPModels() ([]provider.ModelInfo, error) {
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
	return out, nil
}
