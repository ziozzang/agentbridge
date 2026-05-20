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
routes:
  - match: ollama/*
    provider: ollama-cloud
    target_model: "$1"
    api_key_envs: OLLAMA_API_KEY_A, OLLAMA_API_KEY_B
    retry_keys: true
```

Route fields:

| Field | Purpose |
| --- | --- |
| `match` | Requested model pattern. Supports `*` wildcard. |
| `model` | Alias for `match`, useful in compact JSON. |
| `provider` | Configured provider name to delegate to. |
| `target_model` | Upstream model. `$model` keeps the original request; `$1` uses the wildcard capture. |
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

## Legacy Aliases

The following old variables are still accepted:

- `ACP_HARNESS_PROVIDER`, `ACP_HARNESS_API_KEY`, `ACP_HARNESS_MODEL`,
  `ACP_HARNESS_BASE_URL`, `ACP_HARNESS_PROVIDERS_FILE`
- `ACP_HARNESS_<NAME>_API_KEY`
- `ACP_GLM_MODEL`, `ACP_GLM_BASE_URL`, `ACP_GLM_THINKING`,
  `ACP_GLM_MAX_TOKENS`, `ACP_GLM_SESSION_DIR`
- `Z_AI_API_KEY` for GLM/Z.AI
