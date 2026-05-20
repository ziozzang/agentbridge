# Configuration

AgentBridge is configured through environment variables plus optional YAML/JSON
files under `$XDG_CONFIG_HOME/agentbridge`.

## Environment Priority

1. Per-provider variables, for example `AGENTBRIDGE_OPENAI_API_KEY`.
2. Top-level variables, for example `AGENTBRIDGE_API_KEY`.
3. Explicit config file: `AGENTBRIDGE_CONFIG_FILE`.
4. Explicit provider YAML: `AGENTBRIDGE_PROVIDERS_FILE`.
5. User config YAML:
   `$XDG_CONFIG_HOME/agentbridge/config.yaml` or
   `~/.config/agentbridge/config.yaml`.
6. User provider YAML:
   `$XDG_CONFIG_HOME/agentbridge/providers.yaml` or
   `~/.config/agentbridge/providers.yaml`.
7. Legacy user config/provider YAML:
   `$XDG_CONFIG_HOME/acp-harness/config.yaml`,
   `$XDG_CONFIG_HOME/acp-harness/providers.yaml`.
8. Embedded templates in `internal/config/providers.yaml`.

Legacy `ACP_HARNESS_*` variables remain supported as aliases.

## Top-Level Variables

| Variable | Purpose |
| --- | --- |
| `AGENTBRIDGE_PROVIDER` | Active provider. Default: `glm`. |
| `AGENTBRIDGE_MODEL` | Override the active provider default model. |
| `AGENTBRIDGE_API_KEY` | API key for the active provider. |
| `AGENTBRIDGE_BASE_URL` | Base URL override. |
| `AGENTBRIDGE_CONFIG_FILE` | Absolute path to full config YAML. |
| `AGENTBRIDGE_PROVIDERS_FILE` | Absolute path to provider YAML. |
| `AGENTBRIDGE_PLUGINS` | Comma-separated plugins, e.g. `sqlite,duckdb`. |
| `AGENTBRIDGE_DISABLED_PLUGINS` | Comma-separated active plugin names to suppress. |
| `AGENTBRIDGE_MCP_FILE` | External MCP server config file, JSON or YAML. |
| `AGENTBRIDGE_DISABLED_MCPS` | Comma-separated configured MCP server names to suppress. |
| `AGENTBRIDGE_ROUTER_FILE` | Router route file, JSON or YAML. |

## Per-Provider Variables

Use the provider name in uppercase. Hyphens become underscores.

| Variable | Purpose |
| --- | --- |
| `AGENTBRIDGE_<NAME>_API_KEY` | Provider-specific API key. |
| `AGENTBRIDGE_<NAME>_BASE_URL` | Provider-specific base URL. |
| `AGENTBRIDGE_<NAME>_MODEL` | Provider-specific default model. |

Examples:

```bash
AGENTBRIDGE_PROVIDER=openai
AGENTBRIDGE_OPENAI_API_KEY=example-api-key
AGENTBRIDGE_OPENAI_MODEL=gpt-4.1-mini
```

## Logging

| Variable | Purpose |
| --- | --- |
| `AGENTBRIDGE_LOG_LEVEL` | `trace`, `debug`, `info`, `warn`, `error`, or `off`. |
| `AGENTBRIDGE_LOG_FILE` | Optional log file path. |
| `AGENTBRIDGE_LOG_BOTH` | `1` to write both stderr and the log file. |
| `AGENTBRIDGE_LOG_MAX_BYTES` | Rotation threshold. Default: `10485760`. |
| `AGENTBRIDGE_LOG_MAX_FILES` | Rotated file count. Default: `5`. |

Legacy `ACP_HARNESS_LOG_*` and `ACP_GLM_DEBUG` are still accepted.

## Session Persistence

| Variable | Purpose |
| --- | --- |
| `AGENTBRIDGE_SESSION_DIR` | Session storage directory. |
| `XDG_STATE_HOME` | Base for the default session directory. |

Default: `$XDG_STATE_HOME/agentbridge/sessions` or
`~/.local/state/agentbridge/sessions`.

## Config YAML

`config.yaml` has the same `providers:` schema as `providers.yaml`, but is
the preferred place for broader AgentBridge configuration such as router
routes.

Default locations:

- `$XDG_CONFIG_HOME/agentbridge/config.yaml`
- `~/.config/agentbridge/config.yaml`

Explicit override:

```bash
AGENTBRIDGE_CONFIG_FILE=/path/to/config.yaml agentbridge
```

Example:

```yaml
providers:
  router:
    kind: router
    default_model: ollama/gpt-oss:120b
    extra:
      routes_file: ${XDG_CONFIG_HOME}/agentbridge/router.yaml
```

## External MCP Servers

External MCP servers can be registered globally for both ACP sessions and the
HTTP MCP compatibility endpoint. Set `AGENTBRIDGE_MCP_FILE`, or place
`mcp.yaml` / `mcp.json` under `$XDG_CONFIG_HOME/agentbridge`.

```yaml
mcp_servers:
  - name: search
    type: http
    url: http://127.0.0.1:8090/mcp
    allow_tools: [foo, search*]
    deny_tools: [search_debug]
    headers:
      Authorization: Bearer ${MCP_TOKEN}
    enabled: true
```

