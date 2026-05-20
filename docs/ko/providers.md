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
| `xai` | `openai-responses` | `XAI_API_KEY`를 쓰는 xAI Grok Responses API. |
| `xai-oauth` | `openai-responses` | `~/.grok/auth.json`의 xAI Grok OAuth bearer 사용. |
| `zai` | `openai-chat` | Hermes 호환 Z.AI/GLM direct API template. |
| `kimi-coding` | `openai-chat` | Kimi Coding Plan OpenAI 호환 endpoint. |
| `kimi-coding-cn` | `openai-chat` | Kimi/Moonshot China OpenAI 호환 endpoint. |
| `deepseek` | `openai-chat` | DeepSeek direct API. |
| `stepfun` | `openai-chat` | StepFun Step Plan. |
| `alibaba` | `openai-chat` | Alibaba DashScope compatible-mode API. |
| `alibaba-coding-plan` | `openai-chat` | Alibaba Coding Plan endpoint. |
| `nvidia` | `openai-chat` | NVIDIA NIM OpenAI 호환 endpoint. |
| `ai-gateway` | `openai-chat` | Vercel AI Gateway. |
| `opencode-zen` | `openai-chat` | OpenCode Zen gateway. |
| `opencode-go` | `openai-chat` | OpenCode Go gateway의 OpenAI 호환 모델. |
| `kilocode` | `openai-chat` | Kilo Code gateway. |
| `huggingface` | `openai-chat` | Hugging Face Inference Providers router. |
| `novita` | `openai-chat` | Novita OpenAI 호환 router. |
| `arcee` | `openai-chat` | Arcee AI direct API. |
| `gmi` | `openai-chat` | GMI Cloud OpenAI 호환 endpoint. |
| `xiaomi` | `openai-chat` | Xiaomi MiMo API. |
| `tencent-tokenhub` | `openai-chat` | Tencent TokenHub API. |
| `ollama-cloud` | `openai-chat` | Ollama Cloud OpenAI 호환 API. |
| `lmstudio` | `openai-chat` | 로컬 LM Studio OpenAI 호환 server. |

## Hermes 기반 template

AgentBridge에는 Hermes Agent provider registry를 기준으로, 현재
AgentBridge transport와 바로 맞는 provider template을 포함합니다. 이 항목들은
설정만 추가된 통합입니다. `openai-chat`, `openai-responses`, 기존 native
provider를 재사용하며 Hermes credential을 저장하지 않습니다.

예시:

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

가능하면 provider별 `*_BASE_URL`, `*_API_KEY`, `*_MODEL` 변수를 사용하세요.
YAML 해석 후에도 `AGENTBRIDGE_<PROVIDER>_API_KEY` override는 계속 동작합니다.

아래 Hermes 항목은 추가 transport/auth 구현이 필요해서 아직 기본 template으로
활성화하지 않았습니다.

| Hermes provider | 남은 이유 |
| --- | --- |
| `nous` | Device-code OAuth와 scoped inference token minting 필요. |
| `qwen-oauth` | Qwen OAuth token refresh/store 통합 필요. |
| `google-gemini-cli` | 단순 HTTP base URL이 아니라 Cloud Code Assist OAuth transport. |
| `copilot-acp` | 외부 ACP process transport 필요. |
| `github-copilot` | Copilot token/catalog 처리 필요. |
| `bedrock` | AWS SigV4와 Bedrock Converse transport 필요. |
| `minimax`, `minimax-cn`, `minimax-oauth` | Anthropic 호환 path와 OAuth가 현재 Anthropic direct adapter보다 추가 처리를 요구. |
| `azure-foundry` | 배포별 endpoint와 API mode가 달라 사용자 설정이 필요. |

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

Codex provider는 Codex CLI의 현재 기본값과 맞춰 native web search를 cached
mode로 켭니다. 다음 환경 변수로 조정할 수 있습니다.

```bash
CODEX_WEB_SEARCH=live \
CODEX_WEB_SEARCH_CONTEXT_SIZE=high \
CODEX_WEB_SEARCH_COUNTRY=KR \
CODEX_WEB_SEARCH_CITY=Seoul \
CODEX_WEB_SEARCH_TIMEZONE=Asia/Seoul \
AGENTBRIDGE_PROVIDER=codex agentbridge
```

`CODEX_WEB_SEARCH` 값은 `live`, `cached`, `disabled`를 지원합니다.
`CODEX_WEB_SEARCH_ALLOWED_DOMAINS`는 쉼표로 구분된 allowlist입니다.

사용자 정의 OpenAI Responses provider에서도 같은 wire shape을 `extra`에
설정할 수 있습니다.

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

AgentBridge의 Grok OAuth 기본 저장소는 `~/.grok/auth.json`입니다. 파일은
Hermes와 호환되는 provider entry shape을 사용합니다.

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

resolver는 JWT access token이 만료에 가까우면 xAI public OAuth client
(`b1a00492-073a-47ea-816f-4c329264a828`)로 refresh합니다. 이전 중간 단계와
호환하기 위해 `~/.grok/auth.json`이 없으면 Hermes의 `~/.hermes/auth.json`도
읽습니다. 브라우저 PKCE 로그인 자체는 아직 외부에서 수행해야 합니다.
`hermes auth add xai-oauth`로 로그인한 뒤 auth store를 가져오거나,
`AGENTBRIDGE_XAI_OAUTH_ACCESS_TOKEN` /
`AGENTBRIDGE_XAI_OAUTH_REFRESH_TOKEN`을 직접 설정하세요.

upstream xAI/Hermes flow에서 확인한 값:

- Authorization server: `https://accounts.x.ai` / `https://auth.x.ai`
- Redirect: `http://127.0.0.1:56121/callback`
- Scope: `openid profile email offline_access grok-cli:access api:access`
- OAuth API access는 구독/tier gating으로 HTTP 403이 날 수 있습니다. 이 경우
  `XAI_API_KEY` 기반 `xai` provider를 사용하세요.
