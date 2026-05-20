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

## 전역 변수

| 변수 | 용도 |
| --- | --- |
| `AGENTBRIDGE_PROVIDER` | 활성 provider. 기본값: `glm`. |
| `AGENTBRIDGE_MODEL` | 기본 모델 override. |
| `AGENTBRIDGE_API_KEY` | 활성 provider API key. |
| `AGENTBRIDGE_BASE_URL` | base URL override. |
| `AGENTBRIDGE_CONFIG_FILE` | 전체 config YAML 경로. |
| `AGENTBRIDGE_PROVIDERS_FILE` | provider YAML 파일 경로. |
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
providers:
  router:
    kind: router
    default_model: ollama/gpt-oss:120b
    extra:
      routes_file: ${XDG_CONFIG_HOME}/agentbridge/router.yaml
```

## 외부 MCP Servers

외부 MCP server는 ACP session과 HTTP MCP compatibility endpoint 양쪽에서 쓰도록
전역 등록할 수 있습니다. `AGENTBRIDGE_MCP_FILE`을 지정하거나
`$XDG_CONFIG_HOME/agentbridge` 아래 `mcp.yaml` / `mcp.json`을 둡니다.

```yaml
mcp_servers:
  - name: search
    type: http
    url: http://127.0.0.1:8090/mcp
    headers:
      Authorization: Bearer ${MCP_TOKEN}
    enabled: true
```

`mcpServers`도 허용하며, list와 이름-keyed object 형식을 모두 받을 수
있습니다. 끄려면 `disabled: true`, `enabled: false`, 또는
`AGENTBRIDGE_DISABLED_MCPS=search`를 사용합니다. HTTP MCP tool은
`mcp__<server>__<tool>` 이름으로 노출됩니다.

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
