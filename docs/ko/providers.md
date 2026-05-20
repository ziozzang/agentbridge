# 프로바이더

AgentBridge는 모든 프로토콜 표면을 공통 provider interface로 라우팅합니다.
`AGENTBRIDGE_PROVIDER=<name>`으로 활성 provider를 선택합니다.

## 내장 provider

| 이름 | 종류 | 설명 |
| --- | --- | --- |
| `glm` | `glm` | GLM/Z.AI Coding Plan. 기본 provider, 기본 모델 `glm-5.1`. |
| `openai` | `openai-chat` | OpenAI Chat Completions와 호환 gateway. |
| `openai-responses` | `openai-responses` | OpenAI Responses API. |
| `anthropic` | `anthropic` | Anthropic Messages API. |
| `claude-code` | `claude-code-cli` | Claude Code CLI adapter. |
| `ollama` | `ollama` | Ollama native `/api/chat`. |
| `openrouter` | `openai-chat` | OpenRouter Chat Completions. |
| `litellm` | `openai-chat` | LiteLLM proxy 또는 OpenAI 호환 gateway. |
| `codex` | `openai-responses` | Codex/OpenAI OAuth 기반 ChatGPT Codex backend. |

## 예시

OpenAI:

```bash
AGENTBRIDGE_PROVIDER=openai \
AGENTBRIDGE_API_KEY=example-api-key \
AGENTBRIDGE_MODEL=gpt-4.1-mini \
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

OpenAI 호환 gateway:

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

Codex provider는 `~/.codex/auth.json` 또는 `AGENTBRIDGE_CODEX_*` token
override를 사용합니다.
