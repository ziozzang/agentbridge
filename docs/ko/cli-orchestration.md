# CLI Orchestration 설계

AgentBridge는 CLI orchestration을 client-owned harness layer로 봅니다. 서버는
이 layer를 일반 tool처럼 model에 노출할 수 있지만, 실행 위치는 capability가 있는
곳입니다. 즉 terminal client, server process, 또는 다른 client 구현체가 될 수
있습니다.

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

### Terminal UI

`acp-agent`의 기본 interactive shell은 Bubble Tea를 사용합니다. Layer는 다음처럼
나뉩니다.

- ACP transport와 JSON-RPC 처리는 `client`에 남습니다.
- Transport update는 `uiEvent` 값으로 정규화됩니다.
- Bubble Tea model은 viewport, composer, status surface, permission overlay,
  transcript cell을 소유합니다.
- 기존 ANSI/line-oriented UI는 `--plain` 뒤에 debugging/minimal terminal용
  fallback으로 남깁니다.

이 분리는 terminal control을 ACP transport에서 떼어냅니다. 서버는 구조화된
event를 내리고, client가 이를 어떻게 렌더링할지 결정합니다.

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
