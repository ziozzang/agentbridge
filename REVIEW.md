# Review Status

Date: 2026-05-21

This file tracks the currently valid engineering review items. Older findings
about CLI streaming, missing context status, client-owned shell placement,
subagent tool routing, and local `/goal` ownership have been addressed in the
current tree.

## Addressed

- `acp-agent` streaming no longer lets the progress/status line erase answer
  content. The UI clears the progress line once before answer streaming and
  refreshes status without redrawing over active output.
- `acp-agent` now has a dedicated rendering layer and Codex-inspired terminal
  surfaces for user/assistant history cells, tool/thinking cells, status cards,
  and approval overlays.
- `Ctrl-C` during an active prompt sends `session/cancel`; outside an active
  prompt it exits the client.
- Prompt submission is worker-backed: extra user prompts entered while a prompt
  is running are queued, visible in the status line and `/queue`, then drained
  sequentially.
- Assistant streaming uses a small buffered stream controller that flushes on
  newlines, size, timer threshold, tool boundaries, and finalization instead of
  writing every tiny delta directly.
- Tool permissions use explicit numbered choices, including same-command
  approval and yolo mode. Client shell commands also allow an alternate command
  entry.
- Context state is sent through `session_info_update` and shown in CLI status,
  `/status`, and `/context`.
- Shell execution is client-owned through `client__run_command`; the server
  does not own shell/script execution.
- `/goal` is a CLI Lua harness command. Goal state lives in the local
  orchestration store, not in server ACP session state.
- Subagent tool calls route through the parent executor, so parent permission
  handling and client-owned tools still apply.
- Subagents now inherit active skills/tool names, emit tool traces, run
  proactive and overflow compaction, retry once on context overflow, and reject
  recursive nesting beyond the depth limit. They now fail explicitly when
  `maxTurns` is exhausted instead of returning an empty success.
- `Store.Load` now reports read, decode, and unsupported-schema errors instead
  of silently treating them as missing sessions.
- Vision MCP lazy initialization is guarded by a mutex.
- Session setup now shares mode resolution, native-agent handling, and
  session-scoped MCP wiring across New/Load/Resume/Fork.

## Remaining Structural Work

- `internal/agent/agent.go` still carries too many responsibilities: session
  lifecycle, prompt loop, runtime commands, compaction, and provider-native
  routing. A session manager and prompt-loop runner would reduce coupling.
- `cmd/acp-agent/main.go` is still large. It has already been split into
  Lua/UI/rendering/shell/choice helpers, but the remaining command dispatcher
  and ACP client transport could be separated further.
- ACP updates and permission calls still use `map[string]any` payloads. Typed
  update structs would catch field-name regressions at compile time.
- `provider.Message.Content` remains `any`. A small content abstraction or
  helper method would reduce repeated type switches across providers and
  compaction.
- `glm` still exposes provider-neutral aliases. This is technically safe
  because they are aliases, but the package name still obscures the
  provider-neutral intent.
- OpenAI-compatible SSE parsing and tool-call assembly remain duplicated
  between GLM and generic OpenAI chat adapters.
- Provider `Extra map[string]any` accessors and `firstNonEmpty` helpers remain
  duplicated across packages.

## Notes

The current subagent implementation is no longer just a POC for simple tasks,
but it is still intentionally not a long-lived multi-agent scheduler. Complex
goal loops, autoresearch, queues, timers, and side sessions should be composed
in the CLI Lua orchestration layer and exposed through client-owned tools.
