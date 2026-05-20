package mcpconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadYAMLExpandsEnvAndSkipsDisabled(t *testing.T) {
	t.Setenv("MCP_TOKEN", "secret-token")
	t.Setenv("MCP_HOST", "127.0.0.1:8090")
	t.Setenv("AGENTBRIDGE_DISABLED_MCPS", "off")
	path := filepath.Join(t.TempDir(), "mcp.yaml")
	if err := os.WriteFile(path, []byte(`
mcp_servers:
  - name: search
    type: http
    url: http://${MCP_HOST}/mcp
    headers:
      Authorization: Bearer ${MCP_TOKEN}
  - name: off
    type: http
    url: http://example.invalid/mcp
  - name: disabled
    type: http
    url: http://example.invalid/mcp
    enabled: false
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTBRIDGE_MCP_FILE", path)

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len=%d want 1: %#v", len(got), got)
	}
	if got[0].Name != "search" || got[0].URL != "http://127.0.0.1:8090/mcp" {
		t.Fatalf("server=%#v", got[0])
	}
	if got[0].Headers["Authorization"] != "Bearer secret-token" {
		t.Fatalf("headers=%#v", got[0].Headers)
	}
}

func TestLoadMCPServersMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte(`{
  "mcpServers": {
    "docs": {
      "type": "http",
      "url": "http://127.0.0.1:8091/mcp"
    }
  }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTBRIDGE_MCP_FILE", path)

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "docs" {
		t.Fatalf("servers=%#v", got)
	}
}
