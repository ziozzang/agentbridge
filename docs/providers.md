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

## Examples

OpenAI:

```bash
AGENTBRIDGE_PROVIDER=openai \
AGENTBRIDGE_API_KEY=example-api-key \
AGENTBRIDGE_MODEL=gpt-4.1-mini \
agentbridge
```

Anthropic:

```bash
AGENTBRIDGE_PROVIDER=anthropic \
AGENTBRIDGE_ANTHROPIC_API_KEY=example-anthropic-key \
AGENTBRIDGE_MODEL=claude-sonnet-4-5 \
agentbridge
```

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
