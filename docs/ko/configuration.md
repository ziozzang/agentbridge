# 설정

AgentBridge는 환경 변수와 `$XDG_CONFIG_HOME/agentbridge` 아래의 선택적
YAML/JSON 파일로 설정합니다.

## 우선순위

1. provider별 변수. 예: `AGENTBRIDGE_OPENAI_API_KEY`
2. 전역 변수. 예: `AGENTBRIDGE_API_KEY`
3. 명시적 config 파일: `AGENTBRIDGE_CONFIG_FILE`
4. 명시적 provider YAML: `AGENTBRIDGE_PROVIDERS_FILE`
5. 사용자 config YAML:
   `$XDG_CONFIG_HOME/agentbridge/config.yaml`
6. 사용자 provider YAML:
   `$XDG_CONFIG_HOME/agentbridge/providers.yaml`
7. 기존 사용자 config/provider YAML:
   `$XDG_CONFIG_HOME/acp-harness/config.yaml`,
   `$XDG_CONFIG_HOME/acp-harness/providers.yaml`
8. 내장 template: `internal/config/providers.yaml`

기존 `ACP_HARNESS_*` 변수는 alias로 계속 지원됩니다.

활성 provider는 `config.yaml` 안에서도 `provider`, `active_provider`,
`default_provider` 중 하나로 지정할 수 있습니다. 환경 변수 설정이 있으면
파일 설정보다 우선합니다.

```yaml
provider: router
```

## 전역 변수

| 변수 | 용도 |
| --- | --- |
| `AGENTBRIDGE_PROVIDER` | 활성 provider. 기본값: `glm`. |
| `AGENTBRIDGE_MODEL` | 기본 모델 override. |
| `AGENTBRIDGE_API_KEY` | 활성 provider API key. |
| `AGENTBRIDGE_BASE_URL` | base URL override. |
| `AGENTBRIDGE_CONFIG_FILE` | 전체 config YAML 경로. |
| `AGENTBRIDGE_PROVIDERS_FILE` | provider YAML 파일 경로. |
| `AGENTBRIDGE_AGENTS_FILE` | Agent profile YAML/JSON 파일. |
| `AGENTBRIDGE_PLUGINS` | plugin 목록. 예: `sqlite,duckdb`. |
| `AGENTBRIDGE_DISABLED_PLUGINS` | 활성화 목록에 있어도 끌 plugin 이름 목록. |
| `AGENTBRIDGE_MCP_FILE` | 외부 MCP server JSON/YAML 설정 파일. |
| `AGENTBRIDGE_DISABLED_MCPS` | 설정에 있어도 끌 MCP server 이름 목록. |
| `AGENTBRIDGE_ROUTER_FILE` | Router route JSON/YAML 파일. |

## Provider별 변수

provider 이름을 대문자로 쓰고, 하이픈은 밑줄로 바꿉니다.

| 변수 | 용도 |
| --- | --- |
| `AGENTBRIDGE_<NAME>_API_KEY` | provider별 API key. |
| `AGENTBRIDGE_<NAME>_BASE_URL` | provider별 base URL. |
| `AGENTBRIDGE_<NAME>_MODEL` | provider별 기본 모델. |

예:

```bash
AGENTBRIDGE_PROVIDER=openai
AGENTBRIDGE_OPENAI_API_KEY=example-api-key
AGENTBRIDGE_OPENAI_MODEL=gpt-4.1-mini
```

## Logging

| 변수 | 용도 |
| --- | --- |
| `AGENTBRIDGE_LOG_LEVEL` | `trace`, `debug`, `info`, `warn`, `error`, `off`. |
| `AGENTBRIDGE_LOG_FILE` | log file 경로. |
| `AGENTBRIDGE_LOG_BOTH` | `1`이면 stderr와 파일에 동시에 기록. |
| `AGENTBRIDGE_LOG_MAX_BYTES` | rotation 기준 크기. |
| `AGENTBRIDGE_LOG_MAX_FILES` | 보관할 rotation 파일 수. |