CLI / stdio example:

```yaml
mcp_servers:
  - name: filesystem
    type: stdio
    command: npx
    args: [-y, "@modelcontextprotocol/server-filesystem", /workspace]
    env:
      NODE_OPTIONS: --no-warnings
    cwd: /workspace
```

`mcpServers` is also accepted as either a list or a name-keyed object. Set
`disabled: true`, `enabled: false`, or `AGENTBRIDGE_DISABLED_MCPS=search` to
turn a server off. HTTP MCP tools are exported as `mcp__<server>__<tool>`.
Use `allow_tools` / `deny_tools` to filter which upstream commands are
exposed; both fields accept lists or comma/newline-separated strings and
support wildcards.

## Provider YAML

```yaml
providers:
  myprov:
    kind: openai-chat
    base_url: https://example.com/v1
    api_key: ${MYPROV_API_KEY}
    default_model: my-model
```

Then run:

```bash
AGENTBRIDGE_PROVIDER=myprov agentbridge
```

## Router Route Schema

The `router` provider routes by requested model name and delegates to another
configured provider. AgentBridge intentionally does not hardcode model routes.
Put routes in `providers.router.extra.routes`, in
`providers.router.extra.routes_file`, or in `AGENTBRIDGE_ROUTER_FILE`.

Route file locations checked automatically:

- `$XDG_CONFIG_HOME/agentbridge/router.yaml`
- `$XDG_CONFIG_HOME/agentbridge/router.json`

Route file shape:

```yaml
default_model: ollama/gpt-oss:120b
aliases:
  oss: ollama/gpt-oss:120b
  glm-fast: glm-5-turbo
routes:
  - match: ollama/*
    provider: ollama-cloud
    target_model: "$1"
    api_key_envs: OLLAMA_API_KEY_A, OLLAMA_API_KEY_B
    retry_keys: true
  - match: glm-5.1
    aliases: [glm]
    provider: zai
    target_model: glm-5.1
    request_defaults:
      reasoning: off
    fallbacks:
      - provider: zai
        target_model: glm-5-turbo
  - models: "*"
    provider: openrouter
    target_model: "$model"
```

Route fields:

| Field | Purpose |
| --- | --- |
| `match` | Requested model pattern. Supports `*` wildcard. |
| `model` | Alias for `match`, useful in compact JSON. |
| `models` | One or more model patterns for the same provider. `models: "*"` creates a provider-wide catch-all route. |
| `provider` | Configured provider name to delegate to. |
| `target_model` | Upstream model. `$model` keeps the original request; `$1` uses the wildcard capture. |
| `aliases` | Extra requested model names that should hit this route. |
| `fallbacks` | Ordered alternate route list tried when the primary route fails before streaming output. |
| `request_defaults` | Extra upstream request-body fields injected by adapters that support it, currently OpenAI Chat Completions. |
| `api_key_envs` | Environment variable names for one or more keys. Accepts list or delimited string. |
| `api_keys` | Literal keys. Accepts list or delimited string; prefer `api_key_envs`. |
| `retry_keys` | If true, retry the next key after pre-stream 429/quota/weekly/5h limit errors. |
| `default` | Fallback route when no pattern matches. |
| `max_tokens` | Per-route max token override. |
| `context_window` | Per-route context window override. |

`api_key_envs` and `api_keys` accept all of these forms:

```yaml
api_key_envs: [OLLAMA_API_KEY_A, OLLAMA_API_KEY_B]
api_key_envs: OLLAMA_API_KEY_A, OLLAMA_API_KEY_B
api_key_envs: |
  OLLAMA_API_KEY_A
  OLLAMA_API_KEY_B
```

Limit detection is best-effort. The router detects HTTP 429 and common text
signals such as `rate limit`, `quota`, `weekly limit`, and `5h` before any
streamed output is emitted. It marks the key as limited for the current
process and skips it on later round-robin picks. Reset time parsing is not
provider-stable yet.

Fallbacks are for alternate upstream models/providers, not for continuing a
partially streamed response. If a route emits any output and then fails,
AgentBridge returns that failure instead of replaying the conversation against
another model. This avoids duplicating side effects and mixed-model answers.

`request_defaults` is intentionally provider-specific. For `openai-chat` it is
merged into the JSON body after AgentBridge builds the normal request, so it
can force vendor-specific fields such as:

```yaml
request_defaults:
  reasoning: off
  reasoning_cost: 1234
```

## Legacy Aliases

The following old variables are still accepted:

- `ACP_HARNESS_PROVIDER`, `ACP_HARNESS_API_KEY`, `ACP_HARNESS_MODEL`,
  `ACP_HARNESS_BASE_URL`, `ACP_HARNESS_PROVIDERS_FILE`
- `ACP_HARNESS_<NAME>_API_KEY`
- `ACP_GLM_MODEL`, `ACP_GLM_BASE_URL`, `ACP_GLM_THINKING`,
  `ACP_GLM_MAX_TOKENS`, `ACP_GLM_SESSION_DIR`
- `Z_AI_API_KEY` for GLM/Z.AI
