# 프로바이더

AgentBridge는 모든 프로토콜 표면을 공통 provider interface로 라우팅합니다.
`AGENTBRIDGE_PROVIDER=<name>`으로 활성 provider를 선택합니다.

이제 agent loop는 두 가지 방식으로 나뉩니다.

- 일반 LLM provider는 ACP와 `agent:<model>` 또는 `metadata.agent=true`가
  들어간 HTTP 요청에서 AgentBridge의 내장 harness를 탑니다.
- native agent provider는 upstream session/runtime을 그대로 유지하고,
  내장 harness를 bypass합니다. 현재 이 범주에 들어가는 provider는
  `codex-app`입니다.

## 내장 provider

| 이름 | 종류 | 설명 |
| --- | --- | --- |
| `glm` | `glm` | GLM/Z.AI Coding Plan. 기본 provider, 기본 모델 `glm-5.1`. |
| `openai` | `openai-chat` | OpenAI Chat Completions와 호환 gateway. |
| `openai-responses` | `openai-responses` | OpenAI Responses API. |
| `anthropic` | `anthropic` | Anthropic Messages API. |
| `anthropic-vertex` | `anthropic` | Google OAuth token을 쓰는 Vertex AI Claude raw predict. |
| `google` | `google` | cachedContent prompt cache를 지원하는 Gemini native API. |
| `google-vertex`, `google-antigravity` | `google` | Google OAuth access token을 쓰는 Vertex AI Gemini. |
| `amazon-bedrock` | `bedrock-converse` | AWS SigV4 signing을 쓰는 Amazon Bedrock Converse. |
| `amazon-bedrock-mantle` | `anthropic` | Bearer auth를 쓰는 Bedrock Mantle Anthropic 호환 endpoint. |
| `claude-code` | `claude-code-cli` | Claude Code CLI adapter. |
| `codex-app` | `codex-app-server` | stdio JSON-RPC를 쓰는 native `codex app-server` transport. |
| `ollama` | `ollama` | Ollama native `/api/chat`. |
| `llamacpp` | `llama.cpp` | 로컬/원격 llama.cpp server. 명시 요청이 없으면 `model`을 보내지 않습니다. |
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
| `mistral` | `openai-chat` | Mistral OpenAI 호환 API. |
| `groq` | `openai-chat` | Groq OpenAI 호환 API. |
| `fireworks` | `openai-chat` | Fireworks AI OpenAI 호환 API. |
| `together` | `openai-chat` | Together AI OpenAI 호환 API, `reasoning.enabled` 매핑 포함. |
| `cerebras` | `openai-chat` | Cerebras OpenAI 호환 API. |
| `chutes` | `openai-chat` | Chutes OpenAI 호환 API. |
| `deepinfra` | `openai-chat` | DeepInfra OpenAI 호환 API. |
| `moonshot` | `openai-chat` | Moonshot/Kimi OpenAI 호환 API. |
| `minimax` | `openai-chat` | MiniMax OpenAI 호환 API. |
| `qwen` | `openai-chat` | Qwen/DashScope OpenAI 호환 API. |
| `qianfan` | `openai-chat` | Baidu Qianfan OpenAI 호환 API. |
| `venice` | `openai-chat` | Venice OpenAI 호환 API. |
| `vllm` | `openai-chat` | 로컬 vLLM OpenAI 호환 server. |
| `sglang` | `openai-chat` | 로컬 SGLang OpenAI 호환 server. |
| `cloudflare-ai-gateway` | `openai-chat` | Cloudflare AI Gateway template. |
| `microsoft-foundry` | `openai-chat` | Azure/Microsoft Foundry OpenAI 호환 inference endpoint. |
| `byteplus`, `byteplus-plan` | `openai-chat` | BytePlus Ark standard / Coding Plan endpoint. |
| `volcengine`, `volcengine-plan` | `openai-chat` | Volcano Engine Ark standard / Coding Plan endpoint. |
| `modelstudio`, `qwencloud` | `openai-chat` | Qwen/ModelStudio endpoint alias. |
| `github-copilot` | `openai-responses` | GitHub token 교환과 Copilot header를 쓰는 GitHub Copilot API. |
| `minimax-portal` | `openai-chat` | MiniMax Portal/OAuth-token endpoint template. |
| `ollama-cloud` | `openai-chat` | Ollama Cloud OpenAI 호환 API. |
| `lmstudio` | `openai-chat` | 로컬 LM Studio OpenAI 호환 server. |

## llama.cpp

`llama.cpp` provider는 하나의 llama.cpp server instance를 나타냅니다.
`base_url`에는 port가 포함된 전체 URL을 그대로 넣을 수 있고, AgentBridge는
기본적으로 model 이름을 요구하거나 upstream에 보내지 않습니다.

