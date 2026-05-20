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
