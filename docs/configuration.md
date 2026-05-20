# Configuration

AgentBridge is configured primarily through environment variables and an
optional provider YAML file.

## Environment Priority

1. Per-provider variables, for example `AGENTBRIDGE_OPENAI_API_KEY`.
2. Top-level variables, for example `AGENTBRIDGE_API_KEY`.
3. User provider YAML:
   `$XDG_CONFIG_HOME/agentbridge/providers.yaml` or
   `~/.config/agentbridge/providers.yaml`.
4. Legacy user provider YAML:
   `$XDG_CONFIG_HOME/acp-harness/providers.yaml`.
5. Embedded templates in `internal/config/providers.yaml`.

Legacy `ACP_HARNESS_*` variables remain supported as aliases.

## Top-Level Variables

| Variable | Purpose |
| --- | --- |
| `AGENTBRIDGE_PROVIDER` | Active provider. Default: `glm`. |
| `AGENTBRIDGE_MODEL` | Override the active provider default model. |
| `AGENTBRIDGE_API_KEY` | API key for the active provider. |
| `AGENTBRIDGE_BASE_URL` | Base URL override. |
| `AGENTBRIDGE_PROVIDERS_FILE` | Absolute path to provider YAML. |
| `AGENTBRIDGE_PLUGINS` | Comma-separated plugins, e.g. `sqlite,duckdb`. |

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

## Legacy Aliases

The following old variables are still accepted:

- `ACP_HARNESS_PROVIDER`, `ACP_HARNESS_API_KEY`, `ACP_HARNESS_MODEL`,
  `ACP_HARNESS_BASE_URL`, `ACP_HARNESS_PROVIDERS_FILE`
- `ACP_HARNESS_<NAME>_API_KEY`
- `ACP_GLM_MODEL`, `ACP_GLM_BASE_URL`, `ACP_GLM_THINKING`,
  `ACP_GLM_MAX_TOKENS`, `ACP_GLM_SESSION_DIR`
- `Z_AI_API_KEY` for GLM/Z.AI
