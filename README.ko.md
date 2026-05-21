# AgentBridge

AgentBridge는 여러 LLM provider와 여러 에이전트 프로토콜을 하나의
백엔드로 연결하는 프로토콜 브리지이자 호환 게이트웨이입니다. ACP,
A2A, MCP Streamable HTTP, OpenAI 호환 HTTP API, Anthropic Messages 호환
API, AG-UI, gRPC를 공통 provider 백엔드 위에서 제공합니다.

이 프로젝트는 `glm-acp`로 시작했지만, 이제 GLM 전용도 아니고 ACP 전용도
아니므로 `agentbridge`로 이름을 바꿉니다. 기존 `ACP_HARNESS_*`,
`ACP_GLM_*`, `Z_AI_API_KEY`, 예전 credential/session 경로는 가능한 범위에서
하위 호환으로 유지합니다.

English documentation: [README.md](README.md)

## 주요 기능

- **여러 프로토콜, 하나의 백엔드**: ACP stdio/TCP, A2A JSON-RPC, MCP
  Streamable HTTP, OpenAI Chat Completions, OpenAI Responses, Anthropic
  Messages, AG-UI SSE, gRPC.
- **여러 provider**: GLM/Z.AI, OpenAI, OpenAI Responses, Anthropic, Ollama,
  OpenRouter, LiteLLM 호환 게이트웨이, Codex OAuth, Claude Code CLI.
- **서버 모드**: bounded TCP ACP pool, HTTP 호환 listener, gRPC listener.
- **관측성**: rotation을 지원하는 leveled logging, `/metrics` Prometheus
  metrics.
- **OpenAPI/Swagger**: `/openapi.json`, `/v1/openapi.json`, `/swagger`.
- **플러그인**: SQLite와 DuckDB 확장 표면.
- **런타임 제어**: ACP session에서 terminal/runtime command로 checkpoint,
  rollback, markdown skill injection을 지원합니다.
- **하위 호환성**: 기존 `ACP_HARNESS_*`, `ACP_GLM_*`, `Z_AI_API_KEY`를 계속
  허용합니다.

## 빠른 시작

```bash
go build -o agentbridge ./cmd/agentbridge
```

ACP stdio agent로 실행:

```bash
AGENTBRIDGE_PROVIDER=openai \
AGENTBRIDGE_API_KEY="$OPENAI_API_KEY" \
AGENTBRIDGE_MODEL=gpt-4.1-mini \
./agentbridge
```

HTTP 호환 게이트웨이로 실행:

```bash
AGENTBRIDGE_PROVIDER=glm \
Z_AI_API_KEY="$Z_AI_API_KEY" \
AGENTBRIDGE_GLM_MODEL=glm-5.1 \
./agentbridge --http-listen 127.0.0.1:8766
```

ACP TCP, HTTP, gRPC를 함께 실행:

```bash
./agentbridge \
  --server --listen 127.0.0.1:8765 --pool-size 6 --wait-size 3 \
  --http-listen 127.0.0.1:8766 \
  --grpc-listen 127.0.0.1:8767
```

## HTTP 라우트

- `POST /v1/chat/completions`
- `POST /v1/responses`
- `GET /v1/responses/{id}`
- `POST /v1/messages`
- `POST /v1/a2a/rpc`
- `GET /.well-known/agent-card.json`
- `POST /v1/mcp`
- `POST /v1/agui/run`
- `GET /openapi.json`
- `GET /swagger`
- `GET /metrics`
- `GET /health`

대부분의 호환 라우트는 `/v1` prefix 없이도 동작합니다.

## gRPC

gRPC service 이름은 `agentbridge.v1.AgentService`입니다.

- `Chat`
- `ChatStream`
- `A2A`
- `A2AStream`

request/response는 `google.protobuf.Struct`를 사용하므로 프로젝트 전용 stub
생성 없이 표준 gRPC/protobuf transport를 사용할 수 있습니다.
`grpc.health.v1.Health`도 등록됩니다.

## 설정

새 설정 prefix는 `AGENTBRIDGE_*`입니다.

| 변수 | 용도 |
| --- | --- |
| `AGENTBRIDGE_PROVIDER` | 활성 provider. 기본값: `glm`. |
| `AGENTBRIDGE_MODEL` | 기본 모델 override. |
| `AGENTBRIDGE_API_KEY` | 활성 provider API key. |
| `AGENTBRIDGE_BASE_URL` | base URL override. |
| `AGENTBRIDGE_CONFIG_FILE` | 전체 config YAML override. |
| `AGENTBRIDGE_PROVIDERS_FILE` | provider YAML override. |
| `AGENTBRIDGE_ROUTER_FILE` | Model-router route YAML/JSON override. |
| `AGENTBRIDGE_PLUGINS` | 활성화할 plugin 목록. 예: `sqlite`. |
| `AGENTBRIDGE_LOG_LEVEL` | `trace`, `debug`, `info`, `warn`, `error`, `off`. |
| `AGENTBRIDGE_LOG_FILE` | 선택적 log file 경로. |
| `AGENTBRIDGE_SESSION_DIR` | session 저장 디렉터리. |

기존 `ACP_HARNESS_PROVIDER`, `ACP_HARNESS_API_KEY`, `ACP_GLM_MODEL`,
`ACP_GLM_SESSION_DIR` 등은 하위 호환 alias로 계속 동작합니다.

## 문서

| 영어 | 한국어 |
| --- | --- |
| [Install](docs/install.md) | [설치](docs/ko/install.md) |
| [Configuration](docs/configuration.md) | [설정](docs/ko/configuration.md) |
| [Architecture](docs/architecture.md) | [아키텍처](docs/ko/architecture.md) |
| [CLI Orchestration Design](docs/cli-orchestration.md) | [CLI Orchestration 설계](docs/ko/cli-orchestration.md) |
| [Tool Placement](docs/tool-placement.md) | [Tool Placement](docs/ko/tool-placement.md) |
| [Safety Pipeline](docs/safety.md) | [Safety Pipeline](docs/ko/safety.md) |
| [Providers](docs/providers.md) | [프로바이더](docs/ko/providers.md) |
| [Plugins](docs/plugins.md) | [플러그인](docs/ko/plugins.md) |
| [Testing](docs/testing.md) | [테스트](docs/ko/testing.md) |

## 테스트

```bash
go test ./...
go vet ./...
go build -o agentbridge ./cmd/agentbridge
```

## 라이선스

MIT.