```yaml
providers:
  llama-office:
    kind: llama.cpp
    base_url: http://127.0.0.1:8888

  llama-lab:
    kind: llama.cpp
    base_url: http://127.0.0.1:8889
```

여러 instance는 provider 이름만 다르게 여러 개 등록하면 됩니다. 외부에 노출할
alias가 필요하면 model router에서 route를 잡습니다.

```yaml
providers:
  router:
    kind: router
    default_model: local-a
    extra:
      routes_file: ./router.yaml
```

```yaml
# router.yaml
aliases:
  local-a: local-a
  local-b: local-b
routes:
  - model: local-a
    provider: llama-office
  - model: local-b
    provider: llama-lab
```

실험용 intention probe는 llama.cpp `/v1/completions`의 `logprobs`를 사용합니다.
chat template이 answer 전에 reasoning/channel token을 내는 경우를 피하기
위해서입니다.

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

아래 OpenClaw/Hermes 항목은 추가 transport/auth 구현이 필요해서 아직 기본
template으로 활성화하지 않았습니다.

| Hermes provider | 남은 이유 |
| --- | --- |
| `nous` | Device-code OAuth와 scoped inference token minting 필요. |
| `qwen-oauth` | Qwen OAuth token refresh/store 통합 필요. |
| `google-gemini-cli` | 단순 HTTP base URL이 아니라 Cloud Code Assist OAuth transport. |
| `copilot-acp` | 외부 ACP process transport 필요. |
| `minimax-oauth` | 기존 `MINIMAX_OAUTH_TOKEN` 사용을 넘어선 browser OAuth setup flow 필요. |
| `fal`, `comfy`, `vydra` | Chat provider가 아니라 media-generation provider. |

## Provider API / Tool 매트릭스

이 표는 model provider API와 선택적 plugin tool을 분리해서 보여줍니다.
Plugin tool은 MCP `POST /mcp`, `/v1/mcp`로도 직접 노출할 수 있습니다.

