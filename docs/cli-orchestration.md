# CLI Orchestration Design

AgentBridge treats CLI orchestration as a client-owned harness layer. The server
can expose the layer to models as normal tools, but execution happens where the
capability is placed: in the terminal client, in a server process, or in another
client implementation.

In the worker-node model, `acp-agent` is the built-in terminal worker. Its
worker capabilities include shell execution, Lua orchestration, file
attachment, and CLI-local memory/queue state.
The active worker is part of CLI runtime state: the TUI status surface shows a
compact worker label, while `/status` and `/structure` expose the worker id,
kind, capabilities, permission policy, and cancellation support.

## Placement

There are two tool ownership classes.

- Server-owned tools are implemented by AgentBridge and executed by the server
  executor.
- Client-owned tools are advertised by the ACP client in
  `initialize.clientCapabilities.clientTools`. AgentBridge exposes them to the
  model under `client__<name>` and routes calls back to the client with
  `client/call_tool`.

`acp-agent` advertises `run_lua` and `run_command`; the model sees them as
`client__run_lua` and `client__run_command`. This keeps local files, shell
execution, UI state, text handling, timers, and CLI control in the CLI while
still letting an LLM call the orchestration layer as a tool. The server does
not execute shell commands or shell scripts.

The broader ownership and permission rules are documented in
[Tool Placement](tool-placement.md).

## Layers

Layer separation is a completion criterion for the ACP TUI work, not merely an
implementation preference. A change is not considered complete if it works by
mixing transport, command execution, rendering, permission selection, and
provider streaming into the same component. Each change should keep the
ownership boundary visible in code, tests, and this document.

### Terminal UI

`acp-agent` uses Bubble Tea for the default interactive shell. The layering is:

- ACP transport and JSON-RPC handling stay in `client`.
- Transport updates are normalized into `uiEvent` values.
- The Bubble Tea model owns the viewport, composer, status surface, permission
  overlay, and transcript cells.
- Transcript rendering is a dedicated surface: history cells are normalized
  before rendering, and the viewport consumes the rendered transcript string.
- Transcript body text is wrapped to the viewport width, including assistant
  output, thinking, tool details, errors, and info cells, so long provider/tool
  lines do not break the terminal layout horizontally.
- Viewport refresh consumes the wrapped transcript surface directly, so
  auto-follow and manual scroll preservation operate on the same rendered
  content the user sees.
- Status and notice rendering is also a dedicated surface. It derives running
  activity, token progress, queue/subagent/tool counts, scroll state, context,
  quota, worker placement, permission mode, and session identity separately
  from the frame layout.
- The status surface is a single fixed bottom line. Long status content is
  ANSI-aware truncated instead of wrapped, preserving the transcript/composer
  layout even on narrow terminals.
- The notice row above the composer follows the same fixed-row rule: long
  progress text, stop prompts, or completion hints are truncated before frame
  assembly so they cannot move the composer or status rows.
- Permission approval overlays are a dedicated surface for rendering choice
  lists and mapping number/arrow/yes/no keys to ACP permission replies.
- Approval replies close the overlay in the same key event; the next frame
  returns to the transcript and bottom composer without waiting for server
  traffic.
- Permission choices that need replacement text, such as `other command`, stay
  inside the Bubble Tea overlay. They do not read directly from stdin while the
  terminal is in raw mode.
- Permission overlays and client-owned tool calls share the active prompt
  context. Ctrl-C cancels the pending prompt and any waiting permission/client
  tool request instead of leaving a goroutine blocked behind the UI.
- Completion hints are a dedicated surface. It owns slash-command argument
  hints and compact suggestion text, while the composer only owns text input.
- The bottom composer is a dedicated surface, so fixed-width input rendering is
  separate from frame assembly and can evolve independently of the transcript.
- The composer row is fixed-height as well. Long input text is ANSI-aware
  truncated by the composer surface, preventing it from wrapping into the status
  line.
- The top-level frame is also a dedicated surface. It assembles transcript,
  overlay, notice, composer, and status rows without owning any transport or
  command behavior.
- TUI component construction lives outside the runtime update loop, so the
  composer, spinner, viewport, and initial model state can be tested without
  launching a terminal program.
