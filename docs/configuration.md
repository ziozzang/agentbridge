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

The active provider can also be selected in `config.yaml` with one of
`provider`, `active_provider`, or `default_provider`. Environment variables
still win over the file setting:

```yaml
provider: router
```

## Top-Level Variables

| Variable | Purpose |
| --- | --- |
| `AGENTBRIDGE_PROVIDER` | Active provider. Default: `glm`. |
| `AGENTBRIDGE_MODEL` | Override the active provider default model. |
| `AGENTBRIDGE_API_KEY` | API key for the active provider. |
| `AGENTBRIDGE_BASE_URL` | Base URL override. |
| `AGENTBRIDGE_CONFIG_FILE` | Absolute path to full config YAML. |
| `AGENTBRIDGE_PROVIDERS_FILE` | Absolute path to provider YAML. |
| `AGENTBRIDGE_AGENTS_FILE` | Agent profile YAML/JSON file. |
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

Session JSON also stores runtime checkpoints, active skill references, and
`cacheEpoch`. The file format is internal; do not rely on it as a public API.

## Skills

Runtime skills are markdown files loaded by `/skill` commands:

- Project-local: `<cwd>/.agentbridge/skills/*.md`
- User config: `$XDG_CONFIG_HOME/agentbridge/skills/*.md` or
  `~/.config/agentbridge/skills/*.md`

Project-local skills shadow user-config skills with the same file name.

## Project Context

AgentBridge automatically loads one repository context file from the session
cwd and injects it into the system prompt. Preferred order:

1. `SOUL.md`
2. `AGENTS.md`
3. `CLAUDE.md`

The content is capped by `MaxProjectContextChars`. `acp-agent /status` shows
which file, if any, will be used for the current session cwd.

## File Attachments

`acp-agent /attach PATH [...]` extracts local documents and queues them for the
next prompt. Successful prompt submission clears the queue; failed submission
restores it. Extracted content is capped by `filecontext.MaxExtractedChars`.

Supported extraction:

- UTF-8 text-like files, including Markdown, plain text, JSON, YAML, CSV, and
  source files.
- PDF through `pdftotext` when available, with a printable-string fallback for
  simple PDFs.

Use `/files` to inspect the queue, `/clear-files` to drop it, and `/structure`
to inspect the session/context/attachment layout.

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
server:
  enabled: true
  listen: 127.0.0.1:8765
  pool_size: 6
  wait_size: 3
  http_listen: 127.0.0.1:8766
  grpc_listen: 127.0.0.1:8767

compaction:
  enabled: true
  native: true
  summary: true
  prune_fallback: true
  threshold_pct: 0.90
  target_pct: 0.80
  overflow_target_pct: 0.70
  preserve_turns: 10
  keep_recent_tokens: 20000
  reserve_tokens: 16384

pii:
  enabled: true
  mask: true
  disable_defaults: false
  env:
    file: ~/env
    min_length: 12
  routing:
    reject: false
    route_to: local-private-model
  patterns:
    - name: account_id
      regex: '\bACCT-[0-9]{8}\b'
      mask: '[MASK_ACCOUNT_{n}]'

sanitize:
  strip_think_tags: true
  tags: [think, thinking, reasoning, reflection]

cache:
  enabled: true
  ttl: 1h
  max_size: 10000
  models_to_cache: [gpt-*, claude-*]

inject:
  - when: "grok-*, glm-*"
    system_prompt: "Return concise operational answers."
    system_prompt_mode: prepend
    user_suffix: "\n\nReturn only the final answer."
    remove: [logprobs, top_logprobs]
    request_regex:
      - pattern: '\bSECRET:\s*\S+'
        replace: 'SECRET: [redacted]'
        roles: [user]

providers:
  router:
    kind: router
    default_model: ollama/gpt-oss:120b
    extra:
      routes_file: ${XDG_CONFIG_HOME}/agentbridge/router.yaml
