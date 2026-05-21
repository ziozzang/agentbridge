# Architecture

AgentBridge is a protocol bridge with one provider abstraction underneath ACP,
A2A, MCP Streamable HTTP, AG-UI, gRPC, OpenAI-compatible HTTP, and
Anthropic-compatible HTTP surfaces.

## Runtime Shape

The process has three main layers:

1. Protocol adapters parse wire formats and normalize requests.
2. The provider layer translates normalized messages to an upstream model or
   native agent runtime.
3. Shared safety and runtime services wrap providers for PII masking, response
   sanitization, compaction, observability, and status reporting.

The central provider contract is `internal/provider.Provider`:

- `StreamChat` streams normalized assistant chunks.
- `AvailableModels`, `DefaultModel`, and `ContextWindow` feed model discovery
  and context management.
- Optional interfaces add native compaction, intention probing, option
  sanitation, and provider-native agent-loop detection.

## Protocol Surfaces

ACP over stdio or TCP is handled by `internal/acp` and `internal/agent`.
The agent owns ACP sessions, model/mode state, persistence, and notifications.

HTTP compatibility is handled by `internal/httpcompat`. It exposes:

- `/v1/chat/completions`
- `/v1/responses`
- `/v1/messages`
- `/v1/embeddings`
- `/v1/rerank`
- `/v1/models`
- A2A, MCP, AG-UI, OpenAPI, Swagger, metrics, and health endpoints
- `/v1/providers/status` and `/ui/` for read-only runtime status

gRPC compatibility is isolated under `internal/grpccompat`.

## Provider Modes

AgentBridge separates providers into two execution modes.

### Standard LLM Providers

Standard providers expose model APIs but do not own an agentic loop. Examples
include `glm`, `openai-chat`, `openai-responses`, `anthropic`, `google`,
`ollama`, `llama.cpp`, and most OpenAI-compatible gateways.

For ACP, these providers run through AgentBridge's built-in harness:

1. Build a system prompt from cwd, project context, tools, and profile.
2. Stream a model turn.
3. Execute returned tool calls through AgentBridge's executor.
4. Append tool results and continue until the model stops or `max_turns` is
   reached.
5. Compact when runtime thresholds or context-overflow errors require it.

For HTTP, the same harness is used only when the request opts in with
`agent:<model>` or metadata such as `{"agent": true}`. Otherwise HTTP requests
are single provider calls.

### Provider-Native Agent Providers

Native agent providers already own the agentic loop, session runtime, tool
execution lifecycle, and some compaction behavior. These providers implement
`provider.NativeAgentProvider`.

The current native agent provider is `codex-app`, backed by local
`codex app-server` JSON-RPC over stdio.

For ACP sessions, AgentBridge bypasses the built-in harness:

1. ACP `session/new` creates an AgentBridge session id.
2. The provider receives that session id as the stable native session anchor.
3. ACP mode is advertised as `provider_native`.
4. `session/prompt` streams directly through the provider.
5. AgentBridge mirrors text/usage into ACP notifications and persists a light
   transcript, but local tool execution is not invoked.

This separation is intentional. If AgentBridge ran a native provider through
the built-in harness, tool calls and compaction could be executed twice or with
conflicting permission semantics.

## Session Identity

AgentBridge session ids are the primary cross-protocol session key.

- ACP always has a `sessionId`.
- HTTP agent loop can accept `metadata.session_id`, `metadata.sessionId`, or
  `metadata.thread_id`.
- `prompt_cache_key` may be used by native providers as a local session
  affinity key, but it is not necessarily forwarded upstream as a cache hint.

For `codex-app`, stable `session_id` or `prompt_cache_key` lets AgentBridge
reuse the same local Codex thread. The OpenAI-style `prompt_cache_retention`
field is dropped because Codex app-server does not expose that wire contract on
this path.

## HTTP Streaming

`POST /v1/chat/completions` uses true SSE streaming when `stream: true`.
Standard providers forward sanitized provider chunks as OpenAI-compatible
`chat.completion.chunk` events and flush each event immediately.

When the HTTP agent loop is enabled through `agent:<model>` or
`metadata.agent`, AgentBridge streams both assistant text deltas and loop
progress. Tool calls, tool status notifications, tool completion summaries,
usage, stop reasons, and turn boundaries are emitted as chunk objects with an
`agent_event` field. Raw tool input/output payloads are not included in these
intermediate events; tool results are still fed back into the internal model
loop. Tool-call arguments are also omitted from `agent_event` payloads because
they may contain user-provided secrets.

HTTP agent-loop permission policy comes from runtime config. `agent.yolo_mode:
true` maps to executor `bypass_permissions`; `agent.yolo_mode: false` maps to a
non-interactive read-only posture that rejects write/execute permission
requests. Omitting the setting keeps the legacy bypass default.

A2A streaming and AG-UI use the same agent-loop emitter. A2A sends assistant
text as `artifactUpdate` events and loop progress as `agentUpdate`; AG-UI sends
assistant text as `TEXT_MESSAGE_CONTENT` and loop progress as `AGENT_EVENT`.

