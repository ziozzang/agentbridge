# CLI Orchestration 설계

AgentBridge는 CLI orchestration을 client-owned harness layer로 봅니다. 서버는
이 layer를 일반 tool처럼 model에 노출할 수 있지만, 실행 위치는 capability가 있는
곳입니다. 즉 terminal client, server process, 또는 다른 client 구현체가 될 수
있습니다.

Worker-node model에서 `acp-agent`는 내장 terminal worker입니다. Shell execution,
Lua orchestration, file attachment, CLI-local memory/queue state가 이 worker의
capability입니다.
Active worker는 CLI runtime state의 일부입니다. TUI status surface는 compact worker
label을 보여주고, `/status`와 `/structure`는 worker id, kind, capability, permission
policy, cancellation 지원 여부를 노출합니다.

## Placement

Tool ownership은 두 종류입니다.

- Server-owned tool은 AgentBridge가 구현하고 server executor가 실행합니다.
- Client-owned tool은 ACP client가 `initialize.clientCapabilities.clientTools`로
  광고합니다. AgentBridge는 이를 model에 `client__<name>`으로 노출하고,
  호출을 `client/call_tool`로 client에 다시 라우팅합니다.

`acp-agent`는 `run_lua`와 `run_command`를 광고하고, model은 이를
`client__run_lua`, `client__run_command`로 봅니다. 이 구조 덕분에 local file,
shell execution, UI state, text handling, timer, CLI control은 CLI에 남기면서도
LLM이 orchestration layer를 tool로 호출할 수 있습니다. 서버는 shell command나
shell script를 실행하지 않습니다.

더 넓은 ownership/permission 규칙은 [Tool Placement](tool-placement.md)에
정리합니다.

## Layers

Layer separation은 ACP TUI 작업의 완료 조건입니다. 단순한 구현 취향이 아닙니다.
Transport, command execution, rendering, permission selection, provider
streaming을 한 컴포넌트에 섞어서 동작만 맞춘 변경은 완료로 보지 않습니다. 모든
변경은 code, test, 이 문서에서 ownership boundary가 보이도록 유지해야 합니다.

### Terminal UI

`acp-agent`의 기본 interactive shell은 Bubble Tea를 사용합니다. Layer는 다음처럼
나뉩니다.

- ACP transport와 JSON-RPC 처리는 `client`에 남습니다.
- Transport update는 `uiEvent` 값으로 정규화됩니다.
- Bubble Tea model은 viewport, composer, status surface, permission overlay,
  transcript cell을 소유합니다.
- Transcript rendering은 별도 surface입니다. History cell을 정규화한 뒤 렌더링하고,
  viewport는 렌더링된 transcript string을 소비합니다.
- Transcript body text는 viewport 폭에 맞춰 wrap합니다. Assistant output,
  thinking, tool detail, error, info cell의 긴 provider/tool line이 terminal layout을
  가로로 깨지 않도록 합니다.
- Viewport refresh는 wrapped transcript surface를 직접 사용합니다. 따라서
  auto-follow와 manual scroll preservation은 사용자가 보는 렌더링 결과와 같은
  content 기준으로 동작합니다.
- Transcript rendering은 명시적인 dirty flag 뒤에 cache됩니다. Spinner tick,
  status-only update, 기타 frame change는 cell이나 viewport width가 바뀌지 않았다면
  전체 transcript를 다시 wrap하지 않아야 합니다.
- Status/notice rendering도 별도 surface입니다. Running activity, token progress,
  queue/subagent/tool count, scroll state, context, quota, worker placement,
  permission mode, session identity를 frame layout과 분리해서 계산합니다.
- Status surface는 하단 고정 한 줄입니다. 긴 상태 정보는 wrap하지 않고 ANSI-aware
  truncate해서 좁은 터미널에서도 transcript/composer layout을 유지합니다.
