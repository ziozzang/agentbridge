# Providers

AgentBridge routes every protocol surface through a common provider
interface. Select one with `AGENTBRIDGE_PROVIDER=<name>`.

## Built-In Providers

| Name | Kind | Notes |
| --- | --- | --- |
| `glm` | `glm` | GLM/Z.AI Coding Plan. Default provider and default model `glm-5.1`. |
| `openai` | `openai-chat` | OpenAI Chat Completions and compatible gateways. |
| `openai-responses` | `openai-responses` | OpenAI Responses API. |
| `anthropic` | `anthropic` | Anthropic Messages API. |
| `claude-code` | `claude-code-cli` | Claude Code CLI one-shot adapter. |
| `ollama` | `ollama` | Native Ollama `/api/chat`. |
| `openrouter` | `openai-chat` | OpenRouter Chat Completions. |
| `litellm` | `openai-chat` | LiteLLM proxy or any OpenAI-compatible gateway. |
| `codex` | `openai-responses` | ChatGPT Codex backend via Codex/OpenAI OAuth. |
| `xai` | `openai-responses` | xAI Grok Responses API with `XAI_API_KEY`. |
| `xai-oauth` | `openai-responses` | xAI Grok OAuth bearer from `~/.grok/auth.json`. |
| `zai` | `openai-chat` | Hermes-compatible Z.AI/GLM direct API template. |
| `kimi-coding` | `openai-chat` | Kimi Coding Plan OpenAI-compatible endpoint. |
| `kimi-coding-cn` | `openai-chat` | Kimi/Moonshot China OpenAI-compatible endpoint. |
| `deepseek` | `openai-chat` | DeepSeek direct API. |
| `stepfun` | `openai-chat` | StepFun Step Plan. |
| `alibaba` | `openai-chat` | Alibaba DashScope compatible-mode API. |
| `alibaba-coding-plan` | `openai-chat` | Alibaba Coding Plan endpoint. |
| `nvidia` | `openai-chat` | NVIDIA NIM OpenAI-compatible endpoint. |
| `ai-gateway` | `openai-chat` | Vercel AI Gateway. |
| `opencode-zen` | `openai-chat` | OpenCode Zen gateway. |
| `opencode-go` | `openai-chat` | OpenCode Go gateway for OpenAI-compatible models. |
| `kilocode` | `openai-chat` | Kilo Code gateway. |
| `huggingface` | `openai-chat` | Hugging Face Inference Providers router. |
| `novita` | `openai-chat` | Novita OpenAI-compatible router. |
| `arcee` | `openai-chat` | Arcee AI direct API. |
| `gmi` | `openai-chat` | GMI Cloud OpenAI-compatible endpoint. |
| `xiaomi` | `openai-chat` | Xiaomi MiMo API. |
| `tencent-tokenhub` | `openai-chat` | Tencent TokenHub API. |
| `ollama-cloud` | `openai-chat` | Ollama Cloud OpenAI-compatible API. |
| `lmstudio` | `openai-chat` | Local LM Studio OpenAI-compatible server. |

## Hermes-Derived Templates

AgentBridge includes provider templates derived from Hermes Agent's provider
registry for entries that are already compatible with AgentBridge transports.
These are config-only integrations: they reuse `openai-chat`,
`openai-responses`, or the existing native providers and do not embed Hermes
credentials.

Common examples:

```bash
AGENTBRIDGE_PROVIDER=kimi-coding \
KIMI_API_KEY=... \
KIMI_MODEL=kimi-k2.6 \
agentbridge
```

```bash
AGENTBRIDGE_PROVIDER=deepseek \
DEEPSEEK_API_KEY=... \
DEEPSEEK_MODEL=deepseek-chat \
agentbridge
```

```bash
AGENTBRIDGE_PROVIDER=opencode-go \
OPENCODE_GO_API_KEY=... \
OPENCODE_GO_MODEL=kimi-k2.6 \
agentbridge
```

Provider-specific `*_BASE_URL`, `*_API_KEY`, and `*_MODEL` variables are
preferred where available. `AGENTBRIDGE_<PROVIDER>_API_KEY` still works as a
last-mile override after the YAML is resolved.

Hermes entries that still need additional AgentBridge implementation are not
enabled as default templates yet:

| Hermes provider | Reason |
| --- | --- |
| `nous` | Device-code OAuth and scoped inference token minting. |
| `qwen-oauth` | Qwen OAuth token refresh/store integration. |
| `google-gemini-cli` | Cloud Code Assist OAuth transport, not a plain HTTP base URL. |
| `copilot-acp` | External ACP process transport. |
| `github-copilot` | Copilot token/catalog handling. |
| `bedrock` | AWS SigV4 and Bedrock Converse transport. |
| `minimax`, `minimax-cn`, `minimax-oauth` | Anthropic-compatible paths and OAuth need endpoint/header handling beyond the current Anthropic direct adapter. |
| `azure-foundry` | User endpoint and API mode vary by deployment. |

