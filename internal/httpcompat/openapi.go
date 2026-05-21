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
		"/v1/chat/completions":  pathItem("post", "OpenAI-compatible chat completions"),
		"/v1/responses":         pathItem("post", "OpenAI-compatible responses"),
		"/v1/responses/compact": compactPathItem(),
		"/v1/responses/{id}": map[string]any{"get": map[string]any{
			"summary":    "Retrieve a stored response",
			"parameters": []map[string]any{{"name": "id", "in": "path", "required": true, "schema": map[string]any{"type": "string"}}},
			"responses":  okResponse(),
		}},
		"/v1/messages":   pathItem("post", "Anthropic-compatible messages"),
		"/v1/embeddings": embeddingsPathItem(),
		"/v1/rerank":     rerankPathItem(),
		"/v1/models": map[string]any{"get": map[string]any{
			"summary":   "OpenAI-compatible model list",
			"responses": okResponse(),
		}},
		"/v1/a2a/rpc": pathItem("post", "A2A JSON-RPC endpoint"),
		"/v1/mcp":     pathItem("post", "MCP Streamable HTTP JSON-RPC endpoint"),
		"/v1/mcp/catalog": map[string]any{"get": map[string]any{
			"summary":   "Configured MCP server and tool catalog",
			"responses": okResponse(),
		}},
		"/v1/tool-catalog": map[string]any{"get": map[string]any{
			"summary":   "AgentBridge builtin, plugin, and MCP tool catalog",
			"responses": okResponse(),
		}},
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
		httpName := publicHTTPToolName(name)
		paths["/v1/tools/"+url.PathEscape(httpName)] = toolPathItem(httpName, desc, tool["inputSchema"])
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

func compactPathItem() map[string]any {
	return map[string]any{"post": map[string]any{
		"summary": "Compact a conversation into a smaller replacement message set",
		"requestBody": map[string]any{
			"required": true,
			"content": map[string]any{"application/json": map[string]any{
				"schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"model":                map[string]any{"type": "string"},
						"input":                map[string]any{},
						"messages":             map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
						"previous_response_id": map[string]any{"type": "string"},
						"target_tokens":        map[string]any{"type": "integer", "minimum": 1},
						"strategy":             map[string]any{"type": "string", "enum": []string{"auto", "native", "summary", "prune", "none"}},
						"reason":               map[string]any{"type": "string"},
					},
					"example": map[string]any{
						"model":         defaultModel(),
						"messages":      []map[string]any{{"role": "system", "content": "You are concise."}, {"role": "user", "content": "Long conversation history..."}},
						"strategy":      "auto",
						"target_tokens": 8000,
					},
				},
			}},
		},
		"responses": okResponse(),
	}}
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

func embeddingsPathItem() map[string]any {
	return map[string]any{"post": map[string]any{
		"summary": "OpenAI-compatible embeddings",
		"requestBody": map[string]any{
			"required": true,
			"content": map[string]any{"application/json": map[string]any{
				"schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"model":           map[string]any{"type": "string", "example": defaultJinaEmbeddingModel},
						"input":           map[string]any{"oneOf": []map[string]any{{"type": "string"}, {"type": "array", "items": map[string]any{"type": "string"}}}, "example": "hello"},
						"encoding_format": map[string]any{"type": "string", "enum": []string{"float", "base64"}},
						"dimensions":      map[string]any{"type": "integer", "minimum": 1},
					},
					"required": []string{"input"},
					"example": map[string]any{
						"model": defaultJinaEmbeddingModel,
						"input": "hello",
					},
				},
			}},
		},
		"responses": okResponse(),
	}}
}

func rerankPathItem() map[string]any {
	return map[string]any{"post": map[string]any{
		"summary": "Jina-compatible rerank",
		"requestBody": map[string]any{
			"required": true,
			"content": map[string]any{"application/json": map[string]any{
				"schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"model":            map[string]any{"type": "string", "example": defaultJinaRerankModel},
						"query":            map[string]any{"type": "string", "example": "agentbridge"},
						"documents":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "example": []string{"AgentBridge routes models.", "Unrelated text."}},
						"top_n":            map[string]any{"type": "integer", "minimum": 1},
						"return_documents": map[string]any{"type": "boolean"},
					},
					"required": []string{"query", "documents"},
					"example": map[string]any{
						"model":     defaultJinaRerankModel,
						"query":     "agentbridge",
						"documents": []string{"AgentBridge routes models.", "Unrelated text."},
						"top_n":     1,
					},
				},
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
