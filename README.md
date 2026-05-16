# glm-acp

`glm-acp` is a faithful Go port of the [`glm-acp-agent`](https://www.npmjs.com/package/glm-acp-agent) npm package. It is an [Agent Client Protocol](https://github.com/zed-industries/agent-client-protocol) (ACP) coding agent that drives Z.AI / Zhipu AI **GLM** models (GLM-5.1, GLM-4.7, GLM-4.5-Air, …) over stdio so any ACP-aware editor (e.g. Zed) can use them as a chat backend.

The behavior, system prompt, tool surface, and on-the-wire protocol mirror the reference TypeScript implementation while gaining Go's static binary, easier container deployment, and high concurrency.

## Features

- Speaks ACP `2025-…` (protocol version 1) over JSON-RPC 2.0 / newline-delimited JSON on stdio.
- Streams GLM chat completions including the GLM-specific `thinking` (reasoning) deltas.
- Built-in tools: `read_file`, `write_file`, `list_files`, `run_command`, `web_search`, `web_reader`, `image_analysis`.
  - File writes and shell commands always ask the ACP client for permission first.
  - `web_search` / `web_reader` call Z.AI's hosted MCP endpoints with session caching.
- Persists every session as `~/.local/state/glm-acp-agent/sessions/<id>.json` so `session/list`, `session/load`, `session/resume`, `session/fork` all work.
- API key resolution: `Z_AI_API_KEY` env var > `~/.config/glm-acp-agent/credentials.json` (written by `--setup`).
- Container-first: ships with a multi-stage `Dockerfile`.

## Install / build

```bash
go build -o glm-acp-agent ./cmd/glm-acp-agent
```

## Run

```bash
# Bootstrap the API key once, then start the agent on stdio.
./glm-acp-agent --setup
./glm-acp-agent

# Or pass everything via the environment (ideal for containers / CI).
Z_AI_API_KEY=sk-... ACP_GLM_MODEL=glm-5.1 ./glm-acp-agent
```

The agent talks ACP on stdio; an editor like Zed will spawn it under the hood. There is no HTTP server.

## Container

```bash
docker build -t glm-acp-agent .
docker run --rm -i \
  -e Z_AI_API_KEY="$Z_AI_API_KEY" \
  -v "$PWD":/workspace -w /workspace \
  glm-acp-agent
```

Mount a volume at `/home/agent/.local/state` to persist sessions across container restarts.

## Environment variables

| Variable | Purpose |
| --- | --- |
| `Z_AI_API_KEY` | Required for chat. Z.AI Coding Plan API key. |
| `ACP_GLM_MODEL` | Default model id (e.g. `glm-5.1`). |
| `ACP_GLM_AVAILABLE_MODELS` | Comma-separated whitelist advertised to clients. |
| `ACP_GLM_BASE_URL` | Override the Z.AI Coding Plan base URL. |
| `ACP_GLM_MAX_TOKENS` | Per-call max output tokens (default 8192). |
| `ACP_GLM_THINKING` | Force GLM thinking mode on/off (`true`/`false`). |
| `ACP_GLM_DEBUG` | `true`/`1` enables verbose stderr debug logging. |
| `ACP_GLM_SESSION_DIR` | Directory for persisted session JSON files. |
| `XDG_CONFIG_HOME` | Used to locate the credentials file. |

## Layout

```
cmd/glm-acp-agent          # main entry point (stdio + --setup + --version)
internal/acp               # ACP types and JSON-RPC ndjson stdio transport
internal/agent             # GLM-backed Agent (initialize, sessions, prompt loop)
internal/credentials       # XDG credentials + Z_AI_API_KEY resolution
internal/glm               # OpenAI-compatible streaming GLM client (SSE + thinking)
internal/logger            # Stderr leveled logger with secret masking
internal/protocol/imagepre        # Image content-block preprocessor
internal/protocol/sessionstore    # Per-session JSON persistence (XDG_STATE_HOME)
internal/protocol/systemprompt    # System prompt builder + AGENTS.md/CLAUDE.md
internal/tools/definitions # OpenAI function-calling tool schemas
internal/tools/executor    # Tool dispatcher (file/shell + Z.AI MCP)
internal/tools/zaimcp      # Z.AI MCP HTTP+SSE client (web_search, web_reader)
```

## Test

The whole port is TDD-driven. Run:

```bash
go test ./...
```

## License

MIT, matching the upstream `glm-acp-agent` package.