| Provider / plugin | 인증 | 제공 API | AgentBridge tool |
| --- | --- | --- | --- |
| `glm` | `Z_AI_API_KEY` / `AGENTBRIDGE_API_KEY` | ACP chat, Chat Completions 호환 GLM route | 내장 file/shell/web tool, Z.AI MCP web tool |
| `zai` | `GLM_API_KEY`, `ZAI_API_KEY`, `Z_AI_API_KEY` | OpenAI Chat Completions | 내장 agent tool |
| `openai` | `OPENAI_API_KEY` | OpenAI Chat Completions | 내장 agent tool |
| `openai-responses` | `OPENAI_API_KEY` | OpenAI Responses | provider `extra` 설정 시 hosted `web_search` |
| `codex` | `~/.codex/auth.json`의 Codex OAuth | ChatGPT Codex Responses backend | Codex hosted `web_search`, prompt cache metadata |
| `codex-app` | 로컬 Codex CLI auth/session | native `codex app-server` transport | 로컬 session 재사용, provider-native upstream compaction |
| `google` | `GOOGLE_API_KEY` / `GEMINI_API_KEY` | Gemini native `streamGenerateContent` | 내장 agent tool, native cachedContent prompt cache |
| `google-vertex`, `google-antigravity` | `GOOGLE_OAUTH_ACCESS_TOKEN` 또는 인증된 `gcloud`, 그리고 `GOOGLE_CLOUD_PROJECT` | Vertex Gemini `streamGenerateContent` | 내장 agent tool |
| `amazon-bedrock` | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, 선택적 `AWS_SESSION_TOKEN` | Bedrock Converse | 내장 agent tool |
| `amazon-bedrock-mantle` | `BEDROCK_MANTLE_API_KEY` | Mantle Anthropic 호환 endpoint | 내장 agent tool |
| `github-copilot` | `COPILOT_API_TOKEN`, 또는 `COPILOT_GITHUB_TOKEN` / `GH_TOKEN` / `GITHUB_TOKEN` | Copilot Responses-compatible endpoint | 내장 agent tool |
| `xai` | `XAI_API_KEY` | xAI Responses 호환 Grok | plugin 사용 시 xAI hosted `x_search` |
| `xai-oauth` | `~/.grok/auth.json`, fallback `~/.hermes/auth.json` | xAI Responses 호환 Grok | 같은 OAuth token을 `xai` plugin이 재사용 가능 |
| `anthropic` | `ANTHROPIC_API_KEY` | Anthropic Messages | 내장 agent tool |
| `anthropic-vertex` | `GOOGLE_OAUTH_ACCESS_TOKEN` 또는 인증된 `gcloud`, 그리고 `ANTHROPIC_VERTEX_PROJECT_ID` / `GOOGLE_CLOUD_PROJECT` | Vertex Anthropic `streamRawPredict` | 내장 agent tool |
| `claude-code` | Claude Code CLI auth | Claude CLI one-shot adapter | Claude CLI tool policy passthrough |
| `ollama` | 선택적 `OLLAMA_API_KEY` | Ollama native `/api/chat` | 내장 agent tool |
| `openrouter` | `OPENROUTER_API_KEY` | OpenAI Chat Completions gateway | 내장 agent tool |
| `litellm` | `LITELLM_API_KEY` | OpenAI Chat Completions gateway | `/embeddings` 테스트는 `openai_embed` plugin 사용 |
| `kimi-coding`, `kimi-coding-cn` | `KIMI_API_KEY`, `KIMI_CODING_API_KEY`, `KIMI_CN_API_KEY` | OpenAI Chat Completions | 내장 agent tool |
| `deepseek` | `DEEPSEEK_API_KEY` | OpenAI Chat Completions | 내장 agent tool |
| `stepfun` | `STEPFUN_API_KEY` | OpenAI Chat Completions | 내장 agent tool |
| `alibaba`, `alibaba-coding-plan` | `DASHSCOPE_API_KEY`, `ALIBABA_CODING_PLAN_API_KEY` | OpenAI Chat Completions | 내장 agent tool |
| `nvidia` | `NVIDIA_API_KEY` | OpenAI Chat Completions | 내장 agent tool |
| `ai-gateway`, `opencode-zen`, `opencode-go`, `kilocode` | gateway별 API key | OpenAI Chat Completions gateway | 내장 agent tool |
| `huggingface`, `novita`, `arcee`, `gmi`, `xiaomi`, `tencent-tokenhub`, `mistral`, `groq`, `fireworks`, `together`, `cerebras`, `chutes`, `deepinfra`, `moonshot`, `minimax`, `minimax-portal`, `qwen`, `qwencloud`, `modelstudio`, `qianfan`, `venice`, `byteplus`, `byteplus-plan`, `volcengine`, `volcengine-plan`, `cloudflare-ai-gateway`, `microsoft-foundry`, `ollama-cloud`, `lmstudio`, `vllm`, `sglang` | provider별 API key 또는 local key | OpenAI Chat Completions 호환 | 내장 agent tool |
| `plugin:jina` | 선택적 `JINA_API_KEY` | Jina Reader, Search, Embeddings, Rerank | `jina_reader`, `jina_search`, `jina_embed`, `jina_rerank`; HTTP `/v1/embeddings`, `/v1/rerank` |
| `plugin:ollama_search` | `OLLAMA_API_KEY` | Ollama Cloud web search/fetch | `ollama_search`, `ollama_fetch` |
| `plugin:xai` | xAI OAuth 또는 `XAI_API_KEY` | xAI Responses `x_search`, Images generations/edits | `xai_x_search`, `xai_image_generate`, `xai_image_edit` |
| `plugin:openai_embed` | `AGENTBRIDGE_EMBEDDINGS_API_KEY` 또는 mapping key env | OpenAI 호환 `/embeddings` gateway | `embed`; HTTP `/v1/embeddings`; router `extra.embeddings` alias |
| `plugin:sqlite` | local filesystem | SQLite catalog/query | `sqlite_list`, `sqlite_load`, `sqlite_unload`, `sqlite_tables`, `sqlite_schema`, `sqlite_query`, `sqlite_exec` |
| `plugin:duckdb` | local process | Reserved placeholder | `duckdb_status` |

## Model Router

`router`는 LiteLLM 스타일 provider frontend입니다. AgentBridge는 router
model mapping을 하드코딩하지 않습니다. `$XDG_CONFIG_HOME/agentbridge/config.yaml`,
`router.yaml`, 또는 `AGENTBRIDGE_ROUTER_FILE`로 지정한 파일에 mapping을 두세요.
한 번만 선택해두고 요청의 `model` 값으로 실제 backend를 고릅니다.

```bash
AGENTBRIDGE_PROVIDER=router agentbridge --http-listen 127.0.0.1:8766
```

Route는 `$XDG_CONFIG_HOME/agentbridge/config.yaml`에 둘 수 있습니다.

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

위 `ollama/*` route는 `model=ollama/gpt-oss:120b` 요청을
`target_model=gpt-oss:120b`로 바꾸고, API key를
`OLLAMA_API_KEY_A`, `OLLAMA_API_KEY_B`, `OLLAMA_API_KEY_A` 순서로
round-robin 합니다. 비밀값이 저장소에 들어가지 않도록 `api_keys`보다는
`api_key_envs`를 권장합니다.

Route는 `$XDG_CONFIG_HOME/agentbridge/router.yaml`, `router.json`, 또는
`AGENTBRIDGE_ROUTER_FILE`로 지정한 외부 JSON/YAML 파일로도 분리할 수
있습니다.

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

