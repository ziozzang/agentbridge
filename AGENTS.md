# AGENTS.md — guide for AI agents working in this repository

This file is the canonical orientation document for autonomous coding
agents (Copilot, Cursor, Claude Code, …) modifying this codebase. Keep it
current when you change architecture, conventions, or build commands.

---

## What this project is

`agentbridge` is a **generic, high-quality ACP (Agent Client Protocol) harness**
written in Go. The original code was a single-purpose adapter for Z.AI's
GLM models; it has since been generalised into a provider-agnostic harness
with a plugin system. The repository name and the binary name (`agentbridge`)
are kept for back-compat — *but the harness itself is provider-neutral*.

It speaks ACP over stdio (JSON-RPC 2.0 / newline-delimited JSON), ACP over TCP,
and HTTP compatibility APIs. It brokers between ACP-aware clients, OpenAI-style
HTTP clients, A2A/MCP clients, and a **provider** of the user's choice.

## High-level architecture

```
ACP stdio/TCP ─► internal/acp ─► internal/agent ─┬─ built-in agent loop
HTTP/A2A/MCP ─► internal/httpcompat ─────────────┤
                                                  ▼
                                           internal/provider
                                                  │
                        ┌─────────────────────────┴─────────────────────────┐
                        ▼                                                   ▼
               standard LLM providers                           native-agent providers
         openai-chat, responses, glm, ...                        codex-app-server, future ACP/Claude
```

Important boundaries:

- `internal/provider` defines the neutral `Provider` interface and the
  shared `Message`/`Chunk`/`ToolCall` types. **Every adapter translates
  between this neutral shape and an upstream API.**
- Provider implementations are either standard LLM providers or
  provider-native agent providers. See [docs/architecture.md](docs/architecture.md)
  before changing this boundary.
- `internal/provider/glm` contains the GLM/Z.AI client, compaction helpers,
  context-overflow error type, and provider-neutral type aliases used by the
  agent. `internal/provider/glm/preset` registers the `"glm"` provider kind.
- `internal/config` loads layered provider config. Lowest priority is the
  embedded `providers.yaml`; XDG file and `AGENTBRIDGE_PROVIDERS_FILE`
  override on top; per-provider env vars (`AGENTBRIDGE_<NAME>_API_KEY`)
  are applied last.
- `internal/plugins` is the extension surface. Activation is
  `AGENTBRIDGE_PLUGINS=sqlite,duckdb`. Plugin tools are exposed under
  `plugin__<name>__<tool>` and surfaced through the executor.
- `internal/oauth/codex` resolves `oauth:codex` API keys.
- `internal/observability` stores process-local runtime status for
  `/v1/providers/status` and `/ui/`.

## Provider abstraction

```go
type Provider interface {
    Name() string
    Kind() string
    AvailableModels() []ModelInfo
    DefaultModel() string
    ContextWindow(model string) int
    StreamChat(ctx, []Message, StreamOptions) (<-chan Chunk, <-chan error)
}
```

Optional provider interfaces matter:

- `ConversationCompactor` enables provider-native compaction.
- `IntentionProber` enables experimental logprob intention probing.
- `StreamOptionsSanitizer` and `CompactOptionsSanitizer` let providers drop or
  reinterpret unsupported request options.
- `NativeAgentProvider` marks providers that already own an agentic loop.

The safety pipeline wrapper in `internal/provider/pipeline` must preserve these
optional interfaces. If you add another optional provider capability, update the
wrapper too.

Registration uses an init() function:

```go
func init() {
    provider.Register("openai-chat", New)
}
```

Errors that indicate a context-window overflow must wrap
`*provider.ContextOverflowError` — the agent uses this to trigger
emergency compaction and a single retry.

## Agent-loop architecture

AgentBridge has two execution modes. Do not blur them.

### Standard LLM providers

Standard providers only expose a model API. ACP requests for these providers
must pass through AgentBridge's built-in harness:

1. Build a system prompt from cwd, project context, tools, and profile.
2. Stream the model response.
3. Execute tool calls through `internal/tools/executor`.
4. Append tool results and continue until stop or `MaxTurns`.
5. Use configured compaction on thresholds or context-overflow errors.

HTTP uses this loop only when the caller opts in with `agent:<model>` or
metadata such as `{"agent": true}`. Plain `/v1/chat/completions` stays a single
provider call.

### Runtime commands and skills

ACP text prompts beginning with runtime commands are intercepted before the
provider call, including provider-native sessions. Keep these commands out of
the LLM transcript.

- `/btw mark|list|back` is the server-side checkpoint namespace. The terminal
  client exposes `/save`, `/list`, and `/load` aliases for the same operations.
- Rolling back a checkpoint must truncate only AgentBridge's local transcript,
  restore active skills, and increment `cacheEpoch` so cache-aware providers do
  not reuse stale prefix assumptions.
- `/context` and `/compact` are continuation controls, not checkpoint controls.
  `/context` reports estimated usage; `/compact` should reuse the same
  provider-native/summary/prune path as automatic compaction and bump
  `cacheEpoch` only when it actually rewrites the transcript.
