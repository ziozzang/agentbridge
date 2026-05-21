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

The default interactive UI is a Bubble Tea application. It keeps the event loop
inside the TUI runtime: ACP updates are converted into UI events, the viewport
owns transcript scrolling, the bottom composer owns input, and a fixed status
surface shows model/mode/session/context/quota state. User messages, assistant
streams, thinking, tools, status, and approvals are rendered as separate
history cells. Permission prompts use a Codex-style overlay with numbered and
cursor-selectable choices. During a running prompt, `Ctrl-C` first sends
`session/cancel`; pressing it when no prompt is active exits the client.
Additional prompts entered while one is running are queued and can be inspected
with `/queue`. Shell execution remains a client-owned tool.

Use `--plain` to run the minimal line-oriented fallback. This bypasses Bubble
Tea and prints plain text for minimal terminals and debugging.

Use `--json-events` or `--json` when you need protocol-style debugging. It
disables the Bubble Tea renderer, reads one prompt or slash command per stdin
line, and prints the same normalized UI events as newline-delimited JSON,
including user input, assistant deltas, thinking deltas, tool lifecycle updates,
permission requests, status updates, and Lua orchestration events. This is the
stdio-friendly path for reproducing TUI behavior without terminal rendering.

One-shot prompt:

```bash
acp-agent --addr 127.0.0.1:8765 --model codex-agent \
  -p "Inspect the current directory and summarize it."
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
- `-p, --prompt TEXT`: send a single prompt and exit.
- `--show-thinking`: print ACP `agent_thought_chunk` updates to stderr. Hidden
  by default.
- `--plain`: disable the Bubble Tea UI and use the minimal line-oriented
  fallback.
- `--json-events`, `--json`: disable the Bubble Tea UI and print normalized UI
  events as newline-delimited JSON.

Interactive commands:

- `/status`: show address, session id, cwd, model, mode, permission handling,
  thinking display, tool display, and raw update display.
- `/sessions`: list known ACP sessions for the current cwd.
- `/resume SESSION_ID`: resume a persisted session without replaying history.
- `/session-load SESSION_ID`: load a persisted session and replay messages.
- `/save NAME`: save a runtime checkpoint in the current session.
- `/list`: list runtime checkpoints in the current session.
- `/load NAME|ID`: roll back to a runtime checkpoint and bump the session cache
  epoch.
- `/context`: show estimated context usage, context window, compaction
  threshold, target, message count, checkpoint count, and cache epoch.
- `/attach PATH [...]`: extract local files and attach them to the next prompt
  as ACP resource blocks. Markdown, text, JSON/YAML/CSV, source files, and other
  UTF-8 text files are read directly. PDF uses `pdftotext` when installed and
  falls back to printable text extraction.
- `/files`: list queued attachments.
- `/clear-files`: clear queued attachments.
- `/structure`: show the current session id, cwd, model, mode, project context
  file, and queued attachment structure.
- `/lua FILE [args...]`: run a local Lua controller script inside `acp-agent`.
  The CLI also advertises `clientRunLua` and handles server-initiated
  `client/run_lua` JSON-RPC requests. It also advertises a client-owned
  `run_lua` tool and a client-owned `run_command` shell tool; AgentBridge
  exposes them to models as `client__run_lua` and `client__run_command`, then
  routes calls back to the CLI with `client/call_tool`. Exposed Lua API:
  `cli.say(text)`, `cli.status()`, `cli.structure()`, `cli.prompt(text)`,
  `cli.attach(path)`, `cli.files()`, `cli.clear_files()`, and
  `cli.command(line)`. Orchestration helpers are available under `cli.orch`,
  including `plan`, `fetch_next_job`, `run`, `check_status`, `trigger`,
  `steer`, `control_loop`, and `cron`.
- `/goal [status|set TEXT|run|clear]`: use the local Lua goal harness. Goals
  live in the CLI orchestration store, not in the server session; `/goal run`
  sends a goal-specific prompt through the current ACP session.
- `/compact [TARGET_TOKENS]`: manually compact the current transcript. This
  keeps the session going, replaces older turns with a summary when possible,
  and bumps the cache epoch.
- `/new`: create a fresh session in the same cwd.
- `/stop`: send `session/cancel` for the current session. In the current
  terminal client this is also what `Ctrl-C` does while a prompt is active.
- `/queue`: show prompts waiting behind the current active prompt.
- `/subagent [--model MODEL] TASK`: ask the server to run a bounded child
  provider call and return the result to the current session. Subagents inherit
  active skills and tool names, emit tool traces, use the same compaction path
  as the parent loop, retry once after context-overflow compaction, and reject
  recursive nesting beyond the configured depth.
- `/skill list|status|clear|NAME`: list, inspect, clear, or activate markdown
  skills from `.agentbridge/skills` or `$XDG_CONFIG_HOME/agentbridge/skills`.
- `/model [MODEL]`: show or switch model.
- `/mode [MODE]`: show or switch ACP mode.
- `/permission [prompt|allow|reject|cancel]`: show or change permission
  handling.
- `/thinking [on|off|toggle]`: show or change thinking display.
- `/tools [on|off|toggle]`: show or change tool status display.
- `/raw [on|off|toggle]`: show or change raw update display.
- `/help`
- `/exit` or `/quit`

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