기존 `ACP_HARNESS_LOG_*`, `ACP_GLM_DEBUG`도 동작합니다.

## Session 저장

| 변수 | 용도 |
| --- | --- |
| `AGENTBRIDGE_SESSION_DIR` | session 저장 디렉터리. |
| `XDG_STATE_HOME` | 기본 session 디렉터리의 base 경로. |

기본값은 `$XDG_STATE_HOME/agentbridge/sessions` 또는
`~/.local/state/agentbridge/sessions`입니다.

Session JSON에는 runtime checkpoint, active skill reference, `cacheEpoch`도
저장됩니다. 파일 형식은 내부 구현이며 public API로 간주하지 않습니다.

## Skills

Runtime skill은 `/skill` command가 읽는 markdown 파일입니다.

- Project-local: `<cwd>/.agentbridge/skills/*.md`
- User config: `$XDG_CONFIG_HOME/agentbridge/skills/*.md` 또는
  `~/.config/agentbridge/skills/*.md`

같은 이름이면 project-local skill이 user-config skill보다 우선합니다.

## Project Context

AgentBridge는 session cwd에서 repository context 파일 하나를 자동으로 읽어
system prompt에 주입합니다. 우선순위는 아래와 같습니다.

1. `SOUL.md`
2. `AGENTS.md`
3. `CLAUDE.md`

내용은 `MaxProjectContextChars`로 제한됩니다. `acp-agent /status`는 현재 session
cwd에서 어떤 파일이 사용될지 보여줍니다.

## File Attachments

`acp-agent /attach PATH [...]`는 로컬 문서를 추출해서 다음 prompt에 첨부하도록
queue에 넣습니다. Prompt 전송이 성공하면 queue를 비우고, 실패하면 복원합니다.
추출된 내용은 `filecontext.MaxExtractedChars`로 제한됩니다.

지원하는 추출:

- Markdown, plain text, JSON, YAML, CSV, source file 등 UTF-8 text-like file.
- PDF는 `pdftotext`가 있으면 사용하고, 간단한 PDF에는 printable-string fallback을
  사용합니다.

`/files`로 queue를 확인하고, `/clear-files`로 비우며, `/structure`로
session/context/attachment 구조를 확인할 수 있습니다.

## Config YAML

`config.yaml`은 `providers.yaml`과 같은 `providers:` schema를 사용합니다.
Router route처럼 AgentBridge 전역에 가까운 설정은 `config.yaml`에 두는 것을
권장합니다.

기본 위치:

- `$XDG_CONFIG_HOME/agentbridge/config.yaml`
- `~/.config/agentbridge/config.yaml`

명시적 override:

```bash
AGENTBRIDGE_CONFIG_FILE=/path/to/config.yaml agentbridge
```

예시:

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

CLI flag는 여전히 `server:` 값보다 우선합니다.

Compaction은 ACP와 HTTP/A2A agent loop에 모두 적용됩니다. AgentBridge는
provider-native compaction을 먼저 시도하고, structured summary fallback,
마지막으로 `prune_fallback`이 켜진 경우에만 pruning을 사용합니다. 퍼센트 값은
`0.90` 같은 fraction 또는 `90` 같은 percentage로 쓸 수 있습니다. 환경 변수
override도 지원합니다.

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

HTTP client는 같은 메커니즘을 `POST /v1/responses/compact`로 직접 호출할 수
있습니다. 요청은 `input`, `messages`, `previous_response_id` 중 하나와
선택적으로 `strategy`(`auto`, `native`, `summary`, `prune`, `none`),
`target_tokens`를 받습니다. 응답은 교체용 message list와 `strategy`,
`compacted`, token estimate를 돌려줍니다.

## Safety / Request Mutation

`pii`, `sanitize`, `cache`, `inject`는 특정 provider가 아니라 모든 protocol에
동일하게 적용되어야 하므로 top-level 설정으로 둡니다. 의도, 현재 구현 상태,
세부 설정, rollout 순서는 [Safety Pipeline](safety.md)을 보세요.

