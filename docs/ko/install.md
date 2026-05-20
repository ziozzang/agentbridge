# 설치

AgentBridge는 단일 Go 바이너리로 빌드됩니다.

## 요구 사항

- Go 1.25 이상
- Linux, macOS, WSL 등 POSIX 계열 환경

## 빌드

```bash
git clone https://github.com/ziozzang/agentbridge
cd agentbridge
go build -o agentbridge ./cmd/agentbridge
```

## 컨테이너

```bash
docker build -t agentbridge .
docker run --rm -i \
  -e AGENTBRIDGE_PROVIDER=openai \
  -e AGENTBRIDGE_API_KEY="$OPENAI_API_KEY" \
  -v "$PWD":/workspace -w /workspace \
  agentbridge
```

session을 유지하려면 `/home/agent/.local/state`를 volume으로 mount하세요.

## 에디터 모드

ACP를 지원하는 에디터는 AgentBridge를 직접 실행하면 됩니다.

```bash
agentbridge
```

프로세스는 stdio로 ACP JSON-RPC를 주고받습니다.

## TCP ACP 서버

```bash
agentbridge --server --listen 127.0.0.1:8765 --pool-size 6 --wait-size 3
```

각 TCP 연결은 독립적인 ACP JSON-RPC stream입니다. `--pool-size`는 활성 연결
수, `--wait-size`는 대기 연결 수를 제한합니다.

## HTTP 호환 서버

```bash
AGENTBRIDGE_PROVIDER=glm AGENTBRIDGE_GLM_MODEL=glm-5.1 \
agentbridge --http-listen 127.0.0.1:8766
```

주요 route:

- `POST /v1/chat/completions`
- `POST /v1/responses`
- `GET /v1/responses/{id}`
- `POST /v1/messages`
- `POST /v1/embeddings`
- `POST /v1/rerank`
- `GET /v1/models`
- `POST /v1/a2a/rpc`
- `GET /.well-known/agent-card.json`
- `POST /v1/mcp`
- `POST /v1/tools/{tool-name}`
- `POST /v1/agui/run`
- `GET /openapi.json`
- `GET /swagger`
- `GET /metrics`
- `GET /health`

대부분의 route는 `/v1` 없이도 동작합니다.

서버 flag는 `$XDG_CONFIG_HOME/agentbridge/config.yaml`에도 둘 수 있습니다.
CLI flag가 config보다 우선합니다.

```yaml
server:
  enabled: true
  listen: 127.0.0.1:8765
  pool_size: 6
  wait_size: 3
  http_listen: 127.0.0.1:8766
  grpc_listen: 127.0.0.1:8767
```

## gRPC 호환 서버

```bash
agentbridge --grpc-listen 127.0.0.1:8767
```

서비스 이름은 `agentbridge.v1.AgentService`입니다.

- `Chat`
- `ChatStream`
- `A2A`
- `A2AStream`

request/response는 `google.protobuf.Struct`를 사용합니다.
`grpc.health.v1.Health`도 등록됩니다.

## GLM 최초 설정

```bash
agentbridge --setup
```

GLM/Z.AI key를 `$XDG_CONFIG_HOME/agentbridge/credentials.json` 또는
`~/.config/agentbridge/credentials.json`에 저장합니다. 기존
`glm-acp-agent/credentials.json`도 fallback으로 읽습니다.
