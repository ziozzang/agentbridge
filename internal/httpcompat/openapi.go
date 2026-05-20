package httpcompat

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
)

func (h *handler) openapi(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	paths := map[string]any{
		"/v1/chat/completions": pathItem("post", "OpenAI-compatible chat completions"),
		"/v1/responses":        pathItem("post", "OpenAI-compatible responses"),
		"/v1/responses/{id}": map[string]any{"get": map[string]any{
			"summary":    "Retrieve a stored response",
			"parameters": []map[string]any{{"name": "id", "in": "path", "required": true, "schema": map[string]any{"type": "string"}}},
			"responses":  okResponse(),
		}},
		"/v1/messages": pathItem("post", "Anthropic-compatible messages"),
		"/v1/a2a/rpc":  pathItem("post", "A2A JSON-RPC endpoint"),
		"/v1/mcp":      pathItem("post", "MCP Streamable HTTP JSON-RPC endpoint"),
		"/v1/agui/run": pathItem("post", "AG-UI SSE run endpoint"),
		"/.well-known/agent-card.json": map[string]any{"get": map[string]any{
			"summary":   "A2A agent card",
			"responses": okResponse(),
		}},
		"/metrics": map[string]any{"get": map[string]any{
			"summary":   "Prometheus metrics",
			"responses": map[string]any{"200": map[string]any{"description": "Prometheus text format"}},
		}},
		"/health": map[string]any{"get": map[string]any{
			"summary":   "Health check",
			"responses": okResponse(),
		}},
	}
	for _, tool := range h.mcpToolMaps() {
		name, _ := tool["name"].(string)
		if name == "" {
			continue
		}
		desc, _ := tool["description"].(string)
		if desc == "" {
			desc = "Call AgentBridge tool " + name
		}
		paths["/v1/tools/"+url.PathEscape(name)] = toolPathItem(name, desc, tool["inputSchema"])
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"openapi": "3.1.1",
		"info": map[string]any{
			"title":   "AgentBridge compatibility API",
			"version": "1.0.0",
		},
		"servers": []map[string]any{{"url": "/"}},
		"paths":   paths,
		"components": map[string]any{
			"schemas": map[string]any{
				"JSONValue": map[string]any{},
				"JSONRPCRequest": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"jsonrpc": map[string]any{"type": "string", "const": "2.0"},
						"id":      map[string]any{},
						"method":  map[string]any{"type": "string"},
						"params":  map[string]any{},
					},
					"required": []string{"method"},
				},
			},
		},
	})
}

func toolPathItem(name, summary string, schema any) map[string]any {
	return map[string]any{"post": map[string]any{
		"summary":     summary,
		"operationId": "tool_" + name,
		"tags":        []string{"MCP Tools"},
		"requestBody": map[string]any{
			"required": true,
			"content": map[string]any{"application/json": map[string]any{
				"schema": openAPISchema(schema),
			}},
		},
		"responses": okResponse(),
	}}
}

func openAPISchema(schema any) any {
	switch v := schema.(type) {
	case json.RawMessage:
		if len(v) == 0 {
			return map[string]any{"type": "object"}
		}
		var out any
		if err := json.Unmarshal(v, &out); err == nil {
			return out
		}
	case []byte:
		var out any
		if err := json.Unmarshal(v, &out); err == nil {
			return out
		}
	case map[string]any:
		return v
	}
	return map[string]any{"type": "object"}
}

func (h *handler) swaggerUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>AgentBridge API</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    window.ui = SwaggerUIBundle({ url: "/openapi.json", dom_id: "#swagger-ui" });
  </script>
</body>
</html>`)
}

func pathItem(method, summary string) map[string]any {
	return map[string]any{method: map[string]any{
		"summary": summary,
		"requestBody": map[string]any{
			"required": true,
			"content":  map[string]any{"application/json": map[string]any{"schema": map[string]any{}}},
		},
		"responses": okResponse(),
	}}
}

func okResponse() map[string]any {
	return map[string]any{"200": map[string]any{
		"description": "OK",
		"content":     map[string]any{"application/json": map[string]any{"schema": map[string]any{}}},
	}}
}