## Provider API and Tool Matrix

This table separates model-provider APIs from optional plugin tools. Plugin
tools can also be exposed directly through MCP `POST /mcp` and `/v1/mcp`.

| Provider / plugin | Auth | Provider APIs | AgentBridge tools |
| --- | --- | --- | --- |
| `glm` | `Z_AI_API_KEY` / `AGENTBRIDGE_API_KEY` | ACP chat, Chat Completions-compatible GLM route | Built-in file/shell/web tools, Z.AI MCP web tools |
| `zai` | `GLM_API_KEY`, `ZAI_API_KEY`, `Z_AI_API_KEY` | OpenAI Chat Completions | Built-in agent tools |
| `openai` | `OPENAI_API_KEY` | OpenAI Chat Completions | Built-in agent tools |
| `openai-responses` | `OPENAI_API_KEY` | OpenAI Responses | Hosted `web_search` when configured in provider `extra` |
| `codex` | Codex OAuth from `~/.codex/auth.json` | ChatGPT Codex Responses backend | Codex hosted `web_search`, prompt cache metadata |
| `xai` | `XAI_API_KEY` | xAI Responses-compatible Grok | xAI hosted `x_search` when used through plugin |
| `xai-oauth` | `~/.grok/auth.json`, fallback `~/.hermes/auth.json` | xAI Responses-compatible Grok | Same OAuth token can be reused by `xai` plugin |
| `anthropic` | `ANTHROPIC_API_KEY` | Anthropic Messages | Built-in agent tools |
| `claude-code` | Claude Code CLI auth | Claude CLI one-shot adapter | Claude CLI tool policy passthrough |
| `ollama` | optional `OLLAMA_API_KEY` | Native Ollama `/api/chat` | Built-in agent tools |
| `openrouter` | `OPENROUTER_API_KEY` | OpenAI Chat Completions gateway | Built-in agent tools |
| `litellm` | `LITELLM_API_KEY` | OpenAI Chat Completions gateway | Use `openai_embed` plugin for `/embeddings` tests |
| `kimi-coding`, `kimi-coding-cn` | `KIMI_API_KEY`, `KIMI_CODING_API_KEY`, `KIMI_CN_API_KEY` | OpenAI Chat Completions | Built-in agent tools |
| `deepseek` | `DEEPSEEK_API_KEY` | OpenAI Chat Completions | Built-in agent tools |
| `stepfun` | `STEPFUN_API_KEY` | OpenAI Chat Completions | Built-in agent tools |
| `alibaba`, `alibaba-coding-plan` | `DASHSCOPE_API_KEY`, `ALIBABA_CODING_PLAN_API_KEY` | OpenAI Chat Completions | Built-in agent tools |
| `nvidia` | `NVIDIA_API_KEY` | OpenAI Chat Completions | Built-in agent tools |
| `ai-gateway`, `opencode-zen`, `opencode-go`, `kilocode` | gateway-specific API key | OpenAI Chat Completions gateway | Built-in agent tools |
| `huggingface`, `novita`, `arcee`, `gmi`, `xiaomi`, `tencent-tokenhub`, `ollama-cloud`, `lmstudio` | provider-specific API key or local key | OpenAI Chat Completions-compatible | Built-in agent tools |
| `plugin:jina` | optional `JINA_API_KEY` | Jina Reader, Search, Embeddings, Rerank | `jina_reader`, `jina_search`, `jina_embed`, `jina_rerank`; HTTP `/v1/embeddings`, `/v1/rerank` |
| `plugin:ollama_search` | `OLLAMA_API_KEY` | Ollama Cloud web search/fetch | `ollama_search`, `ollama_fetch` |
| `plugin:xai` | xAI OAuth or `XAI_API_KEY` | xAI Responses `x_search`, Images generations/edits | `xai_x_search`, `xai_image_generate`, `xai_image_edit` |
| `plugin:openai_embed` | `AGENTBRIDGE_EMBEDDINGS_API_KEY` or mapped key envs | OpenAI-compatible `/embeddings` gateways | `embed`; HTTP `/v1/embeddings`; aliases from router `extra.embeddings` |
| `plugin:sqlite` | local filesystem | SQLite catalog/query | `sqlite_list`, `sqlite_load`, `sqlite_unload`, `sqlite_tables`, `sqlite_schema`, `sqlite_query`, `sqlite_exec` when enabled |
| `plugin:duckdb` | local process | Reserved placeholder | `duckdb_status` |

## Model Router

