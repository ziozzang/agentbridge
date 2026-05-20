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
    allow_tools: foo, bar*
    deny_tools:
      - bar_debug
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
	if len(got[0].AllowTools) != 2 || got[0].AllowTools[0] != "foo" || got[0].AllowTools[1] != "bar*" {
		t.Fatalf("allow=%#v", got[0].AllowTools)
	}
	if len(got[0].DenyTools) != 1 || got[0].DenyTools[0] != "bar_debug" {
		t.Fatalf("deny=%#v", got[0].DenyTools)
	}
}

func TestLoadMCPServersMap(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("MCP_TOKEN", "secret-token")
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte(`{
  "mcpServers": {
    "docs": {
      "type": "http",
      "url": "http://127.0.0.1:8091/mcp"
    },
    "cli": {
      "type": "stdio",
      "command": "${SHELL}",
      "args": "-lc, true",
      "env": {"TOKEN": "${MCP_TOKEN}"},
      "allow_tools": ["foo"]
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
	if len(got) != 2 || got[0].Name == "" || got[1].Name == "" {
		t.Fatalf("servers=%#v", got)
	}
}
