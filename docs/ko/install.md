# 설치

AgentBridge는 단일 Go 바이너리로 빌드됩니다. 저장소에는 별도 터미널 ACP
client인 `acp-agent`도 포함됩니다.

## 요구 사항

- Go 1.25 이상
- Linux, macOS, WSL 등 POSIX 계열 환경

## 빌드

```bash
git clone https://github.com/ziozzang/agentbridge
cd agentbridge
go build -o agentbridge ./cmd/agentbridge
go build -o acp-agent ./cmd/acp-agent
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

## 터미널 ACP Client

`acp-agent`는 TCP ACP server에 접속해서 Claude CLI 같은 터미널 세션을
제공합니다. AgentBridge server와는 별도 컴포넌트입니다.

```bash
agentbridge --server --listen 127.0.0.1:8765
acp-agent --addr 127.0.0.1:8765 --model glm-5.1
```

기본 interactive UI는 Bubble Tea application입니다. 이벤트 루프는 TUI runtime이
소유합니다. ACP update는 UI event로 변환되고, viewport는 transcript scrolling을,
bottom composer는 입력을, fixed status surface는 model/mode/session/context/quota
상태를 담당합니다. User message, assistant stream, thinking, tool, status,
approval은 별도 history cell로 렌더링합니다. Tool permission은 Codex 스타일
overlay와 숫자/커서 선택지로 묻습니다. Prompt 실행 중 `Ctrl-C`는 먼저
`session/cancel`을 보내고, active prompt가 없을 때는 client를 종료합니다. Prompt
실행 중 입력된 추가 prompt는 queue에 들어가며 `/queue`로 확인할 수 있습니다.
Shell 실행은 계속 client-owned tool입니다.

`--plain`을 주면 최소 line-oriented fallback을 사용합니다. Bubble Tea를 우회하고
plain text를 출력하므로 최소 터미널이나 디버깅에 쓰기 위한 경로입니다.

`--json-events` 또는 `--json`은 protocol-style debugging용입니다. Bubble Tea
renderer를 끄고, stdin 한 줄을 하나의 prompt 또는 slash command로 읽으며,
동일하게 정규화된 UI event를 newline-delimited JSON으로 출력합니다. User input,
assistant delta, thinking delta, tool lifecycle, permission request, status update,
Lua orchestration event를 순서대로 확인할 수 있습니다. 터미널 렌더링 없이 TUI 동작을
재현하는 stdio-friendly 경로입니다.

단발 prompt:

```bash
acp-agent --addr 127.0.0.1:8765 --model codex-agent \
  -p "Inspect the current directory and summarize it."
