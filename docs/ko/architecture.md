# 아키텍처

AgentBridge는 ACP, A2A, MCP Streamable HTTP, AG-UI, gRPC, OpenAI 호환 HTTP,
Anthropic 호환 HTTP 표면 아래에 하나의 provider abstraction을 두는 protocol
bridge입니다.

## 런타임 구조

프로세스는 세 계층으로 나뉩니다.

1. Protocol adapter가 wire format을 파싱하고 요청을 정규화합니다.
2. Provider 계층이 정규화된 메시지를 upstream model 또는 native agent runtime에
   맞게 변환합니다.
3. 공통 safety/runtime service가 provider를 감싸 PII masking, response
   sanitization, compaction, observability, status reporting을 처리합니다.

중심 contract는 `internal/provider.Provider`입니다.

- `StreamChat`은 정규화된 assistant chunk를 스트리밍합니다.
- `AvailableModels`, `DefaultModel`, `ContextWindow`는 model discovery와 context
  관리에 쓰입니다.
- Optional interface로 native compaction, intention probing, option
  sanitation, provider-native agent-loop 감지를 추가합니다.

## Protocol 표면

stdio/TCP ACP는 `internal/acp`와 `internal/agent`가 처리합니다. Agent는 ACP
session, model/mode state, persistence, notification을 소유합니다.

HTTP 호환 API는 `internal/httpcompat`가 처리합니다. 주요 endpoint는 아래입니다.

- `/v1/chat/completions`
- `/v1/responses`
- `/v1/messages`
- `/v1/embeddings`
- `/v1/rerank`
- `/v1/models`
- A2A, MCP, AG-UI, OpenAPI, Swagger, metrics, health endpoint
- read-only runtime status용 `/v1/providers/status`, `/ui/`

gRPC 호환 계층은 `internal/grpccompat`에 분리되어 있습니다.

## Provider 모드

AgentBridge는 provider를 두 실행 모드로 나눕니다.

### 일반 LLM Provider

일반 provider는 model API를 제공하지만 agentic loop를 직접 소유하지 않습니다.
예: `glm`, `openai-chat`, `openai-responses`, `anthropic`, `google`, `ollama`,
`llama.cpp`, 대부분의 OpenAI 호환 gateway.

ACP에서는 AgentBridge 내장 harness를 탑니다.

1. cwd, project context, tools, profile로 system prompt를 만듭니다.
2. model turn을 스트리밍합니다.
3. 반환된 tool call을 AgentBridge executor가 실행합니다.
4. tool result를 붙이고 model이 멈추거나 `max_turns`에 도달할 때까지 반복합니다.
5. runtime threshold 또는 context overflow error가 발생하면 compaction합니다.

HTTP에서는 `agent:<model>` 또는 `{"agent": true}` metadata로 opt-in한 요청만 같은
harness를 탑니다. 그 외 HTTP 요청은 단일 provider call입니다.

### Provider-Native Agent Provider

Native agent provider는 agentic loop, session runtime, tool execution lifecycle,
일부 compaction 동작을 provider 자체가 소유합니다. 이런 provider는
`provider.NativeAgentProvider`를 구현합니다.

현재 native agent provider는 로컬 `codex app-server` stdio JSON-RPC를 사용하는
`codex-app`입니다.

ACP session에서는 AgentBridge 내장 harness를 bypass합니다.

1. ACP `session/new`가 AgentBridge session id를 만듭니다.
2. Provider는 이 session id를 안정적인 native session anchor로 받습니다.
3. ACP mode는 `provider_native`으로 광고됩니다.
4. `session/prompt`는 provider로 직접 스트리밍됩니다.
5. AgentBridge는 text/usage를 ACP notification으로 mirror하고 가벼운 transcript를
   저장하지만, local tool execution은 호출하지 않습니다.

이 분리는 의도된 것입니다. Native provider를 내장 harness에 다시 태우면 tool
call이나 compaction이 두 번 실행되거나 permission 의미가 충돌할 수 있습니다.

## Session Identity

AgentBridge session id가 protocol 간 기본 session key입니다.

- ACP는 항상 `sessionId`를 가집니다.
- HTTP agent loop는 `metadata.session_id`, `metadata.sessionId`,
  `metadata.thread_id`를 받을 수 있습니다.