`router` is a LiteLLM-style provider frontend. AgentBridge does not hardcode
router model mappings; keep them in `config.yaml`, `router.yaml`, or another
file selected with `AGENTBRIDGE_ROUTER_FILE`. Select the router once, then
route by the requested model name:

```bash
AGENTBRIDGE_PROVIDER=router agentbridge --http-listen 127.0.0.1:8766
```

Put routes in `$XDG_CONFIG_HOME/agentbridge/config.yaml`:

```yaml
providers:
  router:
    kind: router
    default_model: ollama/gpt-oss:120b
    extra:
      routes:
        - match: ollama/*
          provider: ollama-cloud
          target_model: "$1"
          api_key_envs:
            - OLLAMA_API_KEY_A
            - OLLAMA_API_KEY_B
        - match: grok
          provider: xai
          target_model: grok-4.3
        - match: zai:*
          provider: zai
          target_model: "$1"
```

For the `ollama/*` route above, calls to `model=ollama/gpt-oss:120b` use
`target_model=gpt-oss:120b` and rotate keys as
`OLLAMA_API_KEY_A`, `OLLAMA_API_KEY_B`, `OLLAMA_API_KEY_A`, and so on. Use
`api_keys` only for local/private files; prefer `api_key_envs` so secrets do
not enter version control.

Routes can also live in `$XDG_CONFIG_HOME/agentbridge/router.yaml`,
`router.json`, or a file selected by `AGENTBRIDGE_ROUTER_FILE`:

```bash
AGENTBRIDGE_PROVIDER=router \
AGENTBRIDGE_ROUTER_FILE=$XDG_CONFIG_HOME/agentbridge/router.yaml \
agentbridge
```

```yaml
default_model: ollama/gpt-oss:120b
routes:
  - match: ollama/*
    provider: ollama-cloud
    target_model: "$1"
    api_key_envs: [OLLAMA_API_KEY_A, OLLAMA_API_KEY_B]
    retry_keys: true
  - match: glm-5.1
    provider: zai
    target_model: glm-5.1
    fallbacks:
      - provider: zai
        target_model: glm-5-turbo
  - match: grok
    provider: xai
    target_model: grok-4.3
  - match: zai:*
    provider: zai
    target_model: "$1"
  - models: "*"
    provider: openrouter
    target_model: "$model"
```

`api_key_envs` and `api_keys` accept either YAML/JSON lists or delimited
strings:

```yaml
api_key_envs: OLLAMA_API_KEY_A, OLLAMA_API_KEY_B
```

When `retry_keys: true` is set, the router detects 429/quota/weekly-limit/5h
limit style errors before any streamed output is emitted, marks that key
limited for the current process, and retries the next configured key. This is
best-effort because providers differ in how they report reset times.