## Provider Cache / Reasoning 옵션

Hermes에서 가져온 provider별 knob는 provider `extra` 또는 내장 template의 환경
변수로 설정할 수 있습니다.

| 변수 | Provider | 용도 |
| --- | --- | --- |
| `ANTHROPIC_PROMPT_CACHE` | `anthropic` | 기본값 `on`; Anthropic `cache_control` breakpoint를 주입합니다. |
| `ANTHROPIC_PROMPT_CACHE_TTL` | `anthropic` | 기본값 `5m`; `1h`로 설정하면 더 긴 ephemeral cache TTL을 씁니다. |
| `CODEX_PROMPT_CACHE_KEY` | `codex` | 기본값 `{session_id}`; `{model}`, `{provider}` template도 지원합니다. |
| `CODEX_REASONING_EFFORT` | `codex` | 기본값 `medium`. |
| `CODEX_REASONING_SUMMARY` | `codex` | 기본값 `auto`. |
| `CODEX_BINARY_PATH` | `codex-app` | Codex CLI/binary의 절대경로. `CODEX_COMMAND`보다 우선합니다. |
| `CODEX_COMMAND` | `codex-app` | `codex app-server`에 사용할 로컬 Codex CLI binary. 기본값 `codex`. |
| `CODEX_APPROVAL_POLICY` | `codex-app` | 기본값 `never`; native app-server thread에 적용됩니다. |
| `CODEX_SANDBOX` | `codex-app` | `workspace-write`, `danger-full-access` 같은 native Codex sandbox mode. |
| `CODEX_REASONING_EFFORT` | `codex-app` | native app-server turn reasoning effort. |
| `COPILOT_API_TOKEN` | `github-copilot` | 이미 교환된 Copilot API token을 사용합니다. |
| `COPILOT_GITHUB_TOKEN`, `GH_TOKEN`, `GITHUB_TOKEN` | `github-copilot` | 짧은 수명의 Copilot API token으로 교환하고 캐시합니다. |
| `XAI_PROMPT_CACHE_KEY` | `xai`, `xai-oauth` | 기본값 `{session_id}`. |
| `XAI_REASONING_EFFORT` | `xai`, `xai-oauth` | `reasoning.effort`를 받는 Grok model에만 전송합니다. |
| `KIMI_REASONING_EFFORT`, `KIMI_CN_REASONING_EFFORT` | `kimi-coding`, `kimi-coding-cn` | Chat Completions top-level `reasoning_effort`; 기본값 `medium`. |
| `DEEPSEEK_REASONING_EFFORT` | `deepseek` | DeepSeek thinking-capable model에만 전송하며 `xhigh`는 `max`로 변환합니다. |
| `TOGETHER_REASONING_EFFORT` | `together` | Together의 `reasoning.enabled` payload로 매핑합니다. |
| `TOKENHUB_REASONING_EFFORT`, `LM_REASONING_EFFORT` | `tencent-tokenhub`, `lmstudio` | Chat Completions top-level `reasoning_effort`. |
| `GOOGLE_CACHE_RETENTION` | `google` | Gemini 2.5/3에서 기본값 `short`; Google native `cachedContents`를 생성/갱신합니다. |
| `GOOGLE_OAUTH_ACCESS_TOKEN`, `GOOGLE_VERTEX_ACCESS_TOKEN`, `GOOGLE_CLOUD_PROJECT`, `GOOGLE_CLOUD_LOCATION` | `google-vertex`, `google-antigravity`, `anthropic-vertex` | Vertex auth/project/region. token env가 없으면 `gcloud auth application-default print-access-token`을 시도합니다. |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `AWS_REGION` | `amazon-bedrock` | Bedrock Converse SigV4 credential과 region. |
| `OPENAI_RESPONSES_SERVER_COMPACTION` | `openai-responses` | OpenAI Responses `context_management` compaction hint를 켭니다. |
| `OPENAI_RESPONSES_COMPACT_THRESHOLD` | `openai-responses` | 선택적 `context_management.compact_threshold`; 기본값은 context의 70%. |
| `OPENAI_PROMPT_CACHE_KEY` | `openai-responses` | 기본값 `{session_id}`; `{model}`, `{provider}` template도 지원합니다. |
| `OPENAI_PROMPT_CACHE_RETENTION` | `openai-responses` | 선택적 upstream `prompt_cache_retention`. |

