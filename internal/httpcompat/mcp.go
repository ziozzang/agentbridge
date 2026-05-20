package httpcompat

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/ziozzang/agentbridge/internal/metrics"
	"github.com/ziozzang/agentbridge/internal/plugins"
	"github.com/ziozzang/agentbridge/internal/provider"
)

func (h *handler) mcp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		sessionID = generateID("mcp")
		w.Header().Set("Mcp-Session-Id", sessionID)
	}
	w.Header().Set("MCP-Protocol-Version", "2025-06-18")

	var req jsonRPCRequest
	if err := decodeBody(r, &req); err != nil {
		writeJSONRPC(w, http.StatusBadRequest, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcError(-32700, err.Error(), nil)})
		return
	}
	switch req.Method {
	case "initialize":
		writeJSONRPC(w, http.StatusOK, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": "2025-06-18",
			"serverInfo":      map[string]any{"name": "agentbridge", "version": "1"},
			"capabilities": map[string]any{
				"tools":     map[string]any{"listChanged": false},
				"resources": map[string]any{},
				"prompts":   map[string]any{},
			},
		}})
	case "tools/list":
		writeJSONRPC(w, http.StatusOK, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"tools": h.mcpToolMaps(),
		}})
	case "tools/call":
		result, err := h.mcpToolCall(r, req.Params)
		if err != nil {
			writeJSONRPC(w, http.StatusOK, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
				"isError": true,
				"content": []map[string]any{{"type": "text", "text": err.Error()}},
			}})
			return
		}
		writeJSONRPC(w, http.StatusOK, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
	case "resources/list":
		writeJSONRPC(w, http.StatusOK, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"resources": []any{}}})
	case "prompts/list":
		writeJSONRPC(w, http.StatusOK, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"prompts": []any{}}})
	default:
		writeJSONRPC(w, http.StatusOK, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcError(-32601, "method not found: "+req.Method, nil)})
	}
}

func (h *handler) mcpToolMaps() []map[string]any {
	tools := []map[string]any{{
		"name":        "chat",
		"description": "Send a prompt to the configured AgentBridge provider.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"input": map[string]any{"type": "string"},
				"model": map[string]any{"type": "string"},
			},
			"required": []string{"input"},
		},
	}}
	if h.plugins != nil {
		for _, t := range h.plugins.Tools() {
			tools = append(tools, map[string]any{
				"name":        t.Function.Name,
				"description": t.Function.Description,
				"inputSchema": json.RawMessage(t.Function.Parameters),
			})
		}
	}
	if h.externalMCP != nil {
		for _, t := range h.externalMCP.ToolDefinitions() {
			tools = append(tools, map[string]any{
				"name":        t.Function.Name,
				"description": t.Function.Description,
				"inputSchema": json.RawMessage(t.Function.Parameters),
			})
		}
	}
	return tools
}

func (h *handler) toolHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/v1/tools/")
	name = strings.TrimPrefix(name, "/tools/")
	name, _ = url.PathUnescape(name)
	name = h.resolveHTTPToolName(name)
	if name == "" {
		http.Error(w, "tool name is required", http.StatusBadRequest)
		return
	}
	var args map[string]any
	if err := decodeBody(r, &args); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	raw, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	result, err := h.mcpToolCall(r, raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) resolveHTTPToolName(name string) string {
	if name == "" || h.plugins == nil {
		return name
	}
	if _, _, ok := plugins.SplitToolName(name); ok {
		return name
	}
	for _, tool := range h.plugins.Tools() {
		if publicHTTPToolName(tool.Function.Name) == name {
			return tool.Function.Name
		}
	}
	return name
}

func publicHTTPToolName(name string) string {
	_, tool, ok := plugins.SplitToolName(name)
	if ok && tool != "" {
		return tool
	}
	return name
}

func (h *handler) mcpToolCall(r *http.Request, raw json.RawMessage) (map[string]any, error) {
	var req struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if h.plugins != nil {
		if _, _, ok := plugins.SplitToolName(req.Name); ok {
			args, err := json.Marshal(req.Arguments)
			if err != nil {
				return nil, err
			}
			result, claimed, err := h.plugins.Dispatch(r.Context(), req.Name, args)
			if claimed {
				metrics.ObserveToolCall("plugin", req.Name, err == nil)
				if err != nil {
					return nil, err
				}
				return map[string]any{
					"isError": false,
					"content": []map[string]any{{"type": "text", "text": result}},
				}, nil
			}
		}
	}
	if h.externalMCP != nil && strings.HasPrefix(req.Name, "mcp__") {
		result, err := h.externalMCP.CallTool(r.Context(), req.Name, req.Arguments)
		metrics.ObserveToolCall("mcp", req.Name, err == nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"isError": false,
			"content": []map[string]any{{"type": "text", "text": result}},
		}, nil
	}
	if req.Name != "chat" {
		return nil, errors.New("unknown tool: " + req.Name)
	}
	model, messages := genericMessages(req.Arguments)
	if len(messages) == 0 {
		return nil, errors.New("input is required")
	}
	text, _, _, err := RunProvider(r.Context(), model, messages)
	metrics.ObserveToolCall("mcp", "chat", err == nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"isError": false,
		"content": []map[string]any{{"type": "text", "text": text}},
	}, nil
}

func genericMessages(in map[string]any) (string, []provider.Message) {
	if in == nil {
		return "", nil
	}
	model, _ := in["model"].(string)
	if msgs := responsesInput(in["messages"]); len(msgs) > 0 {
		return model, msgs
	}
	if s, _ := in["input"].(string); s != "" {
		return model, []provider.Message{{Role: "user", Content: s}}
	}
	if s, _ := in["prompt"].(string); s != "" {
		return model, []provider.Message{{Role: "user", Content: s}}
	}
	return model, nil
}