```

CLI flags still take precedence over `server:` values.

Compaction applies to ACP and HTTP/A2A agent loops. AgentBridge first tries
provider-native compaction when available, then structured summary fallback,
then pruning only when `prune_fallback` is enabled. Percent values can be
written as fractions (`0.90`) or whole percentages (`90`). Environment
overrides are also available:

- `AGENTBRIDGE_COMPACTION_ENABLED`
- `AGENTBRIDGE_COMPACTION_NATIVE`
- `AGENTBRIDGE_COMPACTION_SUMMARY`
- `AGENTBRIDGE_COMPACTION_PRUNE_FALLBACK`
- `AGENTBRIDGE_COMPACTION_THRESHOLD_PCT`
- `AGENTBRIDGE_COMPACTION_TARGET_PCT`
- `AGENTBRIDGE_COMPACTION_OVERFLOW_TARGET_PCT`
- `AGENTBRIDGE_COMPACTION_PRESERVE_TURNS`
- `AGENTBRIDGE_COMPACTION_KEEP_RECENT_TOKENS`
- `AGENTBRIDGE_COMPACTION_RESERVE_TOKENS`

HTTP clients can request the same mechanism directly with
`POST /v1/responses/compact`. The request accepts `input`, `messages`, or
`previous_response_id`, plus optional `strategy` (`auto`, `native`, `summary`,
`prune`, `none`) and `target_tokens`. The response returns a replacement
message list with `strategy`, `compacted`, and token estimates.

## Safety And Request Mutation

`pii`, `sanitize`, `cache`, and `inject` are intentionally configured at the
top level because they are protocol-agnostic. See [Safety Pipeline](safety.md)
for the detailed intent, current implementation status, settings, and rollout
notes.

## Provider Cache And Reasoning Options

Several Hermes-derived provider knobs are available through provider `extra`
or the embedded template environment variables:

| Variable | Provider | Purpose |
| --- | --- | --- |
| `ANTHROPIC_PROMPT_CACHE` | `anthropic` | `on` by default; injects Anthropic `cache_control` breakpoints. |
| `ANTHROPIC_PROMPT_CACHE_TTL` | `anthropic` | `5m` by default; set `1h` for longer ephemeral cache TTL. |
| `CODEX_PROMPT_CACHE_KEY` | `codex` | Defaults to `{session_id}`; supports `{model}` and `{provider}` templates. |
| `CODEX_REASONING_EFFORT` | `codex` | Defaults to `medium`. |
| `CODEX_REASONING_SUMMARY` | `codex` | Defaults to `auto`. |
| `CODEX_BINARY_PATH` | `codex-app` | Absolute path to the Codex CLI/binary. Takes precedence over `CODEX_COMMAND`. |
| `CODEX_COMMAND` | `codex-app` | Local Codex CLI binary for `codex app-server`; defaults to `codex`. |
| `CODEX_APPROVAL_POLICY` | `codex-app` | Defaults to `never`; applied to native app-server threads. |
| `CODEX_SANDBOX` | `codex-app` | Native Codex sandbox mode such as `workspace-write` or `danger-full-access`. |
| `CODEX_REASONING_EFFORT` | `codex-app` | Native app-server turn reasoning effort. |
| `COPILOT_API_TOKEN` | `github-copilot` | Uses a pre-exchanged Copilot API token. |
| `COPILOT_GITHUB_TOKEN`, `GH_TOKEN`, `GITHUB_TOKEN` | `github-copilot` | Exchanged for a short-lived Copilot API token and cached. |
| `XAI_PROMPT_CACHE_KEY` | `xai`, `xai-oauth` | Defaults to `{session_id}`. |
| `XAI_REASONING_EFFORT` | `xai`, `xai-oauth` | Sent only to Grok models that accept `reasoning.effort`. |
| `KIMI_REASONING_EFFORT`, `KIMI_CN_REASONING_EFFORT` | `kimi-coding`, `kimi-coding-cn` | Top-level Chat Completions `reasoning_effort`; defaults to `medium`. |
| `DEEPSEEK_REASONING_EFFORT` | `deepseek` | Sent only for DeepSeek thinking-capable models; `xhigh` maps to `max`. |
| `TOGETHER_REASONING_EFFORT` | `together` | Maps to Together's `reasoning.enabled` payload. |
| `TOKENHUB_REASONING_EFFORT`, `LM_REASONING_EFFORT` | `tencent-tokenhub`, `lmstudio` | Top-level Chat Completions `reasoning_effort`. |
| `GOOGLE_CACHE_RETENTION` | `google` | `short` by default for Gemini 2.5/3; creates/refreshes native Google `cachedContents`. |
| `GOOGLE_OAUTH_ACCESS_TOKEN`, `GOOGLE_VERTEX_ACCESS_TOKEN`, `GOOGLE_CLOUD_PROJECT`, `GOOGLE_CLOUD_LOCATION` | `google-vertex`, `google-antigravity`, `anthropic-vertex` | Vertex auth/project/region. If no token env is set, AgentBridge tries `gcloud auth application-default print-access-token`. |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `AWS_REGION` | `amazon-bedrock` | Bedrock Converse SigV4 credentials and region. |
| `OPENAI_RESPONSES_SERVER_COMPACTION` | `openai-responses` | Enables OpenAI Responses `context_management` compaction hints. |
| `OPENAI_RESPONSES_COMPACT_THRESHOLD` | `openai-responses` | Optional `context_management.compact_threshold`; defaults to 70% of context. |
| `OPENAI_PROMPT_CACHE_KEY` | `openai-responses` | Defaults to `{session_id}`; supports `{model}` and `{provider}` templates. |
| `OPENAI_PROMPT_CACHE_RETENTION` | `openai-responses` | Optional upstream `prompt_cache_retention`. |

OpenAI-chat providers can also use Anthropic-style `cache_control`
breakpoints when the upstream supports them. AgentBridge auto-enables this
for OpenRouter/Nous Claude routes and Qwen routes through Alibaba/OpenCode/Nous.
For a custom OpenAI-compatible provider, set `extra.prompt_cache: on`; set
`extra.prompt_cache_ttl: 1h` when the upstream supports the longer TTL.
OpenRouter response caching can be controlled with provider `extra` keys
`response_cache`, `response_cache_ttl`, and `response_cache_clear`, which map
to the `X-OpenRouter-Cache*` request headers.

HTTP `/v1/chat/completions`, `/v1/responses`, Anthropic-compatible `/v1/messages`,
and A2A calls can also pass `metadata.prompt_cache_key`,
`metadata.prompt_cache_retention`, `metadata.cache_retention`,
`metadata.service_tier`, `metadata.reasoning_effort`, or a session id
(`metadata.session_id`, `sessionId`, or `thread_id`) for per-request routing.
`/v1/responses` additionally maps its top-level `prompt_cache_key` and
`prompt_cache_retention` into the provider request.

## Experimental Intention Probe

AgentBridge includes an opt-in experimental classifier endpoint:
`POST /experimental/intention`. Enable it with either:

```bash
AGENTBRIDGE_EXPERIMENTAL_INTENTION_PROBE=1
# or
AGENTBRIDGE_EXPERIMENTS=intention_probe
```

The endpoint asks a capable upstream to choose among labeled options and
computes confidence from first-token `top_logprobs`. It currently supports the
`openai-chat` provider shape when the upstream actually returns Chat
Completions logprobs, and `router` forwards to such providers. It is not a
general answer-confidence metric.

```bash
curl -s http://127.0.0.1:8766/experimental/intention \
  -H 'content-type: application/json' \
  -d '{"model":"openai/gpt-4.1","prompt":"Which city is the capital of South Korea?","choices":["Seoul","Busan"]}'
