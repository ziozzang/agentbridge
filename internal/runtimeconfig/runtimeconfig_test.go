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

func TestLoadCompactionConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`compaction:
  enabled: true
  native: false
  summary: true
  prune_fallback: false
  threshold_pct: 75
  target_pct: 60
  overflow_target_pct: 50
  preserve_turns: 5
  keep_recent_tokens: 12000
  reserve_tokens: 8000
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTBRIDGE_CONFIG_FILE", path)

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Compaction.Enabled == nil || !*got.Compaction.Enabled {
		t.Fatalf("enabled=%v", got.Compaction.Enabled)
	}
	if got.Compaction.Native == nil || *got.Compaction.Native {
		t.Fatalf("native=%v", got.Compaction.Native)
	}
	if got.Compaction.PruneFallback == nil || *got.Compaction.PruneFallback {
		t.Fatalf("prune_fallback=%v", got.Compaction.PruneFallback)
	}
	if got.Compaction.ThresholdPct == nil || *got.Compaction.ThresholdPct != 75 {
		t.Fatalf("threshold=%v", got.Compaction.ThresholdPct)
	}
	if got.Compaction.PreserveTurns != 5 || got.Compaction.KeepRecentTokens != 12000 || got.Compaction.ReserveTokens != 8000 {
		t.Fatalf("compaction=%#v", got.Compaction)
	}
}

func TestLoadAgentYoloModeConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`agent:
  yolo_mode: false
  permission_mode: read_only
  permission_decision: reject
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTBRIDGE_CONFIG_FILE", path)

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent.YoloMode == nil || *got.Agent.YoloMode {
		t.Fatalf("agent.yolo_mode=%v", got.Agent.YoloMode)
	}
	if got.Agent.PermissionMode != "read_only" || got.Agent.PermissionDecision != "reject" {
		t.Fatalf("agent=%#v", got.Agent)
	}
}

func TestLoadPIIEnvConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`pii:
  enabled: true
  env:
    enabled: true
    file: ~/env
    names: [OPENAI_API_KEY, XAI_API_KEY]
    min_length: 20
    mask: "[MASK_KEY_{n}]"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTBRIDGE_CONFIG_FILE", path)

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !got.PII.Enabled || !got.PII.Env.Enabled || got.PII.Env.File != "~/env" {
		t.Fatalf("pii=%#v", got.PII)
	}
	if got.PII.Env.MinLength != 20 || got.PII.Env.Mask != "[MASK_KEY_{n}]" {
		t.Fatalf("env=%#v", got.PII.Env)
	}
	if len(got.PII.Env.Names) != 2 || got.PII.Env.Names[0] != "OPENAI_API_KEY" {
		t.Fatalf("names=%#v", got.PII.Env.Names)
	}
}
