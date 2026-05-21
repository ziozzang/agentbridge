package httpcompat

import (
	"net/http"

	"github.com/ziozzang/agentbridge/internal/plugins"
	"github.com/ziozzang/agentbridge/internal/tools/definitions"
)

func (h *handler) catalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object":  "catalog",
		"tools":   h.toolCatalog(),
		"mcp":     h.mcpCatalog(),
		"plugins": h.pluginCatalog(),
	})
}

func (h *handler) toolCatalog() []map[string]any {
	var out []map[string]any
	for _, t := range definitions.All() {
		out = append(out, toolEntry("builtin", "", t.Function.Name, t.Function.Description, t.Function.Parameters))
	}
	if h.plugins != nil {
		for _, p := range h.plugins.Plugins() {
			for _, t := range p.Tools() {
				out = append(out, toolEntry("plugin", p.Name(), plugins.ToolName(p.Name(), t.Name), t.Description, t.Parameters))
			}
		}
	}
	if h.externalMCP != nil {
		for _, srv := range h.externalMCP.Catalog() {
			for _, t := range srv.Tools {
				out = append(out, toolEntry("mcp", srv.Name, t.Name, t.Description, t.InputSchema))
			}
		}
	}
	return out
}

func (h *handler) mcpCatalog() []any {
	if h.externalMCP == nil {
		return nil
	}
	out := make([]any, 0)
	for _, srv := range h.externalMCP.Catalog() {
		out = append(out, srv)
	}
	return out
}

func (h *handler) pluginCatalog() []map[string]any {
	if h.plugins == nil {
		return nil
	}
	out := make([]map[string]any, 0)
	for _, p := range h.plugins.Plugins() {
		tools := make([]map[string]any, 0)
		for _, t := range p.Tools() {
			tools = append(tools, map[string]any{
				"name":         plugins.ToolName(p.Name(), t.Name),
				"source_name":  t.Name,
				"description":  t.Description,
				"input_schema": t.Parameters,
			})
		}
		out = append(out, map[string]any{"name": p.Name(), "tools": tools})
	}
	return out
}

func toolEntry(source, owner, name, desc string, schema any) map[string]any {
	return map[string]any{
		"name":         name,
		"description":  desc,
		"source":       source,
		"owner":        owner,
		"input_schema": schema,
	}
}
