# Installation

AgentBridge builds as a single Go binary.
The repository also includes `acp-agent`, a separate terminal ACP client.

## Prerequisites

- Go 1.25 or newer
- Linux, macOS, WSL, or another POSIX-like environment

## Build

```bash
git clone https://github.com/ziozzang/agentbridge
cd agentbridge
go build -o agentbridge ./cmd/agentbridge
go build -o acp-agent ./cmd/acp-agent
```

## Container

```bash
docker build -t agentbridge .
docker run --rm -i \
  -e AGENTBRIDGE_PROVIDER=openai \
  -e AGENTBRIDGE_API_KEY="$OPENAI_API_KEY" \
  -v "$PWD":/workspace -w /workspace \
  agentbridge
```

Mount `/home/agent/.local/state` if you want session files to survive
container restarts.

## Editor Mode

ACP-aware editors can spawn AgentBridge directly:

```bash
agentbridge
```

The process speaks ACP JSON-RPC over stdio.

## TCP ACP Server

```bash
agentbridge --server --listen 127.0.0.1:8765 --pool-size 6 --wait-size 3
```

Each TCP connection is an independent ACP JSON-RPC stream. `--pool-size`
limits active connections. `--wait-size` limits queued connections and
defaults to half of `--pool-size`.

## Terminal ACP Client

`acp-agent` connects to the TCP ACP server and gives a Claude CLI-like terminal
session. It is a separate component from the AgentBridge server.

```bash
agentbridge --server --listen 127.0.0.1:8765
acp-agent --addr 127.0.0.1:8765 --model glm-5.1
```

One-shot prompt:

```bash
acp-agent --addr 127.0.0.1:8765 --model codex-agent \
  --prompt "Inspect the current directory and summarize it."
```

Useful flags:

- `--cwd DIR`: working directory for the ACP session.
- `--model MODEL`: model or agent profile selected through `session/set_model`.
- `--mode MODE`: ACP permission mode such as `default`, `accept_edits`, or
  `bypass_permissions`.
- `--permission prompt|allow|reject|cancel`: how the terminal answers
  `session/request_permission`.
- `--yolo`: shorthand for `--mode bypass_permissions --permission allow`.
- `--read-only`: shorthand for `--mode default --permission reject`.
- `--show-thinking`: print ACP `agent_thought_chunk` updates to stderr. Hidden
  by default.

Interactive commands:

- `/model MODEL`
- `/mode MODE`
- `/exit`

## HTTP Compatibility Server

```bash
AGENTBRIDGE_PROVIDER=glm AGENTBRIDGE_GLM_MODEL=glm-5.1 \
agentbridge --http-listen 127.0.0.1:8766
```

Routes:

- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/responses/compact`
- `GET /v1/responses/{id}`
- `POST /v1/messages`
- `POST /v1/embeddings`
- `POST /v1/rerank`
- `GET /v1/models`
- `GET /v1/providers/status`
- `POST /v1/a2a/rpc`
- `GET /.well-known/agent-card.json`
- `POST /v1/mcp`
- `GET /v1/mcp/catalog`
- `GET /v1/tool-catalog`
- `POST /v1/tools/{tool-name}`
- `POST /v1/agui/run`
- `GET /openapi.json`
- `GET /swagger`
- `GET /ui/`
- `GET /metrics`
- `GET /health`

Most routes are also accepted without `/v1`.

Server flags can also be placed in `$XDG_CONFIG_HOME/agentbridge/config.yaml`.
CLI flags take precedence:

```yaml
server:
  enabled: true
  listen: 127.0.0.1:8765
  pool_size: 6
  wait_size: 3
  http_listen: 127.0.0.1:8766
  grpc_listen: 127.0.0.1:8767
```

## gRPC Compatibility Server

```bash
agentbridge --grpc-listen 127.0.0.1:8767
```

Service: `agentbridge.v1.AgentService`

- `Chat`
- `ChatStream`
- `A2A`
- `A2AStream`

The service uses `google.protobuf.Struct` for request and response payloads.
The standard `grpc.health.v1.Health` service is also registered.

## First-Time GLM Setup

```bash
agentbridge --setup
```

This stores a GLM/Z.AI key at
`$XDG_CONFIG_HOME/agentbridge/credentials.json` or
`~/.config/agentbridge/credentials.json`. The old
`glm-acp-agent/credentials.json` path is still read as a fallback.
