package visionmcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStdioMcpServer is a helper that simulates a stdio MCP server for testing.
// It reads JSON-RPC requests from stdin and writes responses to stdout.
func fakeStdioMcpServer(t *testing.T, errCode int) (cmd, args string) {
	// Write a tiny Go program that speaks MCP stdio protocol.
	script := fmt.Sprintf(`
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req map[string]interface{}
		json.Unmarshal([]byte(line), &req)
		method, _ := req["method"].(string)
		id := req["id"]
		switch method {
		case "initialize":
			fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":%%v,\"result\":{}}\n", id)
		case "notifications/initialized":
			// no response
		case "tools/list":
			fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":%%v,\"result\":{\"tools\":[{\"name\":\"imageAnalysis\",\"inputSchema\":{\"properties\":{\"image_source\":null,\"prompt\":null}}}]}}\n", id)
		case "tools/call":
			if %d > 0 {
				fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":%%v,\"error\":{\"code\":%d,\"message\":\"test error\"}}\n", id)
			} else {
				fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":%%v,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"analysis result\"}]}}\n", id)
			}
		}
	}
}
`, errCode, errCode)
	// Write to a temp file and compile it.
	tmpDir := t.TempDir()
	srcPath := tmpDir + "/mcpserver.go"
	binPath := tmpDir + "/mcpserver"
	if err := os.WriteFile(srcPath, []byte(script), 0644); err != nil {
		t.Fatalf("failed to write fake MCP server script: %v", err)
	}
	buildCmd := exec.Command("go", "build", "-o", binPath, srcPath)
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build fake MCP server: %v\n%s", err, out)
	}
	return binPath, ""
}

func TestVisionMcpHappyPath(t *testing.T) {
	cmd, _ := fakeStdioMcpServer(t, 0)
	client := &Client{
		apiKey:  "test-key",
		command: cmd,
		args:    []string{},
		pending: map[int]*pendingRequest{},
	}
	defer client.Dispose()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := client.CallTool(ctx, "image_analysis", map[string]any{
		"image_source": "/path/to/image.png",
		"prompt":       "what is this?",
	})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if !strings.Contains(result, "analysis result") {
		t.Errorf("unexpected result: %s", result)
	}
	// Verify lazy initialization: child should be spawned only on first call.
	client.mu.Lock()
	initialized := client.initialized
	client.mu.Unlock()
	if !initialized {
		t.Error("expected client to be initialized after first call")
	}
}

func TestVisionMcpLazySpawn(t *testing.T) {
	cmd, _ := fakeStdioMcpServer(t, 0)
	client := &Client{
		apiKey:  "test-key",
		command: cmd,
		args:    []string{},
		pending: map[int]*pendingRequest{},
	}
	defer client.Dispose()

	// Before first call, child should be nil.
	client.mu.Lock()
	childBefore := client.child
	client.mu.Unlock()
	if childBefore != nil {
		t.Error("expected child to be nil before first call")
	}

	// After first call, child should be spawned.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := client.CallTool(ctx, "image_analysis", map[string]any{"image_source": "test.png"})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	client.mu.Lock()
	childAfter := client.child
	client.mu.Unlock()
	if childAfter == nil {
		t.Error("expected child to be spawned after first call")
	}
}

func TestVisionMcpDisposeTerminatesProcess(t *testing.T) {
	cmd, _ := fakeStdioMcpServer(t, 0)
	client := &Client{
		apiKey:  "test-key",
		command: cmd,
		args:    []string{},
		pending: map[int]*pendingRequest{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := client.CallTool(ctx, "image_analysis", map[string]any{"image_source": "test.png"})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}

	client.mu.Lock()
	pid := client.child.Process.Pid
	client.mu.Unlock()

	client.Dispose()

	// Give the process a moment to exit.
	time.Sleep(100 * time.Millisecond)

	// Check if the process is gone.
	proc, err := os.FindProcess(pid)
	if err == nil {
		// On Unix, FindProcess always succeeds. Try to signal it.
		if err := proc.Signal(os.Signal(nil)); err == nil {
			t.Errorf("expected process %d to be killed, but it's still running", pid)
		}
	}
}

func TestVisionMcpRetryOn1113Error(t *testing.T) {
	// This test verifies that a 1113 error code gets translated into a helpful message.
	// We'll simulate a server that returns error code 1113.
	cmd, _ := fakeStdioMcpServer(t, 1113)
	client := &Client{
		apiKey:  "bad-key",
		command: cmd,
		args:    []string{},
		pending: map[int]*pendingRequest{},
	}
	defer client.Dispose()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := client.CallTool(ctx, "image_analysis", map[string]any{"image_source": "test.png"})
	if err == nil {
		t.Fatal("expected error with code 1113, got nil")
	}
	// Check that the error message is actionable.
	if !strings.Contains(err.Error(), "Invalid Z.AI API key") ||
		!strings.Contains(err.Error(), "--setup") {
		t.Errorf("expected actionable error message for 1113, got: %v", err)
	}
}

func TestVisionMcpEnvVarConfiguration(t *testing.T) {
	// Verify that ACP_GLM_VISION_MCP_CMD env var is respected.
	customCmd := "/usr/bin/custom-npx"
	os.Setenv("ACP_GLM_VISION_MCP_CMD", customCmd)
	defer os.Unsetenv("ACP_GLM_VISION_MCP_CMD")

	client := New("test-key")
	if client.command != customCmd {
		t.Errorf("expected command %s, got %s", customCmd, client.command)
	}
}

// TestVisionMcpMissingNpx verifies the error message when npx is not found.
func TestVisionMcpMissingNpx(t *testing.T) {
	client := &Client{
		apiKey:  "test-key",
		command: "/nonexistent/npx",
		args:    []string{"-y", "@z_ai/mcp-server"},
		pending: map[int]*pendingRequest{},
	}
	defer client.Dispose()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := client.CallTool(ctx, "image_analysis", map[string]any{"image_source": "test.png"})
	if err == nil {
		t.Fatal("expected error for missing npx, got nil")
	}
	if !strings.Contains(err.Error(), "Vision MCP startup failed") {
		t.Errorf("expected helpful error for missing npx, got: %v", err)
	}
}

// TestVisionMcpConcurrentCalls verifies that concurrent calls don't race.
func TestVisionMcpConcurrentCalls(t *testing.T) {
	cmd, _ := fakeStdioMcpServer(t, 0)
	client := &Client{
		apiKey:  "test-key",
		command: cmd,
		args:    []string{},
		pending: map[int]*pendingRequest{},
	}
	defer client.Dispose()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := client.CallTool(ctx, "image_analysis", map[string]any{
				"image_source": fmt.Sprintf("image-%d.png", i),
			})
			if err != nil {
				t.Errorf("CallTool %d failed: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
}

// Helper to create a fake MCP server via sh -c for simpler inline scripting.
func fakeMcpServerViaShell(t *testing.T) string {
	script := `#!/bin/sh
while IFS= read -r line; do
  method=$(echo "$line" | grep -o '"method":"[^"]*"' | cut -d'"' -f4)
  id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d':' -f2)
  case "$method" in
    initialize)
      echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{}}"
      ;;
    tools/list)
      echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"tools\":[{\"name\":\"imageAnalysis\",\"inputSchema\":{\"properties\":{\"image_source\":null}}}]}}"
      ;;
    tools/call)
      echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}"
      ;;
  esac
done
`
	tmpDir := t.TempDir()
	scriptPath := tmpDir + "/mcpserver.sh"
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write shell script: %v", err)
	}
	return scriptPath
}
