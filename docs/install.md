# Installation

The harness is a single static Go binary.

## Prerequisites

- Go 1.21+
- A POSIX-like environment (Linux, macOS, WSL); Windows works for
  development but plugin tests rely on `os.Symlink` etc.

## Build from source

```bash
git clone https://github.com/ziozzang/glm-acp
cd glm-acp
go build -o glm-acp-agent ./cmd/glm-acp-agent
```

The binary is self-contained — no runtime dependencies, no shared libraries.

## Container

A multi-stage `Dockerfile` ships in the repo root:

```bash
docker build -t acp-harness .
docker run --rm -i \
  -e ACP_HARNESS_API_KEY="$OPENAI_API_KEY" \
  -e ACP_HARNESS_PROVIDER=openai \
  -v "$PWD":/workspace -w /workspace \
  acp-harness
```

Mount a volume at `/home/agent/.local/state` to persist sessions across
container restarts.

## Editor integration

Any ACP-aware editor can spawn the harness. The most common is **Zed**:
configure your assistant settings to invoke `glm-acp-agent` as the agent
command. See [docs/providers.md](./providers.md) for how to point it at a
specific upstream LLM.

## TCP server mode

For daemon-style deployments, run the harness as a long-lived TCP ACP
server instead of spawning one process per client:

```bash
ACP_HARNESS_PROVIDER=ollama \
OLLAMA_BASE_URL=https://ollama.com \
OLLAMA_MODEL=gpt-oss:120b \
./glm-acp-agent --server --listen 127.0.0.1:8765 --pool-size 4 --wait-size 2
```

The TCP stream uses the same newline-delimited JSON-RPC ACP protocol as
stdio. Each accepted connection gets an independent agent instance.
`--pool-size` caps active connections. `--wait-size` caps queued
connections and defaults to half of `--pool-size`; once both active and
queued capacity are full, new TCP connections are closed.

## HTTP compatibility API

If you need an HTTP API instead of ACP JSON-RPC, start a separate HTTP
listener:

```bash
ACP_HARNESS_PROVIDER=ollama \
OLLAMA_BASE_URL=https://ollama.com \
OLLAMA_MODEL=gpt-oss:120b \
./glm-acp-agent --http-listen 127.0.0.1:8766
```

The HTTP listener supports:

- `POST /v1/chat/completions`
- `POST /v1/responses`
- `GET /v1/responses/{id}`
- `POST /v1/messages`
- `POST /v1/a2a/rpc`
- `POST /v1/mcp`
- `POST /v1/agui/run`
- `GET /openapi.json`
- `GET /swagger`
- `GET /metrics`

The same paths are also accepted without the `/v1` prefix. The endpoints
use the same configured provider as ACP mode and return OpenAI/Anthropic
compatible response shapes.

`/v1/responses` accepts `previous_response_id`, `instructions`,
`parallel_tool_calls`, `max_tool_calls`, `prompt_cache_key`, and
`prompt_cache_retention` for OpenAI Responses/Agents-style clients. Stored
responses can be retrieved with `GET /v1/responses/{id}`.

`/v1/mcp` is a Streamable HTTP JSON-RPC endpoint with `initialize`,
`tools/list`, `tools/call`, `resources/list`, and `prompts/list`. The
initial tool set exposes a `chat` tool backed by the configured provider.

`/v1/agui/run` emits AG-UI-compatible SSE text message lifecycle events:
`RUN_STARTED`, `TEXT_MESSAGE_START`, `TEXT_MESSAGE_CONTENT`,
`TEXT_MESSAGE_END`, and `RUN_FINISHED`.

`/openapi.json` and `/v1/openapi.json` return an OpenAPI 3.1.1 document for
Swagger/OpenAPI tooling, and `/swagger` serves a Swagger UI page for it.
`/metrics` and `/metric` expose Prometheus text format counters and gauges
for request totals, failures, in-flight requests, durations, and per-route
status counts.

## gRPC compatibility API

For long-lived service clients that benefit from HTTP/2 multiplexing and
server streaming, start the gRPC listener:

```bash
ACP_HARNESS_PROVIDER=glm ACP_GLM_MODEL=glm-5.1 \
./glm-acp-agent --grpc-listen 127.0.0.1:8767
```

The service name is `glm_acp.v1.AgentService`:

- `Chat`
- `ChatStream`
- `A2A`
- `A2AStream`

All methods use `google.protobuf.Struct` for request and response payloads.
A minimal chat request can be either `{"input":"hi","model":"glm-5.1"}` or
`{"messages":[{"role":"user","content":"hi"}],"model":"glm-5.1"}`.
The standard `grpc.health.v1.Health` service is also registered.

## First-time setup

```bash
# Pick a provider via env (default is "glm" for back-compat).
export ACP_HARNESS_PROVIDER=openai
export ACP_HARNESS_API_KEY=sk-...

# Or store a key in the credentials file (XDG-aware, mode 0600).
./glm-acp-agent --setup
```

`--setup` stores a key at `$XDG_CONFIG_HOME/glm-acp-agent/credentials.json`
(or `~/.config/glm-acp-agent/credentials.json`) and is back-compat with
the original GLM tool — handy when you only need GLM. For non-GLM
providers, prefer `ACP_HARNESS_*_API_KEY` env vars.
