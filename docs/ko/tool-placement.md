# Tool Placement

AgentBridge는 tool 실행 위치를 ownership으로 분리합니다. 이것은 단순한 구현
세부사항이 아니라 보안과 배치 경계입니다.

Worker node는 이 ownership model 뒤에 있는 execution endpoint입니다. Terminal,
container, remote worker까지 포함한 더 넓은 placement model은
[Worker Nodes](worker-nodes.md)를 참고합니다.

## Server-Owned Tool

Server-owned tool은 AgentBridge 프로세스 안에서 실행되며
`internal/tools/definitions`에 정의됩니다.

현재 server-owned tool:

- `read_file`
- `write_file`
- `list_files`
- `web_search`
- `web_reader`
- `image_analysis`
- `client_run_lua` compatibility bridge

Server-owned file tool은 ACP session cwd 기준으로 path를 해석합니다. Write는 ACP
permission 경로를 사용합니다. 서버는 shell execution을 server-owned tool로
노출하면 안 됩니다.

## Client-Owned Tool

Client-owned tool은 ACP client가
`initialize.clientCapabilities.clientTools`로 광고합니다. AgentBridge는 이를
model-facing tool call에서 `client__<name>`으로 바꾸고, 실행은
`client/call_tool`로 client에 다시 라우팅합니다.

`acp-agent`가 현재 광고하는 tool:

- `run_lua`, model에는 `client__run_lua`로 노출
- `run_command`, model에는 `client__run_command`로 노출

Shell command와 shell script는 이 client-owned layer에 있어야 합니다. Terminal
client가 interactive user, local tty policy, permission mode, cwd를 소유하기
때문입니다. AgentBridge는 model tool call을 중개할 수 있지만 command를 직접
실행하지 않습니다.

Worker-node 관점에서 `acp-agent`는 local terminal worker입니다. `run_command`는
AgentBridge server action이 아니라 이 worker node의 action입니다.

## Permission Model

`acp-agent`의 client-owned shell execution은 CLI permission mode를 따릅니다.

- `--permission prompt`: 실행 전 stderr/stdin으로 확인합니다.
- `--permission allow` 또는 `--yolo`: 확인 없이 실행합니다.
- `--permission reject` 또는 `--read-only`: 실행을 거부합니다.
- `--permission cancel`: 실행을 취소합니다.

Server-owned write operation은 계속 ACP `session/request_permission`을 사용합니다.
HTTP agent-loop 요청은 interactive terminal이 없으므로 write/execute posture는
runtime 설정으로 정합니다.

## Provider-Native Agent

`codex-app` 같은 provider-native agent provider는 자체 runtime, tool lifecycle,
compaction behavior를 이미 소유합니다. AgentBridge는 이런 provider에 대해 standard
harness를 bypass합니다. Client-owned tool은 ACP client capability이므로,
provider-native transport에서 이를 호출하려면 provider별 지원이 별도로 필요합니다.