- Frame도 이 fixed-row status contract를 방어적으로 강제합니다. Status text에
  newline이 있거나 폭을 넘더라도 추가 terminal row를 만들면 안 됩니다.
- Composer 위 notice row도 같은 fixed-row 규칙을 따릅니다. 긴 progress text,
  stop prompt, completion hint는 frame assembly 전에 truncate되어 composer/status
  row를 밀어내지 않습니다.
- Notice surface가 이 truncation을 직접 소유합니다. Frame이 방어적으로 다시 truncate할 수
  있지만, notice surface 호출자는 이미 single fixed-width line을 받아야 합니다.
- Permission approval overlay도 별도 surface입니다. Choice list 렌더링과
  number/arrow/yes/no key를 ACP permission reply로 매핑하는 책임을 가집니다.
- Approval overlay는 자기 width와 wrapping을 소유합니다. 긴 title, command detail,
  choice label, replacement input은 transcript 위에 합성되기 전에 terminal width 안에
  들어가야 합니다.
- Approval overlay는 transcript surface를 세로로 키울 수도 없습니다. 너무 긴
  overlay는 사용 가능한 transcript row 수에 맞춰 clamp되어 notice, composer, 하단
  고정 status line이 제자리에 남아야 합니다.
- Approval reply는 같은 key event 안에서 overlay를 닫습니다. 다음 frame은 server
  traffic을 기다리지 않고 transcript와 bottom composer 상태로 돌아옵니다.
- `other command`처럼 replacement text가 필요한 permission choice도 Bubble Tea
  overlay 안에서 처리합니다. Terminal raw mode 상태에서 stdin을 직접 읽지 않습니다.
- Permission overlay와 client-owned tool call은 active prompt context를 공유합니다.
  Ctrl-C는 pending prompt와 대기 중인 permission/client tool request를 함께 취소해서
  UI 뒤에 goroutine이 막힌 상태로 남지 않게 합니다.
- Completion hint도 별도 surface입니다. Slash-command argument hint와 compact
  suggestion text를 소유하고, composer는 text input만 소유합니다.
- Completion 적용도 key layer의 책임입니다. Tab은 일반 composer text handling 전에
  completion surface를 통해 slash command를 완성하고, Ctrl-N/Ctrl-P는 textinput
  suggestion navigation key로 남깁니다.
- Bottom composer도 별도 surface입니다. Fixed-width input rendering을 frame
  assembly에서 분리해서 transcript와 독립적으로 발전시킬 수 있습니다.
- Composer row도 fixed-height입니다. 긴 입력 text는 composer surface에서
  ANSI-aware truncate되어 status line으로 wrap되지 않습니다.
- 최상위 frame도 별도 surface입니다. Transcript, overlay, notice, composer,
  status row를 조립하지만 transport나 command behavior는 소유하지 않습니다.
- Frame은 fixed notice, composer, status row를 제외하고 남은 row 수에 맞춰
  transcript block도 방어적으로 clamp합니다. 잘못되었거나 너무 큰 transcript
  surface가 하단 shell row를 화면 밖으로 밀어내면 안 됩니다.
- Fixed shell row 개수는 reflow와 frame rendering이 공유하는 layout contract입니다.
  Event-loop code 안에서 임의 산술식으로 중복하면 안 됩니다.
- Composer와 overlay input width도 같은 규칙을 따릅니다. Terminal width 산술은
  layout helper가 소유하고, runtime handler는 계산된 dimension만 적용합니다.
- TUI component construction은 runtime update loop 밖에 둡니다. 그래서 composer,
  spinner, viewport, 초기 model state를 terminal program 실행 없이 테스트할 수 있습니다.
- Bubble Tea program option은 하나의 lifecycle helper에서 만듭니다. Interactive
  program은 caller context와 alternate-screen mode를 함께 받으므로 외부 취소와
  terminal 소유권이 명시적으로 유지됩니다.