- Bubble Tea program options are built in one lifecycle helper. The interactive
  program receives the caller context and alternate-screen mode together, so
  external cancellation and terminal ownership stay explicit.
- Keyboard handling is split into a small key layer: global interrupt/exit keys
  win first, overlays own selection keys, the transcript viewport owns scroll
  keys, and the composer keeps normal text navigation keys.
- Interrupt keys must not perform network cancellation inline inside the Bubble
  Tea update function. The key event updates local UI state immediately, then
  schedules cancellation as a Bubble Tea command so rendering and subsequent
  high-priority keys stay responsive.
- The Bubble Tea update loop routes messages through small handlers for window
  resize, key routing, ACP UI events, command completion, spinner ticks, and
  composer updates. This keeps the runtime event loop testable without starting
  a terminal program.
- Terminal resize events reflow the viewport and composer through the same
  update loop, with tiny terminal dimensions clamped to valid component sizes.
- Stop requests append an immediate transcript cell and refresh the viewport in
  the same key event, so interrupt feedback is visible before the next provider
  event arrives.
- Prompts submitted while a turn is busy are queued by the client, emitted as
  state/info events, and rendered in both the transcript and status surface.
- Local slash commands emit command cells before their result cells. This keeps
  input/output separation visible for `/help`, `/status`, permission changes,
  Lua orchestration, and other client-side commands.
- Local slash command execution is tracked separately from provider prompt
  busy state. A command running in a Bubble Tea command must appear in the
  notice/status surfaces without pretending that the model turn itself is busy.
- Local slash commands run with their own cancellable context. If no model turn
  is busy but a local command is running, Ctrl-C cancels that command first and
  only exits the client on the next Ctrl-C.
- Exit paths such as Ctrl-D, `/quit`, and second Ctrl-C own local cleanup:
  pending permission overlays, prompt contexts, choice waiters, and local
  command contexts must be cancelled before the Bubble Tea program exits.
- Worker-node routed client tools, such as `client__run_command`, execute in
  the CLI process. Their lifecycle must still be visible as TUI tool cells and
  active tool counts, because a delegated worker action is part of the current
  turn.
- Tool/subagent state updates must release client state locks before emitting
  UI state events. The UI event pipeline snapshots client state, so emitting
  while holding that mutex can deadlock the Bubble Tea runtime.
- A minimal line-oriented fallback remains available behind `--plain` for
  debugging and minimal terminals. It does not own terminal layout.

This split keeps terminal control out of the ACP transport. The server emits
structured events; the client decides how those events are rendered.
For debugging, `acp-agent --json-events` bypasses the Bubble Tea renderer and
prints the same normalized event stream as newline-delimited JSON.

The Lua API is organized as primitive functions plus composition functions.
Primitive functions touch one concern directly. Composition functions assemble
primitives into reusable LLM workflows.

### Data

Primitives:

- `cli.data.attach(path)`
- `cli.data.files()`
- `cli.data.clear_files()`

Compositions:

- `cli.data.extract(text, schema, opts)`
- `cli.data.rank(candidates, criteria, opts)`
- `cli.data.research_source(topic, opts)`

Data tools prepare context for the model: local files, extracted text, ranked
candidates, and research source plans.

### Memory

Primitives:

- Turn memory: `cli.memory.get/set/delete/list`
- Working memory: `cli.memory.kv_get/kv_set/kv_delete/kv_list`
- Searchable memory: `cli.memory.put/search`
- Structured memory: `cli.util.sql_query/sql_exec`

The persistent SQLite store defaults to
`$XDG_STATE_HOME/agentbridge/acp-agent/orchestration.sqlite` and can be
overridden with `AGENTBRIDGE_CLI_ORCH_DB`. It initializes `kv`, `events`,
`jobs`, `memories`, and `artifacts` tables.

Use this split deliberately:

- Turn memory is scratch state for one Lua run.
- KV is small durable working memory across turns.
- SQLite tables hold job queues, event logs, observations, artifacts, and
  long-running orchestration state.

### Orchestration

Primitives:

