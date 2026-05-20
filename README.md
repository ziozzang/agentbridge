# glm-acp — a generic, high-quality ACP harness

[![Go tests](https://img.shields.io/badge/go%20test-passing-brightgreen)](#testing)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](#license)

`glm-acp` is an [**Agent Client Protocol**](https://github.com/zed-industries/agent-client-protocol)
(ACP) coding agent written in Go. It runs on stdio as a JSON-RPC 2.0 /
newline-delimited JSON server, or as a TCP server for long-running pooled
deployments, and bridges any ACP-aware editor
(e.g. Zed) to a wide range of LLM providers — OpenAI Chat Completions,
OpenAI Responses, Anthropic Messages, Ollama, Z.AI/GLM, OpenRouter,
LiteLLM, Codex OAuth — through a single small static binary.

> The project started as a focused GLM adapter. It is now a *provider-
> neutral harness*; the binary name `glm-acp-agent` and Go module path
> are kept for backwards compatibility.

## Highlights

- **One agent, many providers.** Switch upstream model with a single env
  variable (`ACP_HARNESS_PROVIDER=openai|anthropic|ollama|glm|…`).
- **Provider templates** in `internal/config/providers.yaml`, overridable
  per-user in `$XDG_CONFIG_HOME/acp-harness/providers.yaml`.
- **Plugin system** activated with `ACP_HARNESS_PLUGINS=sqlite[,…]`.
  Ships with a pure-Go SQLite catalogue browser
  (`sqlite_list/load/query/…`).
- **Codex OAuth** support: use `oauth:codex` API keys; the harness
  refreshes access tokens automatically against the OpenAI OAuth endpoint.
- **Structured logging** with level, file sink, and size-based rotation
  (`ACP_HARNESS_LOG_*`). Stdout stays pure JSON-RPC.
- **Local tools** built in: `read_file`, `write_file`, `list_files`,
  `run_command`, `web_search`, `web_reader`, `image_analysis`. Writes
  and shell commands always ask the ACP client for permission.
- **Persistent sessions** under `$XDG_STATE_HOME/glm-acp-agent/sessions/`.
- **Optional TCP server mode** with a bounded connection pool for shared
  daemon-style deployments.
- **Container-first**: ships with a multi-stage `Dockerfile`.
- **Designed for editors**: lean memory and CPU profile; back-pressure-
  aware channel design; no polling loops.

## Quick start

```bash
go build -o glm-acp-agent ./cmd/glm-acp-agent

# OpenAI Chat (or LiteLLM / OpenRouter / vLLM with OPENAI_BASE_URL …)
ACP_HARNESS_PROVIDER=openai \
ACP_HARNESS_API_KEY=sk-... \
ACP_HARNESS_MODEL=gpt-4o-mini \
./glm-acp-agent

# Anthropic
ACP_HARNESS_PROVIDER=anthropic \
ACP_HARNESS_API_KEY=sk-ant-... \
ACP_HARNESS_MODEL=claude-sonnet-4-5 \
./glm-acp-agent

# Local Ollama
ACP_HARNESS_PROVIDER=ollama ACP_HARNESS_MODEL=llama3.1 ./glm-acp-agent

# Z.AI / GLM (default; back-compat with the legacy tool)
Z_AI_API_KEY=sk-... ACP_HARNESS_PROVIDER=glm ./glm-acp-agent
```

By default, editors spawn the agent and speak ACP over stdio. For a
long-running daemon, use TCP server mode:

```bash
ACP_HARNESS_PROVIDER=ollama \
OLLAMA_BASE_URL=https://ollama.com \
OLLAMA_MODEL=gpt-oss:120b \
OLLAMA_API_KEY=... \
./glm-acp-agent --server --listen 127.0.0.1:8765 --pool-size 4 --wait-size 2
```

Each TCP client connection is one ACP JSON-RPC stream. `--pool-size`
limits active connections. `--wait-size` limits queued connections and
defaults to half of `--pool-size`; connections beyond that queue are
rejected.

You can also expose HTTP compatibility endpoints on a separate listener:

```bash
./glm-acp-agent --http-listen 127.0.0.1:8766
```

Supported routes are `/v1/chat/completions`, `/v1/responses`, and
`/v1/messages` (plus the same paths without `/v1`). They use the same
active provider configuration as ACP server mode.

## Documentation

| Topic | Doc |
| --- | --- |
| Install / build | [docs/install.md](docs/install.md) |
| Environment variables and YAML | [docs/configuration.md](docs/configuration.md) |
| Provider catalogue and how to add one | [docs/providers.md](docs/providers.md) |
| Plugins (sqlite, duckdb, custom) | [docs/plugins.md](docs/plugins.md) |
| Running and writing tests | [docs/testing.md](docs/testing.md) |
| Architecture for AI agents working on the repo | [AGENTS.md](AGENTS.md) |

## Repository layout

```
cmd/glm-acp-agent              entrypoint binary (stdio, TCP server, --setup)
internal/acp                   ACP protocol types + JSON-RPC transport
internal/agent                 ACP Agent, prompt loop, sessions
internal/config                provider templates + layered loader
internal/credentials           XDG credentials + back-compat Z_AI_API_KEY
internal/glm                   legacy GLM HTTP client + type aliases
internal/logger                leveled logger with file sink + rotation
internal/oauth/codex           `oauth:codex` token resolver
internal/plugins               plugin core (sqlite, duckdb)
internal/protocol              image preprocess, session store, system prompt
internal/provider              provider abstraction + adapters
internal/tools                 tool schemas + executor + MCP clients
docs/                          user-facing documentation
```

## Compatibility & back-compat

Existing GLM-only setups continue to work unchanged: `Z_AI_API_KEY`,
`ACP_GLM_MODEL`, `ACP_GLM_BASE_URL`, `ACP_GLM_THINKING`,
`ACP_GLM_MAX_TOKENS`, `ACP_GLM_DEBUG`, `ACP_GLM_SESSION_DIR`, and
`ACP_GLM_AVAILABLE_MODELS` are still honoured. The default
provider is still `glm`.

## Testing

```bash
go test ./...
go vet ./...
```

About two dozen packages, finishing in well under a minute. See
[docs/testing.md](docs/testing.md) for details.

## License

MIT, matching the upstream `glm-acp-agent` package.