OpenAI-chat provider도 upstream이 지원하는 경우 Anthropic-style
`cache_control` breakpoint를 사용할 수 있습니다. AgentBridge는
OpenRouter/Nous의 Claude route와 Alibaba/OpenCode/Nous의 Qwen route에서 이를
자동으로 켭니다. 사용자 정의 OpenAI 호환 provider에서는
`extra.prompt_cache: on`을 설정하세요. upstream이 1시간 TTL을 지원하면
`extra.prompt_cache_ttl: 1h`도 사용할 수 있습니다.
OpenRouter response cache는 provider `extra`의 `response_cache`,
`response_cache_ttl`, `response_cache_clear`로 조정하며, 각각
`X-OpenRouter-Cache*` request header로 매핑됩니다.

HTTP `/v1/chat/completions`, `/v1/responses`, Anthropic 호환 `/v1/messages`,
A2A 요청에서는 `metadata.prompt_cache_key`, `metadata.prompt_cache_retention`,
`metadata.cache_retention`, `metadata.service_tier`, `metadata.reasoning_effort`,
session id(`metadata.session_id`, `sessionId`, `thread_id`)를 넘겨 request별
라우팅에 사용할 수 있습니다. `/v1/responses`의 top-level `prompt_cache_key`와
`prompt_cache_retention`도 provider request로 전달됩니다.

## Experimental Intention Probe

AgentBridge에는 opt-in 실험용 classifier endpoint인
`POST /experimental/intention`이 있습니다. 아래 중 하나로 켭니다.

```bash
AGENTBRIDGE_EXPERIMENTAL_INTENTION_PROBE=1
# 또는
AGENTBRIDGE_EXPERIMENTS=intention_probe
```

이 endpoint는 label이 붙은 선택지 중 하나를 고르게 하고, 첫 token의
`top_logprobs`로 confidence를 계산합니다. 현재는 upstream이 Chat Completions
logprobs를 실제로 반환하는 `openai-chat` provider shape에서만 동작하고,
`router`는 해당 provider로 forwarding합니다. 일반적인 답변 confidence 지표는
아닙니다.

```bash
curl -s http://127.0.0.1:8766/experimental/intention \
  -H 'content-type: application/json' \
  -d '{"model":"openai/gpt-4.1","prompt":"Which city is the capital of South Korea?","choices":["Seoul","Busan"]}'
```

Ollama Cloud처럼 logprobs parameter는 받아도 응답에 logprobs를 내려주지 않는
provider는 unsupported upstream error를 반환합니다.

## Embedding Model Mapping

`openai_embed`는 여러 사용자-facing embedding alias를 서로 다른 OpenAI 호환
upstream으로 라우팅할 수 있습니다. 이제 model router 설정과 같은
`config.yaml`에 두는 것을 권장합니다.

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

map key는 `POST /v1/embeddings`에서 받는 public model ID이며
`GET /v1/models`에도 그대로 표시됩니다. `model`은 upstream model ID입니다.
`provider` 또는 `owned_by`는 OpenAI 호환 `owned_by` 필드로 내려갑니다.

Chat provider의 `models:` entry에는 `GET /v1/models`로 내려갈 metadata도
넣을 수 있습니다. 지원 필드는 `provider`, `api`, `base_url`, `input`,
`reasoning`, `context_window`, `context_tokens`, `max_tokens`, `status`,
`aliases`, `tags`, `compat`, `cost`입니다. 이렇게 하면 `grok` 같은 generic
pseudo-model 대신 실제 `grok-4`가 `owned_by: xai`로 노출됩니다.