`api_key_envs`와 `api_keys`는 YAML/JSON list뿐 아니라 구분 문자열도
허용합니다.

```yaml
api_key_envs: OLLAMA_API_KEY_A, OLLAMA_API_KEY_B
```

`retry_keys: true`를 켜면 router는 streamed output이 아직 나오기 전의
429/quota/weekly-limit/5h limit 계열 오류를 감지하고, 해당 key를 현재
프로세스에서 limited로 표시한 뒤 다음 key로 재시도합니다. Reset 시간 표현은
provider마다 달라서 현재는 best-effort 감지입니다.

전체 route schema와 우선순위는 [설정](configuration.md#router-route-schema)을
참고하세요.

## 예시

OpenAI:

```bash
AGENTBRIDGE_PROVIDER=openai \
AGENTBRIDGE_API_KEY=example-api-key \
AGENTBRIDGE_MODEL=gpt-4.1-mini \
agentbridge
```

OpenAI Responses native conversation compaction은 Codex의 remote compaction
동작과 맞춰 기본적으로 `/v1/responses/compact`를 사용합니다. 다음처럼
끄거나 경로를 바꿀 수 있습니다.

```bash
OPENAI_COMPACTION=disabled AGENTBRIDGE_PROVIDER=openai-responses agentbridge
OPENAI_COMPACT_PATH=/v1/responses/compact AGENTBRIDGE_PROVIDER=openai-responses agentbridge
```

Anthropic native adapter는 기본적으로 prompt caching을 켭니다. AgentBridge는
Hermes의 `system_and_3` 전략과 같이 system prompt와 마지막 non-system
message 3개에 `cache_control`을 표시합니다. 끄려면
`ANTHROPIC_PROMPT_CACHE=off`, 1시간 TTL을 쓰려면
`ANTHROPIC_PROMPT_CACHE_TTL=1h`를 설정하세요.

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

Codex app-server native runtime:

```bash
AGENTBRIDGE_PROVIDER=codex-app \
CODEX_COMMAND=codex \
CODEX_MODEL=gpt-5 \
CODEX_APPROVAL_POLICY=never \
agentbridge
```

`codex-app`은 OpenAI 스타일 `prompt_cache_*` hint를 upstream으로 보내지
않습니다. 대신 AgentBridge 쪽에서 `session_id`나 `prompt_cache_key`가
안정적으로 유지되면 같은 로컬 Codex thread를 재사용합니다. 그래서
`/v1/chat/completions` 클라이언트 뒤에 로컬 native runtime을 두는 용도로
적합합니다.

ACP session에서는 `codex-app`이 `provider_native` 단일 mode를 광고합니다.
이는 agentic loop, compaction trigger, tool execution lifecycle을 provider
자체가 소유한다는 의미입니다.

Codex OAuth:

```bash
AGENTBRIDGE_PROVIDER=codex agentbridge
```

Codex provider는 `~/.codex/auth.json` 또는 `AGENTBRIDGE_CODEX_*` token
override를 사용합니다.

Codex native conversation compaction은 기본적으로 `/responses/compact`
endpoint를 사용합니다. 다음처럼 끄거나 경로를 바꿀 수 있습니다.

```bash
CODEX_COMPACTION=disabled AGENTBRIDGE_PROVIDER=codex agentbridge
CODEX_COMPACT_PATH=/responses/compact AGENTBRIDGE_PROVIDER=codex agentbridge
```

Hermes/OpenAI Codex source는 native remote compaction을 OpenAI와 Azure
Responses provider에서만 지원하는 것으로 판정합니다. xAI/Grok도 Hermes에서
Responses 형태의 transport를 쓰지만 remote-compaction capable로 표시되지는
않으므로, AgentBridge에서도 xAI는 generic summary fallback을 유지합니다.

Codex prompt caching은 기본적으로 현재 session을 따릅니다.
`CODEX_PROMPT_CACHE_KEY`의 기본값은 `{session_id}`이며 `{model}`,
`{provider}`도 함께 보간할 수 있습니다. Reasoning은 기본적으로
`CODEX_REASONING_EFFORT=medium`, `CODEX_REASONING_SUMMARY=auto`를 사용하고,
multi-turn Codex session에서 provider-private reasoning state를 재사용할 수
있도록 encrypted reasoning content를 요청합니다.

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

xAI Responses 요청도 기본적으로 session-scoped `prompt_cache_key`를 사용합니다.
`XAI_REASONING_EFFORT`는 `reasoning.effort`를 받는 것으로 확인된 Grok model에만
전송합니다. 나머지 Grok model에는 이 field를 생략해서 xAI HTTP 400을 피하고,
model의 native reasoning은 그대로 사용합니다.

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
