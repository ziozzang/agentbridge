# Plugins

Plugins add optional tool definitions to AgentBridge. Enable plugins with:

```bash
AGENTBRIDGE_PLUGINS=sqlite,duckdb,jina,ollama_search,xai,openai_embed agentbridge
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

## xAI Direct Tools

The xAI plugin exposes direct-to-xAI auxiliary APIs without requiring
`AGENTBRIDGE_PROVIDER=xai`. It uses `~/.grok/auth.json` / Hermes-compatible
`xai-oauth` credentials when available, then falls back to `XAI_API_KEY`.

Enable it with:

```bash
AGENTBRIDGE_PLUGINS=xai agentbridge --http-listen 127.0.0.1:8766
```

Variables:

| Variable | Purpose |
| --- | --- |
| `AGENTBRIDGE_XAI_API_KEY` | xAI API key. `XAI_API_KEY` is also accepted. |
| `AGENTBRIDGE_XAI_BASE_URL` | Override API base. Defaults to `https://api.x.ai/v1`. |
| `AGENTBRIDGE_XAI_SEARCH_MODEL` | Default X Search Responses model. Defaults to `grok-4.3`. |
| `AGENTBRIDGE_XAI_IMAGE_MODEL` | Default image model. Defaults to `grok-imagine-image`. |
| `AGENTBRIDGE_XAI_OAUTH_PATH` | Override OAuth auth store path. Defaults to `~/.grok/auth.json`. |

Tools:

- `xai_x_search` routes a query through the Responses API with the hosted
  `x_search` tool.
- `xai_image_generate` calls `/v1/images/generations`.
- `xai_image_edit` calls `/v1/images/edits`.

## OpenAI-Compatible Embeddings

The `openai_embed` plugin exposes any OpenAI-compatible `/embeddings` endpoint
as a tool. This is intended for LiteLLM, OpenAI, local vLLM, or similar
gateways.

Enable it with LiteLLM:

```bash
AGENTBRIDGE_PLUGINS=openai_embed \
AGENTBRIDGE_EMBEDDINGS_BASE_URL=http://127.0.0.1:4000/v1 \
AGENTBRIDGE_EMBEDDINGS_API_KEY=... \
AGENTBRIDGE_EMBEDDINGS_MODEL=text-embedding-3-small \
agentbridge
```

Variables:

| Variable | Purpose |
| --- | --- |
| `AGENTBRIDGE_EMBEDDINGS_BASE_URL` | OpenAI-compatible API base. Falls back to `LITELLM_BASE_URL`, `OPENAI_BASE_URL`, then `http://localhost:4000/v1`. |
| `AGENTBRIDGE_EMBEDDINGS_API_KEY` | Bearer token. Falls back to `LITELLM_API_KEY`, `OPENAI_API_KEY`, `AGENTBRIDGE_API_KEY`. |
| `AGENTBRIDGE_EMBEDDINGS_MODEL` | Default embedding model. Falls back to `LITELLM_EMBEDDINGS_MODEL`, `OPENAI_EMBEDDINGS_MODEL`, then `text-embedding-3-small`. |

Tool:

- `embed`

## MCP Tool-Only Mode

The HTTP compatibility server exposes active plugins through MCP. This works
without selecting or calling an LLM provider:

```bash
AGENTBRIDGE_PLUGINS=xai,openai_embed agentbridge --http-listen 127.0.0.1:8766
```

Use `POST /mcp` or `POST /v1/mcp` with MCP `tools/list` and `tools/call`.
The `chat` MCP tool still exists, but plugin tools can be listed and called
independently.

## Security

Plugins run in-process with the same privileges as AgentBridge. Only enable
plugins you trust, and keep write-capable plugins disabled unless you need
them.
