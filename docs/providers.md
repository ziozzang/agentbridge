# Providers

The harness supports many upstream LLM APIs through a common `Provider`
interface. Pick one by setting `ACP_HARNESS_PROVIDER=<name>` and giving it
an API key.

## Built-in templates

The list lives in `internal/config/providers.yaml` and is loaded
automatically. Each template has the shape:

```yaml
<name>:
  kind: <wire-protocol-kind>
  base_url: <default base URL>
  default_model: <model id>
  available_models: [<…>]
  extra_headers:
    Key: Value
```

### `glm` *(default)*

GLM/Z.AI Coding Plan. Uses `openai-chat` wire format with the GLM
`thinking` flag injected for reasoning models.

- Env: `ACP_HARNESS_GLM_API_KEY` or back-compat `Z_AI_API_KEY`.
- Default model: `glm-5.1`.
- Context window: 128K (`glm-5.1`/`glm-5-turbo`), 200K (`glm-4.7`).

### `openai`

OpenAI Chat Completions (`/v1/chat/completions`). Used by OpenAI itself,
LiteLLM, OpenRouter, vLLM, Ollama's OpenAI compatibility layer, and a
host of other gateways.

- Env: `ACP_HARNESS_OPENAI_API_KEY` or `OPENAI_API_KEY`.
- Default model: `gpt-4o-mini`.

### `openai-responses`

OpenAI **Responses** API (`/v1/responses`). This is the new shape used
by Codex/o1-style reasoning models. The adapter speaks the
`response.output_item.added` / `response.completed` event stream.

- Env: `ACP_HARNESS_OPENAI_RESPONSES_API_KEY` or
  `ACP_HARNESS_OPENAI_API_KEY`.
- Default model: `gpt-5`.

### `anthropic`

Anthropic Messages API (`/v1/messages`). Maps tool-use / tool-result
content blocks to the harness's neutral `Message` shape and back.

- Env: `ACP_HARNESS_ANTHROPIC_API_KEY` or `ANTHROPIC_API_KEY`.
- Header: `x-api-key`, `anthropic-version: 2023-06-01`.
- Default model: `claude-sonnet-4-5`.

### `ollama`

Ollama's native API (`/api/chat`). NDJSON streaming.

- Env: usually no API key. Base URL defaults to
  `http://127.0.0.1:11434`.
- Default model: `llama3.1`.

If you'd rather use Ollama's OpenAI-compatible endpoint, use
`openai` with `ACP_HARNESS_OPENAI_BASE_URL=http://localhost:11434/v1`
instead.

### `openrouter`

OpenRouter (https://openrouter.ai/). Wire protocol is OpenAI Chat.

- Env: `ACP_HARNESS_OPENROUTER_API_KEY` or `OPENROUTER_API_KEY`.
- Default model: `openai/gpt-5`.

### `litellm`

LiteLLM proxy (https://docs.litellm.ai/). Wire protocol is OpenAI Chat.

- Env: `ACP_HARNESS_LITELLM_API_KEY`, `ACP_HARNESS_LITELLM_BASE_URL`.

### `codex`

OpenAI's Codex CLI flow: the Responses API authenticated with an
OAuth-refreshable access token (rather than a long-lived `sk-...`).

- Env: `ACP_HARNESS_CODEX_REFRESH_TOKEN`,
  `ACP_HARNESS_CODEX_ACCESS_TOKEN`,
  `ACP_HARNESS_CODEX_TOKEN_FILE`.
- The template sets `api_key: oauth:codex`, which makes the harness
  invoke the resolver in `internal/oauth/codex` to mint a Bearer token.
- The resolver also reads the standard `~/.codex/auth.json` shape used by
  Codex CLI when no harness token file is configured.
- OpenAI providers can use the same flow with `api_key: oauth:openai` or
  `ACP_HARNESS_API_KEY=oauth:openai`. The OpenAI-specific env names are
  `ACP_HARNESS_OPENAI_ACCESS_TOKEN`,
  `ACP_HARNESS_OPENAI_REFRESH_TOKEN`, and
  `ACP_HARNESS_OPENAI_TOKEN_FILE`.

## Picking a provider

```bash
# OpenAI hosted
ACP_HARNESS_PROVIDER=openai \
ACP_HARNESS_API_KEY=sk-... \
ACP_HARNESS_MODEL=gpt-4o ./glm-acp-agent

# Anthropic
ACP_HARNESS_PROVIDER=anthropic \
ACP_HARNESS_API_KEY=sk-ant-... \
ACP_HARNESS_MODEL=claude-sonnet-4-5 ./glm-acp-agent

# Local Ollama
ACP_HARNESS_PROVIDER=ollama \
ACP_HARNESS_MODEL=llama3.1 ./glm-acp-agent

# OpenAI Responses API (reasoning models)
ACP_HARNESS_PROVIDER=openai-responses \
ACP_HARNESS_API_KEY=sk-... \
ACP_HARNESS_MODEL=o3 ./glm-acp-agent
```

## Adding a custom provider

See **AGENTS.md → "How to add a new provider"**. Short version:

1. Implement `provider.Provider` in `internal/provider/<kind>/`.
2. `provider.Register("<kind>", New)` in `init()`.
3. Add a template entry to `internal/config/providers.yaml`, or define
   it in your user YAML file.
4. Side-effect-import the package from `internal/agent/agent.go`.
