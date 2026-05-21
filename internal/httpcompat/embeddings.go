package httpcompat

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ziozzang/agentbridge/internal/config"
	"github.com/ziozzang/agentbridge/internal/plugins"
	"github.com/ziozzang/agentbridge/internal/provider"
)

const (
	defaultJinaEmbeddingModel   = "jina-embeddings-v3"
	defaultOpenAIEmbeddingModel = "text-embedding-3-small"
)

type embeddingsRequest struct {
	Model          string          `json:"model"`
	Input          json.RawMessage `json:"input"`
	EncodingFormat string          `json:"encoding_format,omitempty"`
	Dimensions     int             `json:"dimensions,omitempty"`
}

func (h *handler) embeddings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req, err := decodeEmbeddingsRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Input) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("input is required"))
		return
	}
	pluginName, toolName, args, err := h.embeddingToolRequest(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	raw, handled, err := h.plugins.Dispatch(r.Context(), plugins.ToolName(pluginName, toolName), args)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if !handled {
		writeError(w, http.StatusBadRequest, errors.New("no embeddings provider is active; enable jina or openai_embed in AGENTBRIDGE_PLUGINS"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(raw))
}

func decodeEmbeddingsRequest(r *http.Request) (embeddingsRequest, error) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 8*1024*1024))
	if err != nil {
		return embeddingsRequest{}, err
	}
	if len(body) == 0 {
		return embeddingsRequest{}, errors.New("empty request body")
	}
	var req embeddingsRequest
	if err := json.Unmarshal(body, &req); err == nil {
		return req, nil
	}
	var one string
	if err := json.Unmarshal(body, &one); err == nil {
		input, _ := json.Marshal(one)
		return embeddingsRequest{Input: input}, nil
	}
	var many []string
	if err := json.Unmarshal(body, &many); err == nil {
		input, _ := json.Marshal(many)
		return embeddingsRequest{Input: input}, nil
	}
	return embeddingsRequest{}, errors.New("request body must be an object with input, a string, or an array of strings")
}

func (h *handler) embeddingToolRequest(req embeddingsRequest) (string, string, json.RawMessage, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = defaultEmbeddingModel(h.plugins)
	}
	args := map[string]any{}
	if err := addEmbeddingInput(args, req.Input); err != nil {
		return "", "", nil, err
	}
	if model != "" {
		args["model"] = model
	}
	if req.EncodingFormat != "" {
		args["encoding_format"] = req.EncodingFormat
	}
	if req.Dimensions > 0 {
		args["dimensions"] = req.Dimensions
	}
	body, err := json.Marshal(args)
	if err != nil {
		return "", "", nil, err
	}
	if pluginActive(h.plugins, "jina") && shouldUseJinaEmbedding(model, h.plugins) {
		return "jina", "jina_embed", body, nil
	}
	if pluginActive(h.plugins, "openai_embed") {
		return "openai_embed", "embed", body, nil
	}
	if pluginActive(h.plugins, "jina") {
		return "jina", "jina_embed", body, nil
	}
	return "", "", nil, errors.New("no embeddings provider is active; enable jina or openai_embed in AGENTBRIDGE_PLUGINS")
}

func addEmbeddingInput(args map[string]any, raw json.RawMessage) error {
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		args["input"] = one
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		args["inputs"] = many
		return nil
	}
	return errors.New("input must be a string or an array of strings")
}

func embeddingHTTPModels(active *plugins.Active) []provider.ModelInfo {
	var out []provider.ModelInfo
	if pluginActive(active, "jina") {
		for _, model := range jinaEmbeddingModelsFromEnv() {
			out = append(out, provider.ModelInfo{
				ModelID:     model,
				Name:        model,
				Description: "Jina embedding model via AgentBridge",
				Provider:    "jina",
				API:         "embeddings",
				Input:       []string{"text"},
				Tags:        []string{"embedding"},
				Compat:      map[string]any{"endpoint": "/v1/embeddings"},
			})
		}
	}
	if pluginActive(active, "openai_embed") {
		out = append(out, openAIEmbeddingModelInfosFromEnv()...)
	}
	return out
}

func jinaEmbeddingModelsFromEnv() []string {
	return csvValues(firstNonEmpty(os.Getenv("AGENTBRIDGE_JINA_EMBEDDINGS_MODELS"), os.Getenv("AGENTBRIDGE_JINA_EMBEDDINGS_MODEL"), defaultJinaEmbeddingModel))
}

