package httpcompat

import (
	"net/http"
	"strings"

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
			"name":        firstNonEmpty(m.Name, m.ModelID),
			"metadata":    modelMetadata(m),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func modelMetadata(m provider.ModelInfo) map[string]any {
	meta := map[string]any{}
	set := func(key string, value any) {
		switch v := value.(type) {
		case string:
			if v != "" {
				meta[key] = v
			}
		case int:
			if v != 0 {
				meta[key] = v
			}
		case []string:
			if len(v) > 0 {
				meta[key] = v
			}
		case map[string]any:
			if len(v) > 0 {
				meta[key] = v
			}
		case *bool:
			if v != nil {
				meta[key] = *v
			}
		}
	}
	set("api", m.API)
	set("base_url", m.BaseURL)
	set("input", m.Input)
	set("reasoning", m.Reasoning)
	set("context_window", m.ContextWindow)
	set("context_tokens", m.ContextTokens)
	set("max_tokens", m.MaxTokens)
	set("status", m.Status)
	set("status_reason", m.StatusReason)
	set("replaces", m.Replaces)
	set("replaced_by", m.ReplacedBy)
	set("aliases", m.Aliases)
	set("tags", m.Tags)
	set("compat", m.Compat)
	set("cost", m.Cost)
	kind := inferModelKind(m)
	if kind != "" {
		meta["kind"] = kind
		meta["capabilities"] = inferModelCapabilities(kind)
		meta["modalities"] = inferModelModalities(kind, m.Input)
	}
	return meta
}

func inferModelKind(m provider.ModelInfo) string {
	haystack := strings.ToLower(strings.Join(append([]string{m.ModelID, m.Name, m.Description, m.Provider}, m.Tags...), " "))
	if strings.Contains(haystack, "rerank") || strings.Contains(haystack, "reranker") {
		return "reranker"
	}
	if strings.Contains(haystack, "embedding") || strings.Contains(haystack, "embed") || m.Provider == "openai_embed" {
		return "embedding"
	}
	if strings.Contains(haystack, "agent profile") || strings.Contains(m.ModelID, "agent") {
		return "agent"
	}
	if mode, ok := m.Compat["agent_loop"].(string); ok && mode == "provider_native" {
		return "agent"
	}
	return "llm"
}

func inferModelCapabilities(kind string) []string {
	switch kind {
	case "embedding":
		return []string{"embeddings"}
	case "reranker":
		return []string{"rerank"}
	case "agent":
		return []string{"chat", "agent"}
	default:
		return []string{"chat"}
	}
}

func inferModelModalities(kind string, input []string) map[string]any {
	if len(input) == 0 {
		input = []string{"text"}
	}
	switch kind {
	case "embedding":
		return map[string]any{"input": input, "output": []string{"embedding"}}
	case "reranker":
		return map[string]any{"input": input, "output": []string{"ranking"}}
	default:
		return map[string]any{"input": input, "output": []string{"text"}}
	}
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