## Compaction

Compaction is protocol-agnostic and configured in runtime config.

Standard providers use this order:

1. Provider-native compaction when `ConversationCompactor` is implemented.
2. Structured summary fallback.
3. Pruning only when `prune_fallback` is enabled.

Provider-native agent providers can override this behavior. `codex-app` calls
upstream `thread/compact/start` for the native thread, then replaces the local
mirror transcript with a checkpoint message plus recent turns. The upstream
thread remains the source of truth for deep session state.

## Runtime Commands

ACP text prompts that start with runtime commands are handled before any model
call, including provider-native agent sessions. They are not appended to the
LLM transcript as user messages.

- `/btw mark NAME` and the CLI alias `/save NAME` create a checkpoint with the
  current message index, model, mode, active skills, timestamp, and cache epoch.
- `/btw list` and the CLI alias `/list` show checkpoints.
- `/btw back NAME|ID` and the CLI alias `/load NAME|ID` truncate the local
  transcript to the checkpoint, restore the checkpoint's active skills, and
  increment `cacheEpoch` to avoid stale prompt-cache assumptions.
- `/context` reports estimated context usage, provider context window,
  configured compaction threshold/target, message count, checkpoint count, and
  cache epoch.
- `/compact [TARGET_TOKENS]` runs the same compaction path used by proactive
  and overflow compaction, but on demand. It is a continuation operation, not a
  rollback: older turns are summarized or provider-native compacted, recent
  turns are kept, and `cacheEpoch` is incremented when the transcript changes.
- `/subagent [--model MODEL] TASK` runs a bounded child provider call with a
  child session id and returns the result to the current session. It is the
  primitive Lua/client orchestration can use for delegated work. Subagents use
  the parent tool/permission path, inherit active skills and project context,
  emit a trace of tool calls into the parent result/update stream, run
  proactive and overflow compaction, retry once on context overflow, and reject
  recursive nesting beyond the configured depth. Exhausting `maxTurns` is an
  explicit failure. They are still a lightweight child-call primitive;
  long-lived multi-agent scheduling belongs in the CLI Lua orchestration layer.
  In provider-native sessions this command is an explicit server-side
  delegation call with a child session id; it does not merge into the
  provider's native parent thread.
- `/skill list|status|clear|NAME` manages markdown skills. Skills are loaded
  from `<cwd>/.agentbridge/skills/*.md` first, then
  `$XDG_CONFIG_HOME/agentbridge/skills/*.md`. Active skills are injected into
  the system prompt inside `<skill>` blocks with their content hash.

## CLI Lua Runtime

Lua orchestration is a client-owned harness layer. AgentBridge exposes
client-owned tools under the `client__<name>` namespace and routes calls back to
the ACP client with `client/call_tool`. `acp-agent` advertises `run_lua` and
`run_command`, so the model sees `client__run_lua` and `client__run_command`.
Shell execution is client-owned; the AgentBridge server does not run shell
commands or scripts as server-owned tools.

The detailed placement model, primitive/composition API, memory layers, and
canonical goal/autoresearch cases are documented in
[CLI Orchestration Design](cli-orchestration.md).
Tool ownership and permission boundaries are documented in
[Tool Placement](tool-placement.md).

## Safety Pipeline

Provider construction flows through `internal/provider/pipeline` when safety
features are enabled. The wrapper preserves optional provider capabilities:

- native compaction
- native agent-loop detection
- stream and compact option sanitation
- intention probing

PII masking happens before upstream dispatch. Streamed responses are unmasked
and sanitized before they are returned to clients.

HTTP agent-loop intermediate events scrub raw executor input, raw executor
output, and full tool content recursively before emission. This keeps live tool
status observable without streaming local file contents or command output as
side-channel event metadata.

## Model Catalog

`/v1/models` exposes real model ids plus provider ownership metadata. Synthetic
provider names such as `glm` or `grok` are not emitted as model ids unless they
are actual route aliases. Native agent models include metadata tags and compat
fields that identify them as provider-native agent models.

## Observability

`internal/observability` keeps a process-local snapshot:

- active provider name, kind, model, base URL, and native-agent status
- active HTTP requests
- active ACP sessions
- completed and failed HTTP request counters

`/v1/providers/status` returns the JSON snapshot. `/ui/` renders the same data
as a read-only dashboard. This is intentionally operational visibility only;
configuration changes still happen through files and environment variables.

## Current Native Provider Roadmap

The architecture is ready for more native-agent providers:

- `claude-code` should move from one-shot CLI JSON output to
  `@anthropic-ai/claude-agent-sdk` semantics or an equivalent stable native
  transport.
- ACP-capable providers can be represented as provider-native agents when
  AgentBridge is acting as an ACP bridge rather than an inner harness.
- Additional native providers should implement session-id based routing first,
  then add provider-specific compaction, option sanitation, and status probes.