## Agent Runtime Controls

HTTP agent-loop의 destructive tool 정책은 runtime config에서 제어합니다.

```yaml
agent:
  yolo_mode: false
```

`yolo_mode`가 `true`이면 write/command tool은 `bypass_permissions` mode로
실행됩니다. 명시적으로 `false`이면 non-interactive HTTP agent loop는
write/execute permission request를 기본 reject하고, `read_file`,
`list_files` 같은 read-only tool은 계속 실행합니다. `yolo_mode`를 생략하면
기존 호환성을 위해 permission bypass 기본값을 유지합니다.

고급 override:

```yaml
agent:
  permission_mode: accept_edits   # default, accept_edits, bypass_permissions, read_only
  permission_decision: reject     # allow, reject, cancel
```

Request metadata에서도 단일 HTTP agent-loop turn에 대해 `permission_mode` /
`permission_decision`을 지정할 수 있습니다. Request-level override는 trusted
client control로 보고, 배포 정책은 runtime config에 두는 것을 권장합니다.

## Agent Profiles

Agent profile은 virtual model입니다. ACP에서 profile 이름을 선택하면 지정한
upstream model을 사용하고, 추가 system prompt를 주입하며, 필요한 경우 tool
목록을 제한합니다. OpenAI 호환 `GET /v1/models` 목록에도 함께 표시됩니다.
`AGENTBRIDGE_AGENTS_FILE`을 지정하거나 `$XDG_CONFIG_HOME/agentbridge` 아래
`agents.yaml` / `agents.json`을 둡니다.

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

`system_prompt`는 inline으로 사용할 수 있고, `prompt_file`과 함께 사용할 수도
있습니다. 상대 `prompt_file` 경로는 profile 파일 위치를 기준으로 해석됩니다.

## 외부 MCP Servers

외부 MCP server는 ACP session과 HTTP MCP compatibility endpoint 양쪽에서 쓰도록
전역 등록할 수 있습니다. `AGENTBRIDGE_MCP_FILE`을 지정하거나
`$XDG_CONFIG_HOME/agentbridge` 아래 `mcp.yaml` / `mcp.json`을 둡니다.

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

CLI / stdio 예시:

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

`mcpServers`도 허용하며, list와 이름-keyed object 형식을 모두 받을 수
있습니다. 끄려면 `disabled: true`, `enabled: false`, 또는
`AGENTBRIDGE_DISABLED_MCPS=search`를 사용합니다. HTTP MCP tool은
`mcp__<server>__<tool>` 이름으로 노출됩니다.
노출할 upstream command를 제한하려면 `allow_tools` / `deny_tools`를 사용합니다.
두 필드는 list 또는 쉼표/개행 구분 문자열을 받으며 wildcard를 지원합니다.
Upstream이 `tools/list`에서 광고하지 않는 tool definition을 강제로 넣으려면
`inject_tools`를 사용합니다. Injected tool은 `mcp__<server>__<name>`으로 노출되고
upstream의 `source_name`을 호출합니다.
`GET /v1/tool-catalog`는 builtin, plugin, MCP tool을 한 번에 보여줍니다.
`GET /v1/mcp/catalog`는 설정된 MCP server 상세만 볼 때 사용합니다. Catalog
응답의 secret header는 redaction됩니다.

## Provider YAML

```yaml
providers:
  myprov:
    kind: openai-chat
    base_url: https://example.com/v1
    api_key: ${MYPROV_API_KEY}
    default_model: my-model
```

실행:

```bash
AGENTBRIDGE_PROVIDER=myprov agentbridge
```

## Router Route Schema

`router` provider는 요청의 model 이름을 기준으로 다른 provider에 위임합니다.
AgentBridge는 model route를 하드코딩하지 않습니다.
`providers.router.extra.routes`, `providers.router.extra.routes_file`, 또는
`AGENTBRIDGE_ROUTER_FILE`에 route를 두세요.

자동 탐색 route 파일:

- `$XDG_CONFIG_HOME/agentbridge/router.yaml`
- `$XDG_CONFIG_HOME/agentbridge/router.json`

Route 파일 형식:

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

Route field:

| Field | 용도 |
| --- | --- |
| `match` | 요청 model pattern. `*` wildcard 지원. |
| `model` | `match` alias. 짧은 JSON에 유용. |
| `models` | 같은 provider로 보낼 model pattern 목록. `models: "*"`는 provider-wide catch-all route. |
| `provider` | 위임할 provider 이름. |
| `target_model` | upstream model. `$model`은 원 요청 유지, `$1`은 wildcard capture. |
| `aliases` | 이 route로 보낼 추가 요청 model 이름. |
| `fallbacks` | primary route가 streaming 전에 실패했을 때 순서대로 시도할 대체 route 목록. |
| `request_defaults` | 지원 adapter가 upstream request body에 주입하는 추가 기본 필드. 현재 OpenAI Chat Completions 지원. |
| `api_key_envs` | 하나 이상의 key 환경 변수 이름. list 또는 구분 문자열 허용. |
| `api_keys` | literal key. list 또는 구분 문자열 허용. 가능하면 `api_key_envs` 권장. |
| `retry_keys` | true면 pre-stream 429/quota/weekly/5h limit 오류 후 다음 key 재시도. |
| `default` | pattern이 없을 때 fallback route. |
| `max_tokens` | route별 max token override. |
| `context_window` | route별 context window override. |

`api_key_envs`, `api_keys`는 모두 아래 형식을 허용합니다.

```yaml
api_key_envs: [OLLAMA_API_KEY_A, OLLAMA_API_KEY_B]
api_key_envs: OLLAMA_API_KEY_A, OLLAMA_API_KEY_B
api_key_envs: |
  OLLAMA_API_KEY_A
  OLLAMA_API_KEY_B
```

Limit 감지는 best-effort입니다. Router는 streamed output이 아직 나오기 전의
HTTP 429와 `rate limit`, `quota`, `weekly limit`, `5h` 같은 문구를
감지합니다. 감지된 key는 현재 프로세스에서 limited로 표시하고 이후
round-robin에서 건너뜁니다. Reset 시간 parsing은 아직 provider마다 안정적이지
않습니다.

`max_concurrent_per_key`는 `api_key_envs` 또는 `api_keys`로 선택된 각 key별
동시 session 수를 제한합니다. 예를 들어 Ollama Cloud key가 두 개이고
`max_concurrent_per_key: 3`이면 route 전체로는 최대 6개 session을 실행하되
각 key에는 3개까지만 붙습니다. 이는 quota 감지와 별개입니다. 동시성 cap은
로컬에서 과사용을 막고, quota 감지는 upstream 오류와 quota header/message에
반응합니다.

Fallback은 대체 upstream model/provider를 위한 기능입니다. 어떤 route가 이미
출력을 시작한 뒤 실패하면 AgentBridge는 다른 모델로 재시도하지 않고 해당
실패를 반환합니다. 중복 side effect와 mixed-model 응답을 피하기 위해서입니다.

`request_defaults`는 provider별 기능입니다. `openai-chat`에서는 AgentBridge가
일반 request를 만든 뒤 JSON body에 merge하므로 vendor별 필드를 강제로 넣을 수
있습니다.

```yaml
request_defaults:
  reasoning: off
  reasoning_cost: 1234
```

## Legacy alias

다음 기존 변수는 계속 지원됩니다.

- `ACP_HARNESS_PROVIDER`, `ACP_HARNESS_API_KEY`, `ACP_HARNESS_MODEL`
- `ACP_HARNESS_<NAME>_API_KEY`
- `ACP_GLM_MODEL`, `ACP_GLM_BASE_URL`, `ACP_GLM_THINKING`,
  `ACP_GLM_MAX_TOKENS`, `ACP_GLM_SESSION_DIR`
- GLM/Z.AI용 `Z_AI_API_KEY`
