# Plugins

Plugins are optional extensions that add new tool definitions to the
harness. The model sees them in the function-calling surface as
`plugin__<name>__<tool>`. Activation is opt-in.

## Activating plugins

```bash
export ACP_HARNESS_PLUGINS=sqlite,duckdb
```

Order matters only for tool listing — both plugins are independent.

## Built-in plugins

### `sqlite`

Catalogue-style read-only SQLite browser.

- **Catalog**: directories listed in `ACP_HARNESS_SQLITE_DIRS`
  (comma-separated). Default:
  `$XDG_DATA_HOME/acp-harness/sqlite` or
  `~/.local/share/acp-harness/sqlite`. Files ending in `.db`,
  `.sqlite`, or `.sqlite3` are listed.
- **Tools** (each takes a `file` arg, either a catalog basename or an
  absolute path):
  - `sqlite_list` → list databases in the catalog.
  - `sqlite_load` → open a database, return its table list.
  - `sqlite_unload` → close it.
  - `sqlite_tables` → re-list its tables.
  - `sqlite_schema` (`file`, `table`) → CREATE TABLE DDL + columns.
  - `sqlite_query` (`file`, `sql`, optional `limit`) → run a SELECT/
    EXPLAIN/PRAGMA. Limited to 200 rows by default, max 10000.
  - `sqlite_exec` (`file`, `sql`) → INSERT/UPDATE/DELETE/CREATE/DROP.
    **Disabled by default**; set `ACP_HARNESS_SQLITE_RW=1` to enable.
- The database is opened with SQLite's read-only mode unless RW is on,
  so destructive statements are rejected at the driver level *as well as*
  the harness level.
- Implementation: pure Go (`modernc.org/sqlite`). No CGo, no system
  SQLite required.

### `duckdb`

Placeholder. A full DuckDB build needs CGo and a multi-MiB runtime,
which is at odds with the "small static binary" goal. The current
implementation exposes a single `duckdb_status` tool that reports
unavailability. Replace the file under
`internal/plugins/duckdb/` with a CGo-backed version (e.g.
`github.com/marcboeker/go-duckdb`) behind a build tag if you need it.

## Adding a new plugin

See **AGENTS.md → "How to add a new plugin"**.

```go
package myplugin

import (
    "context"
    "encoding/json"

    "github.com/ziozzang/glm-acp/internal/plugins"
)

func init() { plugins.Register("myplugin", func() plugins.Plugin { return &Plugin{} }) }

type Plugin struct{}

func (p *Plugin) Name() string { return "myplugin" }
func (p *Plugin) Tools() []plugins.ToolDef {
    return []plugins.ToolDef{{
        Name:        "hello",
        Description: "Say hi.",
        Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
    }}
}
func (p *Plugin) Call(_ context.Context, tool string, _ json.RawMessage) (string, error) {
    return "hi from " + tool, nil
}
```

Then add `_ "github.com/ziozzang/glm-acp/internal/plugins/myplugin"`
to the import list in `internal/agent/agent.go`.

## Security notes

Plugins run **in-process**, with the same privileges as the harness
binary. Be careful what you load — a malicious plugin can read any file
the user can read. The harness ships with a curated set; do not allow
arbitrary plugin loading from a network source.
