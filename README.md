# AgentBridge

AgentBridge is a provider-neutral protocol bridge and compatibility gateway
for AI agents. It exposes ACP, A2A, MCP Streamable HTTP, OpenAI-compatible
HTTP APIs, Anthropic-compatible Messages, AG-UI, and gRPC over one shared
provider backend.

The project started as `glm-acp`. It has been renamed because the runtime is
no longer GLM-specific and no longer limited to ACP. Legacy environment
variables and on-disk paths are still accepted where practical.

한국어 문서는 [README.ko.md](README.ko.md)를 보세요.

## Highlights

- **Many protocols, one backend**: ACP stdio/TCP, A2A JSON-RPC, MCP
  Streamable HTTP, OpenAI Chat Completions, OpenAI Responses,
  Anthropic Messages, AG-UI SSE, and gRPC.
- **Many providers**: GLM/Z.AI, OpenAI, OpenAI Responses, Anthropic,
  Ollama, OpenRouter, LiteLLM-compatible gateways, Codex OAuth, and
  Claude Code CLI.
- **Long-running server modes**: bounded TCP ACP pool, HTTP compatibility
  listener, and gRPC listener.
- **Observability**: structured leveled logs with rotation plus Prometheus
  metrics on `/metrics`.
- **OpenAPI and Swagger**: `/openapi.json`, `/v1/openapi.json`, and
  `/swagger`.
- **Plugins**: optional SQLite and DuckDB extension surface.
- **Backwards compatibility**: `ACP_HARNESS_*`, `ACP_GLM_*`,
  `Z_AI_API_KEY`, and old credential/session paths remain supported.

## Quick Start

```bash
go build -o agentbridge ./cmd/agentbridge
```

Run as an ACP stdio agent:

```bash
AGENTBRIDGE_PROVIDER=openai \
AGENTBRIDGE_API_KEY="$OPENAI_API_KEY" \
AGENTBRIDGE_MODEL=gpt-4.1-mini \
./agentbridge
```

Run the HTTP compatibility gateway:

```bash
AGENTBRIDGE_PROVIDER=glm \
Z_AI_API_KEY="$Z_AI_API_KEY" \
AGENTBRIDGE_GLM_MODEL=glm-5.1 \
./agentbridge --http-listen 127.0.0.1:8766
```

Run ACP TCP, HTTP, and gRPC together:

```bash
./agentbridge \
  --server --listen 127.0.0.1:8765 --pool-size 6 --wait-size 3 \
  --http-listen 127.0.0.1:8766 \
  --grpc-listen 127.0.0.1:8767
```

## HTTP Routes

- `POST /v1/chat/completions`
- `POST /v1/responses`
- `GET /v1/responses/{id}`
- `POST /v1/messages`
- `POST /v1/a2a/rpc`
- `GET /.well-known/agent-card.json`
- `POST /v1/mcp`
- `POST /v1/agui/run`
- `GET /openapi.json`
- `GET /swagger`
- `GET /metrics`
- `GET /health`

Most compatibility routes are also accepted without the `/v1` prefix.

## gRPC

The gRPC service is `agentbridge.v1.AgentService`.

- `Chat`
- `ChatStream`
- `A2A`
- `A2AStream`

Requests and responses use `google.protobuf.Struct`, so clients can call the
service without generated project-specific stubs. The standard
`grpc.health.v1.Health` service is also registered.

## Configuration

Preferred environment variables use the `AGENTBRIDGE_*` prefix:

| Variable | Purpose |
| --- | --- |
| `AGENTBRIDGE_PROVIDER` | Active provider. Default: `glm`. |
| `AGENTBRIDGE_MODEL` | Default model override. |
| `AGENTBRIDGE_API_KEY` | API key for the active provider. |
| `AGENTBRIDGE_BASE_URL` | Base URL override. |
| `AGENTBRIDGE_CONFIG_FILE` | Full config YAML override. |
| `AGENTBRIDGE_PROVIDERS_FILE` | Provider YAML override. |
| `AGENTBRIDGE_ROUTER_FILE` | Model-router route YAML/JSON override. |
| `AGENTBRIDGE_PLUGINS` | Comma-separated plugins, e.g. `sqlite`. |
| `AGENTBRIDGE_LOG_LEVEL` | `trace`, `debug`, `info`, `warn`, `error`, or `off`. |
| `AGENTBRIDGE_LOG_FILE` | Optional log file path. |
| `AGENTBRIDGE_SESSION_DIR` | Session persistence directory. |

Legacy aliases such as `ACP_HARNESS_PROVIDER`, `ACP_HARNESS_API_KEY`,
`ACP_GLM_MODEL`, and `ACP_GLM_SESSION_DIR` remain accepted.

## Documentation

| English | Korean |
| --- | --- |
| [Install](docs/install.md) | [설치](docs/ko/install.md) |
| [Configuration](docs/configuration.md) | [설정](docs/ko/configuration.md) |
| [Providers](docs/providers.md) | [프로바이더](docs/ko/providers.md) |
| [Plugins](docs/plugins.md) | [플러그인](docs/ko/plugins.md) |
| [Testing](docs/testing.md) | [테스트](docs/ko/testing.md) |

## Repository Layout

```text
cmd/agentbridge                 entrypoint binary
internal/acp                    ACP protocol and JSON-RPC transport
internal/agent                  ACP agent, sessions, prompt loop
internal/config                 provider templates and config loader
internal/grpccompat             gRPC compatibility service
internal/httpcompat             HTTP, A2A, MCP, AG-UI, OpenAPI, metrics
internal/provider               provider abstraction and adapters
internal/plugins                optional tool plugins
internal/tools                  built-in and MCP tool clients
docs/                           English documentation
docs/ko/                        Korean documentation
```

## Testing

```bash
go test ./...
go vet ./...
go build -o agentbridge ./cmd/agentbridge
```

## License

MIT.
