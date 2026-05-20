# Plugins

Plugins add optional tool definitions to AgentBridge. Enable plugins with:

```bash
AGENTBRIDGE_PLUGINS=sqlite,duckdb agentbridge
```

Legacy `ACP_HARNESS_PLUGINS` is still accepted.

## SQLite

The SQLite plugin exposes a read-only catalogue browser by default.

Variables:

| Variable | Purpose |
| --- | --- |
| `AGENTBRIDGE_SQLITE_DIRS` | Comma-separated catalogue directories. |
| `AGENTBRIDGE_SQLITE_RW` | Set to `1` to allow write statements. |

Default catalogue:

- `$XDG_DATA_HOME/agentbridge/sqlite`
- `~/.local/share/agentbridge/sqlite`

Tools:

- `sqlite_list`
- `sqlite_load`
- `sqlite_unload`
- `sqlite_tables`
- `sqlite_schema`
- `sqlite_query`
- `sqlite_exec` when read-write mode is enabled

## DuckDB

The DuckDB plugin is currently a placeholder because a full DuckDB runtime
requires CGO and a larger binary. The plugin name and tool surface are
reserved for a future CGO-enabled implementation.

## Security

Plugins run in-process with the same privileges as AgentBridge. Only enable
plugins you trust, and keep write-capable plugins disabled unless you need
them.
