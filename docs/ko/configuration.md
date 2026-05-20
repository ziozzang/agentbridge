# 설정

AgentBridge는 주로 환경 변수와 선택적 provider YAML 파일로 설정합니다.

## 우선순위

1. provider별 변수. 예: `AGENTBRIDGE_OPENAI_API_KEY`
2. 전역 변수. 예: `AGENTBRIDGE_API_KEY`
3. 사용자 provider YAML:
   `$XDG_CONFIG_HOME/agentbridge/providers.yaml`
4. 기존 사용자 provider YAML:
   `$XDG_CONFIG_HOME/acp-harness/providers.yaml`
5. 내장 template: `internal/config/providers.yaml`

기존 `ACP_HARNESS_*` 변수는 alias로 계속 지원됩니다.

## 전역 변수

| 변수 | 용도 |
| --- | --- |
| `AGENTBRIDGE_PROVIDER` | 활성 provider. 기본값: `glm`. |
| `AGENTBRIDGE_MODEL` | 기본 모델 override. |
| `AGENTBRIDGE_API_KEY` | 활성 provider API key. |
| `AGENTBRIDGE_BASE_URL` | base URL override. |
| `AGENTBRIDGE_PROVIDERS_FILE` | provider YAML 파일 경로. |
| `AGENTBRIDGE_PLUGINS` | plugin 목록. 예: `sqlite,duckdb`. |

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

## Legacy alias

다음 기존 변수는 계속 지원됩니다.

- `ACP_HARNESS_PROVIDER`, `ACP_HARNESS_API_KEY`, `ACP_HARNESS_MODEL`
- `ACP_HARNESS_<NAME>_API_KEY`
- `ACP_GLM_MODEL`, `ACP_GLM_BASE_URL`, `ACP_GLM_THINKING`,
  `ACP_GLM_MAX_TOKENS`, `ACP_GLM_SESSION_DIR`
- GLM/Z.AI용 `Z_AI_API_KEY`
