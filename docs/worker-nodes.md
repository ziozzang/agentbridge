# Worker Nodes

AgentBridge uses worker nodes to describe where an action actually executes.
This is a placement and permission concept. It is separate from the model,
provider, protocol adapter, and Bubble Tea UI layers.

## Definition

A worker node is an execution endpoint that can run a subtask or tool action on
behalf of an agent loop. It may be local to the terminal, inside the
AgentBridge server process, inside a container, or on a remote host.

Worker nodes own:

- the runtime where the action executes
- the filesystem, process table, network, and environment visible to that
  action
- permission prompts or policy for actions that can mutate state or inspect
  sensitive local data
- cancellation and status reporting for work already delegated to the node

Worker nodes do not own:

- provider streaming or model selection
- protocol wire parsing
- Bubble Tea rendering
- transcript storage, except for explicit worker-local logs/artifacts

## Built-In Worker Node: ACP CLI

`acp-agent` embeds a local worker node. Its current worker capabilities are:

- `run_command`, exposed to models as `client__run_command`
- `run_lua`, exposed to models as `client__run_lua`
- local file attachment/extraction before prompts
- CLI orchestration state such as temporary memory, queue state, and goal
  helpers

Shell execution is the clearest example. A bash command should run on the
terminal worker because that worker owns the user's cwd, environment, TTY
policy, and permission prompt. The AgentBridge server brokers the model tool
call but does not execute shell commands itself.

The CLI exposes this placement state in three places:

- the fixed TUI status surface shows the active worker kind and capability
  count
- `/status` prints the worker id, kind, capability list, permission policy, and
  cancellation support
- `/structure` includes a `worker:` block beside session and context state

The built-in worker id is currently `acp-agent:local`, with kind `terminal`.
This is intentionally a runtime state surface, not a provider or model name.

## Orchestrator Direction

AgentBridge should grow toward an orchestrator/master-node model, similar to a
small control plane. In that design, the orchestrator does not execute every
tool itself. Instead it coordinates multiple ACP servers, agent sessions, and
worker nodes.

Possible orchestrator responsibilities:

- maintain a directory of available ACP servers and worker nodes
- advertise node capabilities, health, locality, and permission policy
- route subtasks and tool calls to the right node
- delegate or broker authentication for downstream nodes
- act as an auth/session proxy when a client should not hold every downstream
  credential directly
- reconcile multiple ACP server nodes attached to one logical workspace
- hold session placement metadata and resume routing
- aggregate progress, cancellation, metrics, and audit events across nodes

This is a design direction, not a claim that distributed orchestration is fully
implemented today. Current code should still preserve the boundary: the server
brokers, the terminal worker executes local terminal actions, and future
orchestrators choose placement explicitly. The first milestone is a clear
contract for directory, capability, placement, auth delegation, and audit
events; clustering or automatic scheduling should follow that contract rather
than being hidden inside provider code.

## Future Worker Nodes

The same model can support additional worker nodes:

- container workers for sandboxed subtasks
- remote host workers for distributed build/test jobs
- GPU/media workers for expensive preprocessing
- browser workers for UI automation
- specialized MCP-backed workers

Each worker should advertise capabilities, health, and permissions explicitly.
The model should see a namespaced tool or routing hint, not an implicit server
side effect.

## Routing Rules

Worker routing must preserve the layer boundaries documented in
[CLI Orchestration Design](cli-orchestration.md).

- Agent loops decide that a subtask or tool action is needed.
- The placement layer chooses the worker node based on capability, policy,
  locality, and user approval.
- The worker executes the action and emits structured progress, result, error,
  and cancellation events.
- The UI renders those events; it does not run the worker.
- Provider adapters stream model chunks; they do not execute worker actions.

If a task can run in multiple places, prefer the worker closest to the resource
being inspected or modified. For example, local process inspection belongs on
the CLI worker, while a containerized test run belongs on a container worker.

## Cancellation

Worker actions must be tied to the active prompt or orchestration context.
Ctrl-C or an explicit stop request must cancel any pending worker request,
permission prompt, or delegated command. A worker that cannot cancel an action
must report that limitation as part of its capability contract.

## Completion Gate

A worker-node change is incomplete unless the code and documentation show:

- which worker owns execution
- how the capability is advertised
- how permission is decided
- how progress/results/errors are surfaced
- how cancellation propagates
- which layer is forbidden from executing the action
