# Tool Placement

AgentBridge separates tool execution by ownership. This is a security and
deployment boundary, not only an implementation detail.

## Server-Owned Tools

Server-owned tools run in the AgentBridge process and are defined under
`internal/tools/definitions`.

Current server-owned tools:

- `read_file`
- `write_file`
- `list_files`
- `web_search`
- `web_reader`
- `image_analysis`
- `client_run_lua` compatibility bridge

Server-owned file tools resolve paths against the ACP session cwd. Writes use
the ACP permission path. The server must not expose shell execution as a
server-owned tool.

## Client-Owned Tools

Client-owned tools are advertised by an ACP client through
`initialize.clientCapabilities.clientTools`. AgentBridge renames them to
`client__<name>` for model-facing tool calls and routes execution back to the
client with `client/call_tool`.

`acp-agent` currently advertises:

- `run_lua`, surfaced to models as `client__run_lua`
- `run_command`, surfaced to models as `client__run_command`

Shell commands and shell scripts belong in this client-owned layer because the
terminal client owns the interactive user, local tty policy, permission mode,
and cwd. AgentBridge may broker the model tool call, but it does not execute
the command.

## Permission Model

For `acp-agent`, client-owned shell execution uses the CLI permission mode:

- `--permission prompt`: ask on stderr/stdin before execution.
- `--permission allow` or `--yolo`: execute without prompting.
- `--permission reject` or `--read-only`: reject execution.
- `--permission cancel`: cancel execution.

Server-owned write operations continue to use ACP `session/request_permission`.
HTTP agent-loop requests do not have an interactive terminal; their write and
execute posture is configured by runtime settings.

## Provider-Native Agents

Provider-native agent providers such as `codex-app` already own their own
runtime, tool lifecycle, and compaction behavior. AgentBridge bypasses the
standard harness for those providers. Client-owned tools remain an ACP client
capability; provider-native transports need provider-specific support before
they can call them.