- Keyboard handling은 작은 key layer로 분리합니다. Global interrupt/exit key가
  가장 먼저 처리되고, overlay는 selection key를, transcript viewport는 scroll key를,
  composer는 일반 text navigation key를 소유합니다.
- Interrupt key는 Bubble Tea update function 안에서 network cancellation을
  동기 실행하면 안 됩니다. Key event는 local UI state를 즉시 바꾸고, cancellation은
  Bubble Tea command로 예약해서 rendering과 이후 high-priority key가 계속 반응하도록
  유지합니다.
- Key handler는 직접 렌더링하지 않습니다. 처리된 key path는 변경된 model을 Bubble
  Tea update loop에 돌려주고, 해당 key message의 단일 viewport refresh는 update
  tail이 소유합니다.
- Bubble Tea update loop는 window resize, key routing, ACP UI event, command
  completion, spinner tick, composer update용 작은 handler를 거쳐 message를
  처리합니다. 그래서 terminal program을 띄우지 않고 runtime event loop를 테스트할
  수 있습니다.
- Composer update는 key-layer fallback일 때만 실행합니다. Window resize, spinner
  tick, ACP UI event, command completion message는 textinput component로 보내지
  않습니다.
- Buffered ACP UI event는 update loop에 들어가기 전에 제한된 batch로 drain합니다.
  Streaming delta burst는 event 순서를 유지하면서 하나의 Bubble Tea update에서
  적용하고, 다음 event wait는 계속 명시적으로 예약합니다.
- TUI event channel buffer와 batch limit은 이름 있는 runtime constant입니다.
  Responsiveness를 조정하는 지점이므로 event loop 안의 literal로 숨기면 안 됩니다.
- Batched UI event는 model state를 순서대로 변경한 뒤 batch 마지막에 viewport를 한 번만
  refresh합니다. Batch 내부에서 event마다 transcript refresh를 반복하지 않습니다.
- ACP event handler는 직접 렌더링하지 않습니다. Transcript state를 dirty로 만들고
  다음 wait를 예약하며, 해당 message의 단일 viewport refresh는 Bubble Tea update
  tail이 소유합니다.
- Terminal resize event는 같은 update loop를 통해 viewport와 composer를 reflow하고,
  매우 작은 terminal 크기도 유효한 component size로 clamp합니다. Reflow는 dimension과
  dirty state만 갱신하고, resize message의 viewport refresh는 update tail이 소유합니다.
- Stop request는 같은 key event 안에서 즉시 transcript cell을 추가합니다. Update
  tail이 반환 전에 viewport를 refresh하므로 다음 provider event가 오기 전에도
  interrupt feedback이 화면에 보입니다.
- Turn 실행 중 들어온 prompt는 client queue에 들어가고 state/info event로 방출되어
  transcript와 status surface 양쪽에 렌더링됩니다.
- Local slash command는 결과 cell 전에 command cell을 방출합니다. `/help`,
  `/status`, permission 변경, Lua orchestration 같은 client-side command도
  transcript에서 입력과 출력을 구분해서 볼 수 있습니다.
- Local slash command 실행은 provider prompt busy state와 분리해서 추적합니다.
  Bubble Tea command 안에서 실행 중인 명령은 model turn이 busy인 것처럼 가장하지
  않고 notice/status surface에 표시되어야 합니다.
- Local slash command는 command별 cancellable context로 실행합니다. Model turn은
  busy가 아니지만 local command가 실행 중이면, Ctrl-C는 먼저 그 command를 취소하고
  다음 Ctrl-C에서 client를 종료합니다.
- Ctrl-D, `/quit`, 두 번째 Ctrl-C 같은 exit path는 local cleanup을 소유합니다.
  Bubble Tea program이 종료되기 전에 pending permission overlay, prompt context,
  choice waiter, local command context를 취소해야 합니다.
