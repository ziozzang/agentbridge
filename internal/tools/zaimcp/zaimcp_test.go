package zaimcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeMCPServer is a tiny in-memory Z.AI MCP simulator.
type fakeMCPServer struct {
	mu           sync.Mutex
	sessionID    string
	initCount    int
	listCount    int
	callCount    int
	tools        []map[string]any
	failNextCall bool // simulate -32601 for first tools/call
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
		fail := f.failNextCall
		f.failNextCall = false
		f.mu.Unlock()
		if fail {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": body["id"],
				"error": map[string]any{"code": -32601, "message": "Method not found"},
			})
			return
		}
		params, _ := body["params"].(map[string]any)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": body["id"],
			"result": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "called " + params["name"].(string)}},
			},
		})
	default:
		http.Error(w, "unknown", 400)
	}
}

func TestCallToolHappyPath(t *testing.T) {
	f := &fakeMCPServer{tools: []map[string]any{
		{"name": "webSearchPrime", "inputSchema": map[string]any{"properties": map[string]any{"query": map[string]any{}}}},
	}}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	c := New()
	c.HTTP = srv.Client()
	out, err := c.CallTool(context.Background(), CallToolInput{
		Endpoint: srv.URL, APIKey: "k", ToolName: "webSearchPrime",
		Arguments: map[string]any{"query": "go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "called webSearchPrime") {
		t.Errorf("unexpected result: %s", out)
	}
	// Second call must reuse the cached session and not re-initialize.
	_, err = c.CallTool(context.Background(), CallToolInput{
		Endpoint: srv.URL, APIKey: "k", ToolName: "webSearchPrime",
		Arguments: map[string]any{"query": "go again"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.initCount != 1 {
		t.Errorf("expected initialize cached (initCount=1), got %d", f.initCount)
	}
}

func TestCallToolRetriesOnMethodNotFound(t *testing.T) {
	f := &fakeMCPServer{
		tools:        []map[string]any{{"name": "webReader", "inputSchema": map[string]any{"properties": map[string]any{}}}},
		failNextCall: true,
	}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	c := New()
	c.HTTP = srv.Client()
	out, err := c.CallTool(context.Background(), CallToolInput{
		Endpoint: srv.URL, APIKey: "k", ToolName: "webReader",
		Arguments: map[string]any{"url": "https://example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "called webReader") {
		t.Errorf("unexpected: %s", out)
	}
	if f.initCount < 2 {
		t.Errorf("expected re-init after retry, got initCount=%d", f.initCount)
	}
}

func TestSseParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		method, _ := body["method"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("MCP-Session-Id", "x")
		switch method {
		case "initialize":
			w.Write([]byte("data: " + jsonString(map[string]any{
				"jsonrpc": "2.0", "id": body["id"],
				"result": map[string]any{},
			}) + "\n\n"))
		case "notifications/initialized":
			w.WriteHeader(200)
		case "tools/list":
			w.Write([]byte("data: " + jsonString(map[string]any{
				"jsonrpc": "2.0", "id": body["id"],
				"result": map[string]any{"tools": []any{map[string]any{"name": "foo"}}},
			}) + "\n\n"))
		case "tools/call":
			w.Write([]byte("data: " + jsonString(map[string]any{
				"jsonrpc": "2.0", "id": body["id"],
				"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}},
			}) + "\n\n"))
		}
	}))
	defer srv.Close()
	c := New()
	c.HTTP = srv.Client()
	out, err := c.CallTool(context.Background(), CallToolInput{
		Endpoint: srv.URL, APIKey: "k", ToolName: "foo", Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "ok") {
		t.Errorf("unexpected: %s", out)
	}
}

func jsonString(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestSnakeToCamel(t *testing.T) {
	cases := map[string]string{
		"foo":         "foo",
		"foo_bar":     "fooBar",
		"foo_bar_baz": "fooBarBaz",
		"already_one": "alreadyOne",
		"trailing_":   "trailing",
	}
	for in, want := range cases {
		if got := snakeToCamel(in); got != want {
			t.Errorf("snakeToCamel(%q) = %q, want %q", in, got, want)
		}
	}
}