- `cli.orch.plan(items)`
- `cli.orch.fetch_next_job(plan)` / `next_job(plan)`
- `cli.orch.run(job, fn)`
- `cli.orch.check_status(plan)` / `status_line(plan)`
- `cli.orch.trigger(name, predicate, action)`
- `cli.orch.run_triggers(ctx, triggers)`
- `cli.orch.steer(ctx, directive)`
- `cli.orch.timer(opts)` / `tick(timer)`
- `cli.orch.cron(opts, fn)`

Compositions:

- `cli.orch.control_loop(opts)`
- `cli.orch.reflect(ctx, opts)`
- `cli.orch.judge(goal, evidence, opts)`
- `cli.orch.delegate(task, opts)`

The intended control loop is:

`plan -> fetch_next_job -> run -> check_status -> trigger -> steer/continue/stop`

`control_loop`, `run`, and the local goal harness emit `orch:*` UI events while
they execute, so the Bubble Tea transcript and `--json-events` mode can show
job start, job completion, failures, and loop completion without waiting for the
Lua script to finish.

Triggers are the boundary between observation and control. They can stop the
loop, write steering directives, compact context, save checkpoints, enqueue
jobs, or launch delegated work.

### Maintenance

Primitives:

- `cli.maint.status()`
- `cli.maint.structure()`
- `cli.maint.snapshot()`
- `cli.maint.context()`
- `cli.maint.compact(target)`
- `cli.maint.save(name)`

Maintenance functions expose current state and mutate the session only when the
script explicitly asks for it.

### Utilities

Primitives:

- `cli.util.now()`
- `cli.util.time_unix()`
- `cli.util.sleep_ms(ms)`
- `cli.util.emit(name, payload)`
- `cli.util.sql_query(sql)`
- `cli.util.sql_exec(sql)`

`emit` also writes to the `events` table.

### LLM Composition

Compositions:

- `cli.llm.ask(prompt, opts)`
- `cli.llm.reflect(ctx, opts)`
- `cli.llm.judge(goal, evidence, opts)`
- `cli.llm.extract(text, schema, opts)`
- `cli.llm.rank(candidates, criteria, opts)`
- `cli.llm.summarize(text, opts)`
- `cli.llm.critic(planOrAnswer, opts)`

These functions create sufficiently contextual prompts for the model. By
default they return the prompt string. With `opts.run = true`, they call
`cli.prompt(...)`. With `opts.store_key` or `opts.memory`, they also write the
prompt to KV or searchable memory.

## Cases To Keep Tested

### Goal Loop

A goal loop stores a current goal, creates a plan, runs jobs, uses `judge` to
create goal-specific evaluation prompts, updates `jobs`, and fires a trigger
that steers the loop to stop when enough work is done.

`acp-agent` ships a small local goal harness on top of this design:

- `/goal` or `/goal status` reads `cli.goal.status()`.
- `/goal set TEXT` stores the goal in the local orchestration KV store.
- `/goal run` sends a goal-specific prompt through the current ACP session.
- `/goal clear` removes the local goal.

Goal judgment is deliberately CLI-owned. The server sees only the resulting ACP
prompt/tool traffic; it does not store a canonical goal field on the ACP
session.

### Autoresearch

Autoresearch creates a source plan with `research_source`, stores the plan in
KV and memory, runs extraction jobs, ranks evidence, and stops when coverage is
sufficient.

### Memory-Backed Queue

A long-running workflow stores jobs in SQLite, fetches the next job with SQL,
writes observations into `memories`, and emits events for each state change.

### Maintenance Steering

A workflow can call `cli.maint.context`, `compact`, and `save` from triggers
when context pressure or risk conditions appear.

## Safety Boundaries

- Lua runs in the client runtime, not the server harness.
- Client-owned tools are namespaced as `client__*`.
- Shell commands and shell scripts are client-owned. Use
  `client__run_command`; do not add server-owned shell execution.
- The terminal UI is intentionally a client surface. User messages, assistant
  streams, thinking, tool lifecycle updates, status cards, and approval
  overlays are rendered locally from ACP updates; the server should keep
  emitting structured events instead of terminal control sequences.
- Scripts should avoid recursive `cli.prompt(...)` while an active prompt is in
  flight.
- `sql_query` is read-only. `sql_exec` mutates only the local orchestration DB.
- Local file and attachment access is client-local placement by design.
