# CLI Orchestration Design

AgentBridge treats CLI orchestration as a client-owned harness layer. The server
can expose the layer to models as normal tools, but execution happens where the
capability is placed: in the terminal client, in a server process, or in another
client implementation.

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

### Terminal UI

`acp-agent` uses Bubble Tea for the default interactive shell. The layering is:

- ACP transport and JSON-RPC handling stay in `client`.
- Transport updates are normalized into `uiEvent` values.
- The Bubble Tea model owns the viewport, composer, status surface, permission
  overlay, and transcript cells.
- The legacy ANSI/line-oriented UI remains available behind `--plain` for
  debugging and minimal terminals.

This split keeps terminal control out of the ACP transport. The server emits
structured events; the client decides how those events are rendered.

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
