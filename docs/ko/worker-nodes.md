# Worker Nodes

AgentBridge에서 worker node는 action이 실제로 실행되는 위치를 설명하는 개념입니다.
이는 placement와 permission boundary이며, model, provider, protocol adapter,
Bubble Tea UI layer와 분리됩니다.

## 정의

Worker node는 agent loop를 대신해 subtask나 tool action을 실행할 수 있는 execution
endpoint입니다. Worker는 terminal local일 수도 있고, AgentBridge server process,
container, remote host일 수도 있습니다.

Worker node가 소유하는 것:

- action이 실행되는 runtime
- action이 보는 filesystem, process table, network, environment
- state를 변경하거나 민감한 local data를 읽는 action에 대한 permission prompt 또는
  policy
- 이미 위임된 작업의 cancellation과 status reporting

Worker node가 소유하지 않는 것:

- provider streaming 또는 model selection
- protocol wire parsing
- Bubble Tea rendering
- 명시적인 worker-local log/artifact를 제외한 transcript storage

## 내장 Worker Node: ACP CLI

`acp-agent`는 local worker node 기능을 일부 내장합니다. 현재 worker capability는
아래와 같습니다.

- `run_command`, model에는 `client__run_command`로 노출
- `run_lua`, model에는 `client__run_lua`로 노출
- prompt 전에 수행되는 local file attachment/extraction
- temporary memory, queue state, goal helper 같은 CLI orchestration state

Shell execution은 가장 명확한 예입니다. Bash command는 terminal worker에서 실행되어야
합니다. 이 worker가 사용자의 cwd, environment, TTY policy, permission prompt를
소유하기 때문입니다. AgentBridge server는 model tool call을 중개하지만 shell command를
직접 실행하지 않습니다.

CLI는 이 placement 상태를 세 곳에 노출합니다.

- fixed TUI status surface는 active worker kind와 capability 개수를 보여줍니다.
- `/status`는 worker id, kind, capability list, permission policy, cancellation
  지원 여부를 출력합니다.
- `/structure`는 session/context state 옆에 `worker:` block을 포함합니다.

현재 내장 worker id는 `acp-agent:local`이고 kind는 `terminal`입니다. 이는 provider나
model name이 아니라 runtime state surface입니다.

## Orchestrator 방향성

AgentBridge는 작은 control plane에 가까운 orchestrator/master-node 모델로 확장할 수
있어야 합니다. 이 설계에서 orchestrator가 모든 tool을 직접 실행하지는 않습니다. 대신
여러 ACP server, agent session, worker node를 조율합니다.

가능한 orchestrator 책임은 아래와 같습니다.

- 사용 가능한 ACP server와 worker node directory 유지
- node capability, health, locality, permission policy 광고
- subtask와 tool call을 적절한 node로 routing
- downstream node 인증 위임 또는 대행
- client가 모든 downstream credential을 직접 들고 있지 않아도 되도록 auth/session
  proxy 역할 수행
- 하나의 logical workspace에 여러 ACP server node가 붙는 경우 이를 조정
- session placement metadata와 resume routing 보유
- node 전체의 progress, cancellation, metric, audit event 집계

이는 설계 방향성이지, distributed orchestration이 오늘 완성되었다는 의미는 아닙니다.
현재 code는 여전히 boundary를 지켜야 합니다. Server는 중개하고, terminal worker는 local
terminal action을 실행하며, 향후 orchestrator는 placement를 명시적으로 선택합니다. 첫
milestone은 directory, capability, placement, auth delegation, audit event의 명확한
contract입니다. Clustering이나 automatic scheduling은 provider code 안에 숨기지 말고
그 contract 위에서 따라와야 합니다.

## 향후 Worker Node

같은 모델로 추가 worker node를 붙일 수 있습니다.

- sandboxed subtask용 container worker
- distributed build/test job용 remote host worker
- 비싼 preprocessing을 위한 GPU/media worker
- UI automation용 browser worker
- 특수 MCP-backed worker

각 worker는 capability, health, permission을 명시적으로 광고해야 합니다. Model은
암묵적인 server side effect가 아니라 namespaced tool 또는 routing hint를 보아야 합니다.

## Routing 규칙

Worker routing은 [CLI Orchestration Design](cli-orchestration.md)에 문서화된 layer
boundary를 보존해야 합니다.

- Agent loop는 subtask 또는 tool action이 필요하다고 판단합니다.
- Placement layer는 capability, policy, locality, user approval을 기준으로 worker
  node를 선택합니다.
- Worker는 action을 실행하고 structured progress, result, error, cancellation event를
  방출합니다.
- UI는 event를 렌더링합니다. UI가 worker를 실행하지 않습니다.
- Provider adapter는 model chunk를 스트리밍합니다. Provider adapter가 worker action을
  실행하지 않습니다.

여러 위치에서 실행할 수 있는 task라면, 검사하거나 수정할 resource에 가장 가까운 worker를
선택합니다. 예를 들어 local process inspection은 CLI worker에 속하고, containerized test
run은 container worker에 속합니다.

## Cancellation

Worker action은 active prompt 또는 orchestration context에 묶여야 합니다. Ctrl-C나 명시적
stop request는 pending worker request, permission prompt, delegated command를 취소해야
합니다. 어떤 worker가 action을 취소할 수 없다면 그 제한을 capability contract에 표시해야
합니다.

## Completion Gate

Worker-node 변경은 code와 documentation에서 아래 항목이 보여야 완료입니다.

- 어떤 worker가 execution을 소유하는가
- capability를 어떻게 광고하는가
- permission을 어떻게 결정하는가
- progress/result/error를 어떻게 표면화하는가
- cancellation이 어떻게 전파되는가
- 어떤 layer가 해당 action 실행을 하면 안 되는가