func openAIEmbeddingModelsFromEnv() []string {
	infos := openAIEmbeddingModelInfosFromEnv()
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.ModelID)
	}
	return out
}

func openAIEmbeddingModelInfosFromEnv() []provider.ModelInfo {
	seen := map[string]bool{}
	var out []provider.ModelInfo
	add := func(model, owner, desc string) {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			return
		}
		owner = strings.TrimSpace(owner)
		if owner == "" {
			owner = "openai_embed"
		}
		desc = strings.TrimSpace(desc)
		if desc == "" {
			desc = "OpenAI-compatible embedding model via AgentBridge"
		}
		seen[model] = true
		out = append(out, provider.ModelInfo{
			ModelID:     model,
			Name:        model,
			Description: desc,
			Provider:    owner,
			API:         "embeddings",
			Input:       []string{"text"},
			Tags:        []string{"embedding"},
			Compat:      map[string]any{"endpoint": "/v1/embeddings"},
		})
	}
	if mf, ok := configuredEmbeddingMap(); ok {
		for alias, route := range mf.Models {
			add(alias, firstNonEmpty(os.ExpandEnv(route.OwnedBy), os.ExpandEnv(route.Provider)), os.ExpandEnv(route.Description))
		}
		if len(out) == 0 {
			add(mf.Default, "", "")
		}
	}
	if len(out) == 0 {
		add(firstNonEmpty(os.Getenv("AGENTBRIDGE_EMBEDDINGS_MODEL"), os.Getenv("LITELLM_EMBEDDINGS_MODEL"), os.Getenv("OPENAI_EMBEDDINGS_MODEL"), defaultOpenAIEmbeddingModel), "openai_embed", "")
	}
	return out
}

type embeddingMapFile struct {
	Default string `json:"default"`
	Models  map[string]struct {
		Model       string `json:"model"`
		Provider    string `json:"provider"`
		OwnedBy     string `json:"owned_by"`
		Description string `json:"description"`
	} `json:"models"`
}

func configuredEmbeddingMap() (embeddingMapFile, bool) {
	return routerEmbeddingMap()
}

func routerEmbeddingMap() (embeddingMapFile, bool) {
	manifest, err := config.Load()
	if err != nil {
		return embeddingMapFile{}, false
	}
	router, err := manifest.Resolve("router")
	if err != nil || router.Extra == nil {
		return embeddingMapFile{}, false
	}
	raw, ok := router.Extra["embeddings"]
	if !ok {
		raw, ok = router.Extra["embedding_routes"]
	}
	if !ok || raw == nil {
		return embeddingMapFile{}, false
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return embeddingMapFile{}, false
	}
	var mf embeddingMapFile
	if json.Unmarshal(data, &mf) != nil {
		return embeddingMapFile{}, false
	}
	return mf, len(mf.Models) > 0 || strings.TrimSpace(mf.Default) != ""
}

func defaultEmbeddingModel(active *plugins.Active) string {
	if pluginActive(active, "jina") {
		if models := jinaEmbeddingModelsFromEnv(); len(models) > 0 {
			return models[0]
		}
		return defaultJinaEmbeddingModel
	}
	if models := openAIEmbeddingModelsFromEnv(); len(models) > 0 {
		return models[0]
	}
	return defaultOpenAIEmbeddingModel
}

func shouldUseJinaEmbedding(model string, active *plugins.Active) bool {
	if !pluginActive(active, "jina") {
		return false
	}
	for _, jinaModel := range jinaEmbeddingModelsFromEnv() {
		if model == "" || model == jinaModel {
			return true
		}
	}
	return strings.HasPrefix(strings.ToLower(model), "jina-")
}

func csvValues(spec string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		v := strings.TrimSpace(raw)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func pluginActive(active *plugins.Active, name string) bool {
	if active == nil {
		return false
	}
	for _, n := range active.ActiveNames() {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

func dedupeModels(models []provider.ModelInfo) []provider.ModelInfo {
	seen := map[string]bool{}
	out := make([]provider.ModelInfo, 0, len(models))
	for _, model := range models {
		if model.ModelID == "" || seen[model.ModelID] {
			continue
		}
		seen[model.ModelID] = true
		out = append(out, model)
	}
	return out
}

func modelCreatedAt() int64 {
	return time.Now().Unix()
}