- `client__run_command` 같은 worker-node routed client tool은 CLI process에서
  실행됩니다. 하지만 위임된 worker action도 현재 turn의 일부이므로 lifecycle은 TUI
  tool cell과 active tool count에 보여야 합니다.
- Tool/subagent state update는 UI state event를 emit하기 전에 client state lock을
  해제해야 합니다. UI event pipeline은 client state를 snapshot하므로, mutex를 잡은
  상태에서 emit하면 Bubble Tea runtime이 deadlock될 수 있습니다.
- 최소 line-oriented fallback은 `--plain` 뒤에 debugging/minimal terminal용으로
  남깁니다. 이 경로는 terminal layout을 소유하지 않습니다.

이 분리는 terminal control을 ACP transport에서 떼어냅니다. 서버는 구조화된
event를 내리고, client가 이를 어떻게 렌더링할지 결정합니다.
디버깅할 때는 `acp-agent --json-events`로 Bubble Tea renderer를 우회하고 같은
정규화 event stream을 newline-delimited JSON으로 볼 수 있습니다.

Lua API는 primitive function과 composition function으로 구성합니다. Primitive는
하나의 관심사를 직접 다루고, composition은 primitive를 조립해서 LLM workflow를
만듭니다.

### Data

Primitives:

- `cli.data.attach(path)`
- `cli.data.files()`
- `cli.data.clear_files()`

Compositions:

- `cli.data.extract(text, schema, opts)`
- `cli.data.rank(candidates, criteria, opts)`
- `cli.data.research_source(topic, opts)`

Data tool은 local file, extracted text, ranked candidates, research source plan
같은 model context를 준비합니다.

### Memory

Primitives:

- Turn memory: `cli.memory.get/set/delete/list`
- Working memory: `cli.memory.kv_get/kv_set/kv_delete/kv_list`
- Searchable memory: `cli.memory.put/search`
- Structured memory: `cli.util.sql_query/sql_exec`

Persistent SQLite store의 기본 위치는
`$XDG_STATE_HOME/agentbridge/acp-agent/orchestration.sqlite`이며
`AGENTBRIDGE_CLI_ORCH_DB`로 바꿀 수 있습니다. 기본 `kv`, `events`, `jobs`,
`memories`, `artifacts` table을 초기화합니다.

구분은 의도적으로 유지합니다.

- Turn memory는 한 Lua 실행 동안만 쓰는 scratch state입니다.
- KV는 turn 사이에 유지되는 작은 working memory입니다.
- SQLite table은 job queue, event log, observation, artifact, long-running
  orchestration state를 담습니다.

### Orchestration

Primitives:

- `cli.orch.plan(items)`
- `cli.orch.fetch_next_job(plan)` / `next_job(plan)`
- `cli.orch.run(job, fn)`
- `cli.orch.check_status(plan)` / `status_line(plan)`
- `cli.orch.trigger(name, predicate, action)`
- `cli.orch.run_triggers(ctx, triggers)`
- `cli.orch.steer(ctx, directive)`
- `cli.orch.timer(opts)` / `tick(timer)`
- `cli.orch.cron(opts, fn)`

Compositions:

- `cli.orch.control_loop(opts)`
- `cli.orch.reflect(ctx, opts)`
- `cli.orch.judge(goal, evidence, opts)`
- `cli.orch.delegate(task, opts)`

의도한 control loop는 아래 형태입니다.

`plan -> fetch_next_job -> run -> check_status -> trigger -> steer/continue/stop`

`control_loop`, `run`, local goal harness는 실행 중 `orch:*` UI event를 냅니다.
따라서 Bubble Tea transcript와 `--json-events` mode에서 Lua script가 끝나기 전에도
job start, job completion, failure, loop completion을 볼 수 있습니다.

Trigger는 observation과 control의 경계입니다. Trigger는 loop를 멈추거나, steering
directive를 쓰거나, context compact/save를 실행하거나, job을 enqueue하거나,
delegated work를 실행할 수 있습니다.

