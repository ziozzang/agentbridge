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

It speaks ACP over stdio (JSON-RPC 2.0 / newline-delimited JSON) and
brokers between any ACP-aware client (Zed, the ACP CLI, …) and an LLM
**provider** of the user's choice.

## High-level architecture

```
                ┌───────────────────────────┐
   stdio  ────► │  internal/acp  (JSON-RPC) │ ◄──── ACP client (Zed, …)
                └────────────┬──────────────┘
                             │
                             ▼
                ┌───────────────────────────┐
                │  internal/agent           │  per-session prompt loop,
                │  (Agent)                  │  history, modes, tool dispatch
                └───┬───────────┬───────┬───┘
                    │           │       │
        ┌───────────▼───┐  ┌────▼───────────────┐  ┌──▼─────────────────┐
        │ internal/     │  │ internal/tools/    │  │ internal/plugins/  │
        │   provider/   │  │   executor         │  │   sqlite, duckdb,  │
        │ (LLM adapters)│  │ (file/shell/MCP)   │  │   …                │
        └───┬───────────┘  └────────────────────┘  └────────────────────┘
            │
   ┌────────┴──────────────┬────────────┬────────────┬──────────┐
   ▼                       ▼            ▼            ▼          ▼
 openai-chat       openai-responses  anthropic     ollama      glm
 (also litellm,    (Codex OAuth     (Messages    (native      (thinking
  openrouter,       compatible)      API)         /api/chat)  flag)
  ollama-openai)
```

Important boundaries:

- `internal/provider` defines the neutral `Provider` interface and the
  shared `Message`/`Chunk`/`ToolCall` types. **Every adapter translates
  between this neutral shape and an upstream API.**
- `internal/glm` is *kept for back-compat*. Its public types are now
  type aliases (`type Message = provider.Message`) so the agent's existing
  literal usage (`glm.Message{Role: "user", …}`) still compiles. The
  legacy HTTP client (`*glm.Client`) is still tested but no longer the
  default runtime path; `agent.Agent.Provider` is the new field.
- `internal/config` loads layered provider config. Lowest priority is the
  embedded `providers.yaml`; XDG file and `AGENTBRIDGE_PROVIDERS_FILE`
  override on top; per-provider env vars (`AGENTBRIDGE_<NAME>_API_KEY`)
  are applied last.
- `internal/plugins` is the extension surface. Activation is
  `AGENTBRIDGE_PLUGINS=sqlite,duckdb`. Plugin tools are exposed under
  `plugin__<name>__<tool>` and surfaced through the executor.
- `internal/oauth/codex` resolves `oauth:codex` API keys.

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

Registration uses an init() function:

```go
func init() {
    provider.Register("openai-chat", New)
}
```

Errors that indicate a context-window overflow must wrap
`*provider.ContextOverflowError` — the agent uses this to trigger
emergency compaction and a single retry.

### How to add a new provider

1. Create `internal/provider/<kind>/<kind>.go`.
2. Implement the `Provider` interface end-to-end (translate Messages,
   parse streaming responses, surface Usage when available).
3. Register it in `init()`.
4. Add a template entry in `internal/config/providers.yaml`.
5. Import the package for its side-effect from `internal/agent/agent.go`.
6. Write tests using `httptest.Server` that drive a real SSE/NDJSON wire
   format. Follow the patterns in
   `internal/provider/openaichat/openaichat_test.go`.

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
internal/glm                   # legacy GLM HTTP client + type aliases
internal/logger                # leveled logger with file sink + rotation
internal/oauth/codex           # `oauth:codex` token resolver
internal/plugins               # plugin core (sqlite, duckdb)
internal/protocol/imagepre     # image content-block preprocessor
internal/protocol/sessionstore # per-session JSON persistence
internal/protocol/systemprompt # system prompt builder + AGENTS.md loader
internal/provider              # provider abstraction + concrete adapters
internal/tools/definitions     # OpenAI function-calling tool schemas
internal/tools/executor        # tool dispatcher (file/shell/MCP/plugin)
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