```

주요 flag:

- `--cwd DIR`: ACP session 작업 디렉터리.
- `--model MODEL`: `session/set_model`로 선택할 model 또는 agent profile.
- `--mode MODE`: `default`, `accept_edits`, `bypass_permissions` 같은 ACP
  permission mode.
- `--permission prompt|allow|reject|cancel`: `session/request_permission`에
  terminal client가 응답하는 방식.
- `--yolo`: `--mode bypass_permissions --permission allow` 축약.
- `--read-only`: `--mode default --permission reject` 축약.
- `-p, --prompt TEXT`: 단발 prompt를 보내고 종료합니다.
- `--show-thinking`: ACP `agent_thought_chunk` update를 stderr에 출력합니다.
  기본값은 숨김입니다.
- `--plain`: Bubble Tea UI를 끄고 최소 line-oriented fallback을 사용합니다.
- `--json-events`, `--json`: Bubble Tea UI를 끄고 정규화된 UI event를
  newline-delimited JSON으로 출력합니다.

대화형 command:

- `/status`: 주소, session id, cwd, model, mode, permission 처리 방식,
  thinking 표시, tool 표시, raw update 표시 상태를 보여줍니다.
- `/sessions`: 현재 cwd의 ACP session 목록을 보여줍니다.
- `/resume SESSION_ID`: 저장된 session을 history replay 없이 resume합니다.
- `/session-load SESSION_ID`: 저장된 session을 load하고 message를 replay합니다.
- `/save NAME`: 현재 session에 runtime checkpoint를 저장합니다.
- `/list`: 현재 session의 runtime checkpoint 목록을 보여줍니다.
- `/load NAME|ID`: runtime checkpoint로 되돌리고 session cache epoch를 올립니다.
- `/context`: 추정 context token, context window, compaction threshold, target,
  message 수, checkpoint 수, cache epoch를 보여줍니다.
- `/attach PATH [...]`: 로컬 파일을 추출해서 다음 prompt에 ACP resource block으로
  첨부합니다. Markdown, text, JSON/YAML/CSV, source file 등 UTF-8 text file은
  직접 읽습니다. PDF는 `pdftotext`가 있으면 사용하고, 없으면 printable text
  fallback을 사용합니다.
- `/files`: 다음 prompt에 첨부될 file queue를 보여줍니다.
- `/clear-files`: 첨부 file queue를 비웁니다.
- `/structure`: 현재 session id, cwd, model, mode, project context file, queued
  attachment 구조를 보여줍니다.
- `/lua FILE [args...]`: `acp-agent` 안에서 로컬 Lua controller script를 실행합니다.
  CLI는 `clientRunLua` capability를 광고하고, 서버가 보내는 `client/run_lua`
  JSON-RPC 요청도 처리합니다. 또한 client-owned `run_lua` tool을 광고하며,
  client-owned `run_command` shell tool도 광고합니다. AgentBridge는 이를 model에
  `client__run_lua`, `client__run_command`로 노출하고 `client/call_tool`로 CLI에
  다시 라우팅합니다. Lua API는 `cli.say(text)`, `cli.status()`,
  `cli.structure()`, `cli.prompt(text)`, `cli.attach(path)`, `cli.files()`,
  `cli.clear_files()`, `cli.command(line)`입니다.
- `/goal [status|set TEXT|run|clear]`: local Lua goal harness를 사용합니다.
  Goal은 server session이 아니라 CLI orchestration store에 저장됩니다.
  `/goal run`은 현재 ACP session으로 goal-specific prompt를 보냅니다.
- `/compact [TARGET_TOKENS]`: 현재 transcript를 수동 compaction합니다. 가능한 경우
  오래된 turn을 summary로 교체하고 cache epoch를 올립니다.
- `/new`: 같은 cwd에서 새 session을 만듭니다.
- `/stop`: 현재 session에 `session/cancel`을 보냅니다. Prompt가 실행 중일 때
  `Ctrl-C`도 같은 동작을 합니다.
- `/queue`: 현재 active prompt 뒤에서 대기 중인 prompt queue를 보여줍니다.
- `/subagent [--model MODEL] TASK`: 서버에 bounded child provider call을 실행하게 하고
  결과를 현재 session으로 돌려받습니다. Subagent는 active skill과 tool name을
  상속하고, tool trace를 남기며, 부모 loop와 같은 compaction 경로를 사용합니다.
  Context overflow 후에는 compaction 뒤 1회 재시도하고, 설정된 depth를 넘는
  recursive nesting은 거부합니다.
- `/skill list|status|clear|NAME`: `.agentbridge/skills` 또는
  `$XDG_CONFIG_HOME/agentbridge/skills`의 markdown skill을 목록/상태/해제/활성화합니다.
- `/model [MODEL]`: model 확인 또는 변경.
- `/mode [MODE]`: ACP mode 확인 또는 변경.
- `/permission [prompt|allow|reject|cancel]`: permission 처리 방식 확인 또는
  변경.
- `/thinking [on|off|toggle]`: thinking 표시 확인 또는 변경.
- `/tools [on|off|toggle]`: tool status 표시 확인 또는 변경.
- `/raw [on|off|toggle]`: raw update 표시 확인 또는 변경.
- `/help`
- `/exit` 또는 `/quit`

## HTTP 호환 서버

```bash
AGENTBRIDGE_PROVIDER=glm AGENTBRIDGE_GLM_MODEL=glm-5.1 \
agentbridge --http-listen 127.0.0.1:8766
```

주요 route:

- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/responses/compact`
- `GET /v1/responses/{id}`
- `POST /v1/messages`
- `POST /v1/embeddings`
- `POST /v1/rerank`
- `GET /v1/models`
- `GET /v1/providers/status`
- `POST /v1/a2a/rpc`
- `GET /.well-known/agent-card.json`
- `POST /v1/mcp`
- `GET /v1/mcp/catalog`
- `GET /v1/tool-catalog`
- `POST /v1/tools/{tool-name}`
- `POST /v1/agui/run`
- `GET /openapi.json`
- `GET /swagger`
- `GET /ui/`
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