```

Providers that accept the logprobs parameters but omit logprobs in the
response, such as Ollama Cloud in current testing, return an unsupported
upstream error.

## Embedding Model Mapping

`openai_embed` can route multiple user-facing embedding aliases to different
OpenAI-compatible upstreams. Prefer keeping those routes beside the model
router in `config.yaml`:

```yaml
providers:
  router:
    extra:
      embeddings:
        default: jina-embeddings-v5-text-nano
        models:
          embeddinggemma-300m:
            base_url: http://127.0.0.1:28080/v1
            model: embeddinggemma-300m
            provider: local
          pplx-embed-v1-0.6b:
            base_url: https://openrouter.ai/api/v1
            api_key_env: OPENROUTER_API_KEY
            model: perplexity/pplx-embed-v1-0.6b
            provider: openrouter
```

The map key is the public model ID accepted by `POST /v1/embeddings` and
returned from `GET /v1/models`. `model` is the upstream model ID.
`provider` or `owned_by` controls the OpenAI-compatible `owned_by` field.

Chat provider `models:` entries can also include metadata returned in
`GET /v1/models`: `provider`, `api`, `base_url`, `input`, `reasoning`,
`context_window`, `context_tokens`, `max_tokens`, `status`, `aliases`, `tags`,
`compat`, and `cost`. This keeps aliases such as `grok-4` owned by `xai`
instead of exposing generic pseudo-models such as `grok`.

## Agent Runtime Controls

HTTP agent-loop destructive tool policy is controlled by runtime config:

```yaml
agent:
  yolo_mode: false