See [Configuration](configuration.md#router-route-schema) for the full route
schema and precedence rules.

## Examples

OpenAI:

```bash
AGENTBRIDGE_PROVIDER=openai \
AGENTBRIDGE_API_KEY=example-api-key \
AGENTBRIDGE_MODEL=gpt-4.1-mini \
agentbridge
```

OpenAI Responses native conversation compaction is enabled by default via
`/v1/responses/compact`, matching Codex's remote-compaction capability for
OpenAI-family Responses providers. Override or disable it with:

```bash
OPENAI_COMPACTION=disabled AGENTBRIDGE_PROVIDER=openai-responses agentbridge
OPENAI_COMPACT_PATH=/v1/responses/compact AGENTBRIDGE_PROVIDER=openai-responses agentbridge
```

Anthropic:

```bash
AGENTBRIDGE_PROVIDER=anthropic \
AGENTBRIDGE_ANTHROPIC_API_KEY=example-anthropic-key \
AGENTBRIDGE_MODEL=claude-sonnet-4-5 \
agentbridge
```

Anthropic prompt caching is enabled by default for the native Anthropic
adapter. AgentBridge marks the system prompt plus the last three non-system
messages with `cache_control`, matching the Hermes `system_and_3` strategy.
Use `ANTHROPIC_PROMPT_CACHE=off` to disable it, or
`ANTHROPIC_PROMPT_CACHE_TTL=1h` for the longer ephemeral TTL.

GLM/Z.AI:

```bash
AGENTBRIDGE_PROVIDER=glm \
Z_AI_API_KEY=... \
AGENTBRIDGE_GLM_MODEL=glm-5.1 \
agentbridge
```

Ollama:

```bash
AGENTBRIDGE_PROVIDER=ollama \
OLLAMA_BASE_URL=http://127.0.0.1:11434 \
OLLAMA_MODEL=llama3.1 \
agentbridge
```

OpenAI-compatible gateway:

```bash
AGENTBRIDGE_PROVIDER=openai \
AGENTBRIDGE_BASE_URL=http://localhost:4000/v1 \
AGENTBRIDGE_API_KEY=anything \
AGENTBRIDGE_MODEL=kimi-k2.6 \
agentbridge
```

Codex OAuth:

```bash
AGENTBRIDGE_PROVIDER=codex agentbridge
```

The Codex provider reads Codex CLI auth from `~/.codex/auth.json` or
`AGENTBRIDGE_CODEX_*` token overrides.

Codex native conversation compaction is enabled by default via the
`/responses/compact` endpoint. Override or disable it with:

```bash
CODEX_COMPACTION=disabled AGENTBRIDGE_PROVIDER=codex agentbridge
CODEX_COMPACT_PATH=/responses/compact AGENTBRIDGE_PROVIDER=codex agentbridge
```

Hermes/OpenAI Codex source currently treats native remote compaction as
available for OpenAI and Azure Responses providers. xAI/Grok also uses a
Responses-shaped transport in Hermes, but is not marked as remote-compaction
capable there, so AgentBridge leaves xAI on the generic summary fallback.

Codex prompt caching follows the current session by default:
`CODEX_PROMPT_CACHE_KEY` defaults to `{session_id}` and can also interpolate
`{model}` and `{provider}`. Reasoning defaults to
`CODEX_REASONING_EFFORT=medium` with `CODEX_REASONING_SUMMARY=auto`, and
encrypted reasoning content is requested so multi-turn Codex sessions can
reuse provider-private reasoning state.

Codex-style native web search is enabled for the Codex provider by default in
cached mode, matching Codex CLI's current default. Override it with:

```bash
CODEX_WEB_SEARCH=live \
CODEX_WEB_SEARCH_CONTEXT_SIZE=high \
CODEX_WEB_SEARCH_COUNTRY=KR \
CODEX_WEB_SEARCH_CITY=Seoul \
CODEX_WEB_SEARCH_TIMEZONE=Asia/Seoul \
AGENTBRIDGE_PROVIDER=codex agentbridge
```

Supported `CODEX_WEB_SEARCH` values are `live`, `cached`, and `disabled`.
`CODEX_WEB_SEARCH_ALLOWED_DOMAINS` accepts a comma-separated allowlist.

For custom OpenAI Responses providers, the same wire shape is available under
provider `extra`:

```yaml
providers:
  my-responses:
    kind: openai-responses
    extra:
      web_search: live
      tools:
        web_search:
          context_size: high
          allowed_domains: openai.com,github.com
          location:
            country: KR
            city: Seoul
            timezone: Asia/Seoul
```

xAI Grok API key:

```bash
AGENTBRIDGE_PROVIDER=xai \
XAI_API_KEY=xai-... \
XAI_MODEL=grok-4.3 \
agentbridge
```

xAI Grok OAuth:

```bash
AGENTBRIDGE_PROVIDER=xai-oauth agentbridge
```

xAI Responses requests also use a session-scoped `prompt_cache_key` by
default. `XAI_REASONING_EFFORT` is sent only to Grok models known to accept
the `reasoning.effort` field; for other Grok models AgentBridge omits the
field to avoid xAI HTTP 400 responses while still letting the model reason
natively.

AgentBridge expects Grok OAuth credentials in `~/.grok/auth.json`. The file
uses the Hermes-compatible provider entry shape:

```json
{
  "providers": {
    "xai-oauth": {
      "tokens": {
        "access_token": "...",
        "refresh_token": "..."
      },
      "discovery": {
        "token_endpoint": "https://auth.x.ai/oauth2/token"
      }
    }
  }
}
```

The resolver refreshes expiring JWT access tokens with xAI's public OAuth
client (`b1a00492-073a-47ea-816f-4c329264a828`). During migration it also
accepts Hermes' `~/.hermes/auth.json` if `~/.grok/auth.json` does not exist.
Interactive browser PKCE login is still external; use Hermes
`hermes auth add xai-oauth` and copy/import the auth store, or provide
`AGENTBRIDGE_XAI_OAUTH_ACCESS_TOKEN` / `AGENTBRIDGE_XAI_OAUTH_REFRESH_TOKEN`.

Notes from the upstream xAI/Hermes flow:

- Authorization server: `https://accounts.x.ai` / `https://auth.x.ai`
- Redirect: `http://127.0.0.1:56121/callback`
- Scope: `openid profile email offline_access grok-cli:access api:access`
- OAuth API access can return HTTP 403 for subscription/tier gating. In that
  case use the `xai` provider with `XAI_API_KEY`.

## Provider YAML

Built-in templates live in `internal/config/providers.yaml`. You can add or
override providers with:

```bash
AGENTBRIDGE_PROVIDERS_FILE=/path/to/providers.yaml agentbridge
```

or:

```text
$XDG_CONFIG_HOME/agentbridge/providers.yaml
```

Legacy `ACP_HARNESS_*` provider variables are still supported.
