# Plugins

Plugins add optional tool definitions to AgentBridge. Enable plugins with:

```bash
AGENTBRIDGE_PLUGINS=sqlite,duckdb,jina,ollama_search agentbridge
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

## Jina

The Jina plugin exposes the official Reader, Search, and Embeddings APIs:

- Reader: `https://r.jina.ai`
- Search: `https://s.jina.ai`
- Embeddings: `https://api.jina.ai/v1/embeddings`

Enable it with:

```bash
AGENTBRIDGE_PLUGINS=jina agentbridge
```

Variables:

| Variable | Purpose |
| --- | --- |
| `AGENTBRIDGE_JINA_API_KEY` | Optional Jina API key. `JINA_API_KEY` is also accepted. |
| `AGENTBRIDGE_JINA_READER_BASE_URL` | Override Reader base URL. Defaults to `https://r.jina.ai`. |
| `AGENTBRIDGE_JINA_SEARCH_BASE_URL` | Override Search base URL. Defaults to `https://s.jina.ai`. |
| `AGENTBRIDGE_JINA_EMBEDDINGS_BASE_URL` | Override embeddings API base. Defaults to `https://api.jina.ai/v1`. |
| `AGENTBRIDGE_JINA_EMBEDDINGS_MODEL` | Default embedding model. Defaults to `jina-embeddings-v3`. |

Tools:

- `jina_reader`
- `jina_search`
- `jina_embed`

## Ollama Search

The Ollama Search plugin exposes Ollama Cloud's official web APIs:

- `POST https://ollama.com/api/web_search`
- `POST https://ollama.com/api/web_fetch`

Enable it with:

```bash
AGENTBRIDGE_PLUGINS=ollama_search OLLAMA_API_KEY=... agentbridge
```

Variables:

| Variable | Purpose |
| --- | --- |
| `AGENTBRIDGE_OLLAMA_SEARCH_API_KEY` | Ollama API key. `OLLAMA_API_KEY` is also accepted. |
| `AGENTBRIDGE_OLLAMA_SEARCH_BASE_URL` | Override base URL. Defaults to `https://ollama.com`. |

Tools:

- `ollama_search`
- `ollama_fetch`

## Security

Plugins run in-process with the same privileges as AgentBridge. Only enable
plugins you trust, and keep write-capable plugins disabled unless you need
them.
