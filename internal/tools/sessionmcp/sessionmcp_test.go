package sessionmcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ziozzang/agentbridge/internal/acp"
)

// fakeMCPServer is a tiny in-memory MCP simulator.
type fakeMCPServer struct {
	mu        sync.Mutex
	sessionID string
	initCount int
	listCount int
	callCount int
	tools     []map[string]any
}

func (f *fakeMCPServer) handler(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{}
	dec := json.NewDecoder(r.Body)
	_ = dec.Decode(&body)
	method, _ := body["method"].(string)
	w.Header().Set("Content-Type", "application/json")

	switch method {
	case "initialize":
		f.mu.Lock()
		f.initCount++
		f.sessionID = "sess-1"
		f.mu.Unlock()
		w.Header().Set("MCP-Session-Id", "sess-1")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": body["id"],
			"result": map[string]any{"serverInfo": map[string]any{"name": "fake"}},
		})
	case "notifications/initialized":
		w.WriteHeader(202)
	case "tools/list":
		f.mu.Lock()
		f.listCount++
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": body["id"],
			"result": map[string]any{"tools": f.tools},
		})
	case "tools/call":
		f.mu.Lock()
		f.callCount++
		f.mu.Unlock()
		params, _ := body["params"].(map[string]any)
		name, _ := params["name"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": body["id"],
			"result": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "called " + name}},
			},
		})
	default:
		http.Error(w, "unknown", 400)
	}
}

func TestSessionMcpHappyPath(t *testing.T) {
	f := &fakeMCPServer{tools: []map[string]any{
		{"name": "search", "description": "Search the web", "inputSchema": map[string]any{"properties": map[string]any{"query": map[string]any{}}}},
		{"name": "reader", "description": "Read a URL", "inputSchema": map[string]any{"properties": map[string]any{"url": map[string]any{}}}},
	}}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()

	specs := []acp.McpServer{
		{Type: "http", Name: "test-server", URL: srv.URL, Headers: map[string]string{"Authorization": "Bearer test"}},
	}
	client, err := NewWithHTTP(specs, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Dispose()

	tools := client.ToolDefinitions()
	if len(tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(tools))
	}
	// Check name namespacing: mcp__<serverName>__<toolName>
	// Note: hyphen is kept in server name (test-server not test_server)
	if !strings.HasPrefix(tools[0].Function.Name, "mcp__test-server__") {
		t.Errorf("expected mcp__test-server__ prefix, got %s", tools[0].Function.Name)
	}

	// Call a tool
	result, err := client.CallTool(context.Background(), "mcp__test-server__search", map[string]any{"query": "golang"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "called search") {
		t.Errorf("unexpected result: %s", result)
	}
	if f.initCount != 1 {
		t.Errorf("expected initialize called once, got %d", f.initCount)
	}
	if f.callCount != 1 {
		t.Errorf("expected callTool called once, got %d", f.callCount)
	}
}

func TestSessionMcpMultipleServers(t *testing.T) {
	f1 := &fakeMCPServer{tools: []map[string]any{
		{"name": "search", "inputSchema": map[string]any{}},
	}}
	srv1 := httptest.NewServer(http.HandlerFunc(f1.handler))
	defer srv1.Close()

	f2 := &fakeMCPServer{tools: []map[string]any{
		{"name": "analyze", "inputSchema": map[string]any{}},
	}}
	srv2 := httptest.NewServer(http.HandlerFunc(f2.handler))
	defer srv2.Close()

	specs := []acp.McpServer{
		{Type: "http", Name: "server-one", URL: srv1.URL},
		{Type: "http", Name: "server-two", URL: srv2.URL},
	}
	client, err := NewWithHTTP(specs, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Dispose()

	tools := client.ToolDefinitions()
	if len(tools) != 2 {
		t.Errorf("expected 2 tools from 2 servers, got %d", len(tools))
	}

	// Call tool from first server
	result, err := client.CallTool(context.Background(), "mcp__server-one__search", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "called search") {
		t.Errorf("unexpected result: %s", result)
	}

	// Call tool from second server
	result, err = client.CallTool(context.Background(), "mcp__server-two__analyze", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "called analyze") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestSessionMcpUnknownTool(t *testing.T) {
	f := &fakeMCPServer{tools: []map[string]any{
		{"name": "foo", "inputSchema": map[string]any{}},
	}}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()

	specs := []acp.McpServer{
		{Type: "http", Name: "test", URL: srv.URL},
	}
	client, err := NewWithHTTP(specs, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Dispose()

	_, err = client.CallTool(context.Background(), "mcp__test__missing", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "unknown MCP tool") {
		t.Errorf("expected unknown tool error, got: %v", err)
	}
}

func TestSessionMcpSkipNonHttp(t *testing.T) {
	specs := []acp.McpServer{
		{Type: "stdio", Name: "stdio-server"},
		{Type: "sse", Name: "sse-server"},
	}
	client, err := NewWithHTTP(specs, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Dispose()

	tools := client.ToolDefinitions()
	if len(tools) != 0 {
		t.Errorf("expected 0 tools (all non-http skipped), got %d", len(tools))
	}
}

func TestSessionMcpNameCollisionHandling(t *testing.T) {
	f := &fakeMCPServer{tools: []map[string]any{
		{"name": "tool-1", "inputSchema": map[string]any{}},
		{"name": "tool-1", "inputSchema": map[string]any{}},
	}}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()

	specs := []acp.McpServer{
		{Type: "http", Name: "srv", URL: srv.URL},
	}
	client, err := NewWithHTTP(specs, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Dispose()

	tools := client.ToolDefinitions()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools with collision handling, got %d", len(tools))
	}
	// Names should be different due to suffix addition
	if tools[0].Function.Name == tools[1].Function.Name {
		t.Errorf("expected different names for colliding tools, got both: %s", tools[0].Function.Name)
	}
}