- `prompt_cache_key`는 native provider에서 local session affinity key로 사용할
  수 있지만, 항상 upstream cache hint로 전달되는 것은 아닙니다.

`codex-app`에서는 안정적인 `session_id` 또는 `prompt_cache_key`가 있으면 같은
로컬 Codex thread를 재사용합니다. OpenAI 스타일 `prompt_cache_retention`은 이
경로의 Codex app-server wire contract가 아니므로 제거됩니다.

## HTTP Streaming

`POST /v1/chat/completions`는 `stream: true`인 경우 실제 SSE streaming을
사용합니다. 일반 provider는 sanitize가 끝난 provider chunk를 OpenAI 호환
`chat.completion.chunk` event로 즉시 flush합니다.

`agent:<model>` 또는 `metadata.agent`로 HTTP agent loop를 켠 경우,
AgentBridge는 assistant text delta와 loop 진행 상태를 함께 스트리밍합니다.
Tool call, tool status notification, tool completion summary, usage, stop
reason, turn boundary는 `agent_event` field가 있는 chunk object로 내려갑니다.
Raw tool input/output payload는 이런 중간 event에 포함하지 않습니다. Tool
result 자체는 내부 model loop에는 계속 전달됩니다.

A2A streaming과 AG-UI도 같은 agent-loop emitter를 사용합니다. A2A는 assistant
text를 `artifactUpdate`, loop 진행 상태를 `agentUpdate`로 내리고, AG-UI는
assistant text를 `TEXT_MESSAGE_CONTENT`, loop 진행 상태를 `AGENT_EVENT`로
내립니다.

## Compaction

Compaction은 protocol-agnostic 설정입니다.

일반 provider는 아래 순서로 처리합니다.

1. `ConversationCompactor`를 구현한 경우 provider-native compaction.
2. Structured summary fallback.
3. `prune_fallback`이 켜진 경우에만 pruning.

Provider-native agent provider는 이 동작을 재정의할 수 있습니다. `codex-app`은
native thread에 upstream `thread/compact/start`를 호출한 뒤, 로컬 mirror
transcript를 checkpoint message와 최근 turn으로 교체합니다. 깊은 session state의
source of truth는 upstream thread입니다.

## Safety Pipeline

Safety 기능이 켜진 경우 provider 생성은 `internal/provider/pipeline` wrapper를
통과합니다. Wrapper는 아래 optional capability를 보존합니다.

- native compaction
- native agent-loop detection
- stream/compact option sanitation
- intention probing

PII masking은 upstream dispatch 전에 일어납니다. Streaming response는 client로
돌아가기 전에 unmask 및 sanitize됩니다.

HTTP agent-loop 중간 event는 executor의 raw input, raw output, full tool
content를 재귀적으로 제거한 뒤 내보냅니다. 따라서 live tool status는 볼 수
있지만, local file 내용이나 command output이 side-channel event metadata로
스트리밍되지는 않습니다.

## Model Catalog

`/v1/models`는 실제 model id와 provider ownership metadata를 노출합니다.
`glm`, `grok` 같은 synthetic provider 이름은 실제 route alias가 아닌 한 model
id로 내리지 않습니다. Native agent model은 provider-native agent model임을
나타내는 tag와 compat field를 포함합니다.

## Observability

`internal/observability`는 process-local snapshot을 유지합니다.

- active provider name, kind, model, base URL, native-agent 여부
- active HTTP request
- active ACP session
- completed/failed HTTP request counter

`/v1/providers/status`는 JSON snapshot을 반환합니다. `/ui/`는 같은 데이터를
read-only dashboard로 렌더링합니다. 이 화면은 운영 가시성만 제공하며, 설정
변경은 여전히 파일과 환경 변수로 처리합니다.

## Native Provider 로드맵

현재 구조는 추가 native-agent provider를 받을 준비가 되어 있습니다.

- `claude-code`는 one-shot CLI JSON output 대신
  `@anthropic-ai/claude-agent-sdk` 또는 그에 준하는 안정적인 native transport로
  이동하는 것이 맞습니다.
- ACP capable provider는 AgentBridge가 inner harness가 아니라 ACP bridge로
  동작할 때 provider-native agent로 표현할 수 있습니다.
- 추가 native provider는 먼저 session-id 기반 routing을 구현하고, 이후
  provider별 compaction, option sanitation, status probe를 추가하는 순서가
  적절합니다.
