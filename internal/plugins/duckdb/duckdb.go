// Package duckdbplugin is a placeholder DuckDB plugin. A full DuckDB
// integration requires CGO and a multi-megabyte runtime, which is at odds
// with AgentBridge's static-binary design goal. This stub keeps the
// `duckdb` plugin name reserved and returns a structured error message
// pointing users to the LiteLLM/MotherDuck options.
//
// Activation:    add `duckdb` to AGENTBRIDGE_PLUGINS.
//
// To enable a real DuckDB integration, replace this implementation with a
// CGO-backed one (e.g. github.com/marcboeker/go-duckdb) gated by a build
// tag; the surface is left intentionally identical to the SQLite plugin
// for easy substitution.
package duckdbplugin

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ziozzang/agentbridge/internal/plugins"
)

// Name is the plugin identifier.
const Name = "duckdb"

func init() {
	plugins.Register(Name, func() plugins.Plugin { return &stub{} })
}

type stub struct{}

func (s *stub) Name() string { return Name }

func (s *stub) Tools() []plugins.ToolDef {
	return []plugins.ToolDef{
		{
			Name:        "duckdb_status",
			Description: "Returns the status of the DuckDB plugin. In this build the plugin is a placeholder — DuckDB requires a CGO build (see docs/plugins.md).",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		},
	}
}

func (s *stub) Call(_ context.Context, tool string, _ json.RawMessage) (string, error) {
	switch tool {
	case "duckdb_status":
		return `{"status":"unavailable","message":"DuckDB plugin requires a CGO build; rebuild with -tags duckdb or use a LiteLLM/MotherDuck bridge."}`, nil
	default:
		return "", errors.New("duckdb: plugin is a placeholder in this build")
	}
}
