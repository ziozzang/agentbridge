# Configuration

All user-visible knobs are environment variables. There is no separate
config-file format you *must* learn — but a YAML file is supported for
people who want to define multiple providers up front.

## Environment-variable tiers

The harness reads variables in this priority order (highest wins):

1. **Per-provider override**, e.g. `ACP_HARNESS_OPENAI_API_KEY`,
   `ACP_HARNESS_ANTHROPIC_BASE_URL`.
2. **Top-level override**: `ACP_HARNESS_API_KEY`, `ACP_HARNESS_BASE_URL`,
   `ACP_HARNESS_MODEL`.
3. **User YAML file**: `$XDG_CONFIG_HOME/acp-harness/providers.yaml` or
   the path in `ACP_HARNESS_PROVIDERS_FILE`.
4. **Embedded templates**: `internal/config/providers.yaml`.

The variable you *almost always* want is just two of these:

```bash
export ACP_HARNESS_PROVIDER=openai
export ACP_HARNESS_API_KEY=sk-...
```

## Top-level variables

| Variable | Purpose |
| --- | --- |
| `ACP_HARNESS_PROVIDER` | Which provider from the templates is active. Default: `glm`. |
| `ACP_HARNESS_MODEL` | Override the active provider's default model. |
| `ACP_HARNESS_API_KEY` | API key applied to the active provider unless a per-provider override is set. |
| `ACP_HARNESS_BASE_URL` | Override base URL for the active provider. |
| `ACP_HARNESS_PROVIDERS_FILE` | Absolute path to a YAML file to merge over the embedded templates. |
| `ACP_HARNESS_PLUGINS` | Comma-separated list of plugins to activate (e.g. `sqlite,duckdb`). |

## Per-provider overrides

Replace `<NAME>` with the provider key from `providers.yaml`
(`openai`, `openai-responses`, `anthropic`, `glm`, `ollama`,
`openrouter`, `litellm`, `codex`):

| Variable | Purpose |
| --- | --- |
| `ACP_HARNESS_<NAME>_API_KEY` | Per-provider key (wins over `ACP_HARNESS_API_KEY`). |
| `ACP_HARNESS_<NAME>_BASE_URL` | Per-provider base URL. |
| `ACP_HARNESS_<NAME>_MODEL` | Per-provider default model. |

`<NAME>` is uppercased; hyphens become underscores
(`openai-responses` → `OPENAI_RESPONSES`).

## Logging

| Variable | Purpose |
| --- | --- |
| `ACP_HARNESS_LOG_LEVEL` | `trace` / `debug` / `info` / `warn` / `error` / `off`. Default `warn`. |
| `ACP_HARNESS_LOG_FILE` | Path to write logs to (in addition to nothing else). |
| `ACP_HARNESS_LOG_BOTH` | `1` to mirror logs to both stderr and the file. |
| `ACP_HARNESS_LOG_MAX_BYTES` | Rotation threshold (default `10485760`, i.e. 10 MiB). |
| `ACP_HARNESS_LOG_MAX_FILES` | Number of rotated files to keep (default `5`). |
| `ACP_GLM_DEBUG` | Back-compat: `true`/`1` forces debug level. |

Logs never go to stdout — stdout is reserved for the ACP JSON-RPC stream.

## Session persistence

| Variable | Purpose |
| --- | --- |
| `ACP_GLM_SESSION_DIR` | Directory for persisted session JSON files. Defaults to `$XDG_STATE_HOME/glm-acp-agent/sessions`. |
| `XDG_STATE_HOME` | Used to locate the default session directory. |
| `XDG_CONFIG_HOME` | Used to locate the credentials/providers files. |

## YAML user-override file

You can keep most things in env vars, but if you want to define a
brand-new provider it's easier to drop a YAML file at
`$XDG_CONFIG_HOME/acp-harness/providers.yaml` (or `ACP_HARNESS_PROVIDERS_FILE`):

```yaml
providers:
  myprov:
    kind: openai-chat
    base_url: https://api.mycompany.com/v1
    api_key: ${MY_COMPANY_TOKEN}
    default_model: my-special-model
    available_models:
      - my-special-model
      - my-other-model
    extra_headers:
      X-Org: my-team

  # Override an existing template.
  glm:
    default_model: glm-4.7
```

Then:

```bash
export ACP_HARNESS_PROVIDER=myprov
./glm-acp-agent
```

`${VAR}` and `${VAR:-default}` are expanded against the process
environment before YAML parsing.

## Back-compat (GLM-only) variables

The following are still honoured so existing setups keep working:

| Variable | Purpose |
| --- | --- |
| `Z_AI_API_KEY` | Picked up as the GLM provider key when no harness override is set. |
| `ACP_GLM_MODEL` / `ACP_GLM_AVAILABLE_MODELS` | GLM model selection. |
| `ACP_GLM_BASE_URL` / `ACP_GLM_MAX_TOKENS` / `ACP_GLM_THINKING` | GLM tuning. |
| `ACP_GLM_DEBUG` | Force debug logging. |
