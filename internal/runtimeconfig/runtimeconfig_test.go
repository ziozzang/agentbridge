package runtimeconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadServerConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`server:
  enabled: true
  listen: 127.0.0.1:9000
  pool_size: 8
  wait_size: 4
  http_listen: 127.0.0.1:9001
  grpc_listen: 127.0.0.1:9002
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTBRIDGE_CONFIG_FILE", path)

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Server.Enabled || got.Server.Listen != "127.0.0.1:9000" || got.Server.PoolSize != 8 {
		t.Fatalf("server=%#v", got.Server)
	}
	if got.Server.WaitSize == nil || *got.Server.WaitSize != 4 {
		t.Fatalf("wait=%v", got.Server.WaitSize)
	}
	if got.Server.HTTPListen != "127.0.0.1:9001" || got.Server.GRPCListen != "127.0.0.1:9002" {
		t.Fatalf("server=%#v", got.Server)
	}
}