```

When `yolo_mode` is `true`, write and command tools run in
`bypass_permissions` mode. When explicitly `false`, non-interactive HTTP agent
loops reject write/execute permission requests by default, while read-only
tools such as `read_file` and `list_files` still run. If `yolo_mode` is omitted,
AgentBridge keeps the legacy default of bypassing permissions for compatibility.

Advanced overrides:

```yaml
agent:
  permission_mode: accept_edits   # default, accept_edits, bypass_permissions, read_only
  permission_decision: reject     # allow, reject, cancel
```

Request metadata may also set `permission_mode` / `permission_decision` for a
single HTTP agent-loop turn. Treat request-level overrides as trusted-client
controls; deployment policy should live in the runtime config.

## Agent Profiles

Agent profiles are virtual models: selecting the profile name in ACP uses a
target upstream model, injects an extra system prompt, and optionally filters
the available tools. They are also included in the OpenAI-compatible
`GET /v1/models` list. Set `AGENTBRIDGE_AGENTS_FILE`, or place
`agents.yaml` / `agents.json` under `$XDG_CONFIG_HOME/agentbridge`.

```yaml
agents:
  - name: foo
    description: Foo coding agent
    target_model: glm-5.1
    prompt_file: prompts/foo.md
    tools:
      - read_file
      - list_files
      - mcp__search__*
      - plugin__jina__jina_search
```

`system_prompt` may be used inline instead of, or in addition to,
`prompt_file`. Relative `prompt_file` paths resolve from the profile file's
directory.

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
    inject_tools:
      - name: forced_search
        source_name: search
        description: Search through the upstream MCP server.
        inputSchema:
          type: object
          properties:
            query:
              type: string
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
Use `inject_tools` to force additional ACP/MCP tool definitions even when the
upstream server does not advertise them in `tools/list`. Injected tools are
exposed as `mcp__<server>__<name>` and call `source_name` upstream.
Use `GET /v1/tool-catalog` to inspect builtin, plugin, and MCP tools. Use
`GET /v1/mcp/catalog` when you only need configured MCP server details. Secret
headers are redacted in catalog responses.

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

`max_concurrent_per_key` limits simultaneous sessions for each key selected by
`api_key_envs` or `api_keys`. For example, with two Ollama Cloud keys and
`max_concurrent_per_key: 3`, the route can run up to six concurrent upstream
sessions, three on each key. This is separate from quota detection: concurrency
caps prevent local overuse, while quota detection reacts to upstream errors and
quota headers/messages.

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