- `/new` is a client/session command. `/stop` maps to ACP `session/cancel`;
  making it interrupt a running terminal prompt requires a concurrent input
  loop, not only a server handler.
- `/subagent [--model MODEL] TASK` is the server-side bounded child-call
  primitive. It uses the parent tool/permission path, inherits active skills,
  emits tool traces, runs proactive/overflow compaction with one overflow
  retry, and rejects recursive nesting beyond the depth limit. It should stay
  small and avoid mutating parent transcript except through returned
  runtime-command text and structured ACP updates.
- `/attach` is a terminal-client feature. It extracts local files through
  `internal/harness/filecontext` and sends them as ACP `resource` blocks on the
  next prompt. Keep extraction bounded and text-only unless a provider-specific
  binary upload path is deliberately added.
- `/structure` is a local inspection command for session, project context, and
  queued attachments. It should not mutate state.
- Lua orchestration belongs in `cmd/acp-agent`, not in `internal/agent`.
  `acp-agent` exposes `/lua FILE` and handles server-initiated
  `client/run_lua`. Keep the Lua API restricted to CLI flow/text/attachment
  control (`cli.prompt`, `cli.attach`, `cli.command`, `cli.status`, etc.).
  `/goal` is implemented as a local Lua harness command over `cli.goal`; do not
  add a canonical server-side goal field to ACP sessions unless the protocol
  explicitly grows one.
- Client-owned tools are advertised through `initialize.clientCapabilities`
  as `clientTools` and surfaced to the model under `client__<name>`. Executor
  must route those calls back to the ACP client with `client/call_tool`. This is
  separate from AgentBridge-owned server tools.
- Shell commands and shell scripts are client-owned. `acp-agent` advertises
  `run_command`, which the model sees as `client__run_command`; do not add
  server-owned shell execution back into `internal/tools/executor`.
- `/skill list|status|clear|NAME` manages markdown skills from
  `<cwd>/.agentbridge/skills` and `$XDG_CONFIG_HOME/agentbridge/skills`.
  Active skills are persisted on the session and injected into the standard
  loop's system prompt. Native-agent providers still accept skill commands, but
  skill injection into their native transport must be implemented per provider.

### Provider-native agent providers

Providers like `codex-app-server`, Claude SDK, and ACP-backed agents can own
their own session runtime, tool execution, permissions, and compaction. These
must implement `provider.NativeAgentProvider` and bypass the built-in ACP
harness.

Rules for native-agent providers:

- Session identity is always anchored by AgentBridge `sessionId`.
- ACP mode must be `provider_native`.
- Do not connect session-scoped MCP or run local tool execution unless the
  provider specifically delegates that responsibility back to AgentBridge.
- Keep a lightweight local transcript for UI/session persistence, but treat the
  provider session as the source of truth.
- Forward only options the native transport supports. Use option sanitizer
  interfaces for cache/reasoning/compaction exceptions.

For `codex-app-server`, `prompt_cache_key` may be used as local session
affinity for HTTP, but `prompt_cache_retention` is dropped because it is not an
app-server wire option.

### How to add a new provider

1. Create `internal/provider/<kind>/<kind>.go`.
2. Implement the `Provider` interface end-to-end (translate Messages,
   parse streaming responses, surface Usage when available).
3. Register it in `init()`.
4. Add a template entry in `internal/config/providers.yaml`.
5. Import the package for its side-effect from `internal/agent/agent.go`.
6. Import it from `internal/httpcompat/server.go` when HTTP surfaces need it.
7. For provider-native agents, implement `NativeAgentProvider`, document the
   native session behavior, and add bypass tests in `internal/agent`.
8. Write tests using `httptest.Server` that drive a real SSE/NDJSON wire
   format. Follow the patterns in
   `internal/provider/openaichat/openaichat_test.go`.

For process-backed native providers, prefer a small helper-process test using
`os.Args[0]` over shelling out to a real CLI. Real CLI tests must be opt-in.

## Plugin system

```go
type Plugin interface {
    Name() string
    Tools() []ToolDef
    Call(ctx, tool string, args json.RawMessage) (string, error)
}
```

Activation: `AGENTBRIDGE_PLUGINS=sqlite[,duckdb,...]`.

Plugin tools are auto-prefixed `plugin__<name>__<tool>`. The agent's
`availableTools()` appends them and the executor dispatches via the
`PluginDispatcher` interface (which lives in `internal/tools/executor`
to avoid an import cycle).

### How to add a new plugin

1. Create `internal/plugins/<name>/<name>.go`.
2. Call `plugins.Register("<name>", func() plugins.Plugin { … })` from
   `init()`.
3. Implement `Name`, `Tools`, `Call`. Keep arguments JSON-schema-compatible
   for `Tools()` so the model can call them correctly.
4. Side-effect-import from `internal/agent/agent.go`.
5. Add tests covering load, dispatch, and failure paths.

## Configuration model

User-visible knobs are *all* environment variables, organised in tiers:

| Tier | Examples |
| --- | --- |
| Top-level | `AGENTBRIDGE_PROVIDER`, `AGENTBRIDGE_MODEL`, `AGENTBRIDGE_API_KEY`, `AGENTBRIDGE_BASE_URL` |
| Per-provider override | `AGENTBRIDGE_OPENAI_API_KEY`, `AGENTBRIDGE_GLM_BASE_URL`, … |
| Plugin activation | `AGENTBRIDGE_PLUGINS`, `AGENTBRIDGE_SQLITE_DIRS`, … |
| Logging | `AGENTBRIDGE_LOG_LEVEL`, `AGENTBRIDGE_LOG_FILE`, `AGENTBRIDGE_LOG_MAX_BYTES`, `AGENTBRIDGE_LOG_MAX_FILES` |
| Back-compat | `Z_AI_API_KEY`, `ACP_GLM_MODEL`, `ACP_GLM_DEBUG`, … (still work) |

User override file (optional): `$XDG_CONFIG_HOME/agentbridge/providers.yaml`
or whatever `AGENTBRIDGE_PROVIDERS_FILE` points at.

## Logging

`internal/logger` is the single place to emit diagnostic output. Use:

```go
logger.Tracef("low-level stream: %s", body)
logger.Debugf("attempt %d failed: %v", i, err)
logger.Infof("active provider: %s", name)
logger.Warnf("config: ignoring unknown field %q", f)
logger.Errorf("session/load: %v", err)
```

Never write directly to `os.Stderr`/`fmt.Println` inside agent code — the
stdio channel must remain pure JSON-RPC.

## HTTP status UI

`/v1/providers/status` and `/ui/` are read-only operational views. They should
not mutate runtime config. Put shared state in `internal/observability`; do not
make protocol packages reach into each other's internal maps.

The UI is intentionally simple, embedded HTML. Keep it dependency-free and
focused on live request/session/provider state.

## Conventions

- **No new top-level dependencies** without strong justification; the
  harness aims to stay a small static binary. Pure-Go libraries are
  strongly preferred (e.g. `modernc.org/sqlite` over CGo).
- **Errors**: return early. Wrap with context using `fmt.Errorf("%s: %w", …)`.
- **Concurrency**: every streaming function returns a `<-chan Chunk` and a
  `<-chan error` and the goroutine that produces them must close both
  channels exactly once.
- **Tests**: every adapter and plugin gets at least one end-to-end test
  using `httptest.Server` (for network) or a temp directory (for files).
- **Resource use**: this harness is meant to run alongside a code editor.
  Avoid any unbounded queues, polling loops, or per-token allocations
  that would visibly impact CPU/RAM.
- **Doc comments**: every exported type and func has a doc comment that
  starts with the identifier name. `go vet ./...` enforces parts of this.

## Build / test / lint

```bash
go build ./...
go test ./...               # ~24 packages, ~5s on a laptop
go vet ./...
```

At the time of writing, `internal/plugins/xai` has an environment-sensitive
mock/OAuth test that can fail with EOF when local xAI OAuth state leaks into
the test. When this happens, run and report the targeted package set relevant
to your change, then call out the residual xAI test failure explicitly.

Single-package iteration:

```bash
go test ./internal/provider/openaichat/...
```

Targeted CI debugging:

```bash
AGENTBRIDGE_LOG_LEVEL=trace AGENTBRIDGE_LOG_FILE=/tmp/harness.log ./agentbridge
```

## Repository layout

```
cmd/agentbridge              # entrypoint binary (stdio + --setup)
internal/acp                   # ACP protocol types + JSON-RPC ndjson transport
internal/agent                 # ACP Agent, prompt loop, sessions
internal/config                # embedded providers.yaml + layered loader
internal/credentials           # XDG credentials + Z_AI_API_KEY back-compat
internal/provider/glm          # GLM/Z.AI client, compaction helpers, preset
internal/logger                # leveled logger with file sink + rotation
internal/oauth/codex           # `oauth:codex` token resolver
internal/observability         # process-local status snapshot for /ui and status API
internal/plugins               # plugin core (sqlite, duckdb)
internal/protocol/imagepre     # image content-block preprocessor
internal/protocol/sessionstore # per-session JSON persistence
internal/protocol/systemprompt # system prompt builder + SOUL.md/AGENTS.md/CLAUDE.md loader
internal/provider              # provider abstraction + concrete adapters
internal/provider/codexnative  # local `codex app-server` native-agent provider
internal/tools/definitions     # OpenAI function-calling tool schemas
internal/tools/executor        # tool dispatcher (file/MCP/plugin/client-owned routing)
internal/tools/sessionmcp      # session-scoped MCP servers
internal/tools/visionmcp       # vision MCP client
internal/tools/zaimcp          # Z.AI hosted MCP (web_search, web_reader)
docs/                          # user-facing docs (install, configuration, …)
```

## When in doubt

- Smaller, surgical changes always beat broad rewrites.
- Tests that previously passed must continue to pass; if you must change
  test expectations, mention the reason in the PR description.
- The user-facing tone of the project is "high-quality production harness".
  Read the existing godoc and follow the same level of polish.