### Maintenance

Primitives:

- `cli.maint.status()`
- `cli.maint.structure()`
- `cli.maint.snapshot()`
- `cli.maint.context()`
- `cli.maint.compact(target)`
- `cli.maint.save(name)`

Maintenance 함수는 현재 상태를 보여주고, script가 명시적으로 요청한 경우에만
session을 변경합니다.

### Utilities

Primitives:

- `cli.util.now()`
- `cli.util.time_unix()`
- `cli.util.sleep_ms(ms)`
- `cli.util.emit(name, payload)`
- `cli.util.sql_query(sql)`
- `cli.util.sql_exec(sql)`

`emit`은 `events` table에도 기록합니다.

### LLM Composition

Compositions:

- `cli.llm.ask(prompt, opts)`
- `cli.llm.reflect(ctx, opts)`
- `cli.llm.judge(goal, evidence, opts)`
- `cli.llm.extract(text, schema, opts)`
- `cli.llm.rank(candidates, criteria, opts)`
- `cli.llm.summarize(text, opts)`
- `cli.llm.critic(planOrAnswer, opts)`

이 함수들은 model이 충분한 맥락으로 사용할 prompt를 만듭니다. 기본값은 prompt
문자열 반환입니다. `opts.run = true`이면 `cli.prompt(...)`를 호출합니다.
`opts.store_key` 또는 `opts.memory`를 주면 KV나 searchable memory에도 저장합니다.

## 테스트로 유지할 사례

### Goal Loop

Goal loop는 현재 goal을 저장하고, plan을 만들고, job을 실행하며, `judge`로
목표별 평가 prompt를 만들고, `jobs` table을 갱신합니다. 충분히 완료되면 trigger가
steering directive를 쓰고 loop를 멈춥니다.

`acp-agent`는 이 설계 위에 작은 local goal harness를 제공합니다.

- `/goal` 또는 `/goal status`는 `cli.goal.status()`를 읽습니다.
- `/goal set TEXT`는 local orchestration KV store에 goal을 저장합니다.
- `/goal run`은 현재 ACP session으로 goal-specific prompt를 보냅니다.
- `/goal clear`는 local goal을 제거합니다.

Goal 판단은 의도적으로 CLI-owned입니다. 서버는 결과 ACP prompt/tool traffic만
보며 ACP session에 canonical goal field를 저장하지 않습니다.

### Autoresearch

Autoresearch는 `research_source`로 source plan을 만들고, KV와 memory에 저장하고,
extraction job을 실행하고, evidence를 rank하며, coverage가 충분하면 멈춥니다.

### Memory-Backed Queue

Long-running workflow는 SQLite에 job을 저장하고, SQL로 다음 job을 고르며,
observation을 `memories`에 쓰고, 상태 변화마다 event를 남깁니다.

### Maintenance Steering

Workflow는 context pressure나 risk condition이 보이면 trigger에서
`cli.maint.context`, `compact`, `save`를 호출할 수 있습니다.

## Safety Boundaries

- Lua는 server harness가 아니라 client runtime에서 실행됩니다.
- Client-owned tool은 `client__*` namespace로 노출됩니다.
- Shell command와 shell script는 client-owned입니다. `client__run_command`를
  사용하고 server-owned shell execution은 추가하지 않습니다.
- Terminal UI는 의도적으로 client surface입니다. User message, assistant
  stream, thinking, tool lifecycle update, status card, approval overlay는 ACP
  update를 바탕으로 로컬에서 렌더링합니다. Server는 terminal control sequence가
  아니라 structured event를 계속 내려야 합니다.
- Active prompt 중에는 재귀적인 `cli.prompt(...)` 호출을 피해야 합니다.
- `sql_query`는 read-only입니다. `sql_exec`는 local orchestration DB만 변경합니다.
- Local file/attachment access는 의도적으로 client-local placement입니다.
