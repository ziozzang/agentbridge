# Safety Pipeline

AgentBridge는 `gollm`의 safety / request mutation 메커니즘을 흡수하고
있습니다. 이 기능들은 특정 provider 설정이 아니라 ACP, A2A, OpenAI 호환 HTTP,
Anthropic 호환 HTTP, 이후 추가될 protocol 표면에 동일하게 적용되어야 하므로
별도 문서로 분리합니다.

## 의도

목표 request lifecycle은 아래 순서입니다.

1. Declarative inject rule 적용.
2. 지원하지 않거나 보내면 안 되는 top-level request parameter 제거.
3. Upstream 호출 전 PII detect 및 선택적 mask.
4. 선택된 provider 또는 router route로 dispatch.
5. Response에서 PII placeholder rollback.
6. 설정된 경우 thinking tag 또는 structured reasoning field 제거.
7. PII가 감지되지 않은 eligible non-streaming response만 cache.

순서가 중요합니다. PII masking은 upstream dispatch 전에 일어나야 하고,
response rollback은 최종 response sanitize 전에 일어나야 하며, response cache는
PII 감지 요청을 피해야 합니다.

## 현재 상태

| 영역 | 상태 | 구현된 부분 |
| --- | --- | --- |
| PII detection/masking | primitive 있음 | `internal/pii`에 한국/일반 default pattern, salted placeholder, dedupe, mapping rollback, provider message walker, streaming unmask 지원이 들어갔습니다. |
| Thinking tag sanitize | primitive 있음 | `internal/sanitize`가 `<think>`, `<thinking>`, `<reasoning>`, `<reflection>` span을 제거하고 split streaming chunk도 처리합니다. |
| Response cache | primitive 있음 | `internal/responsecache`가 stable SHA-256 JSON key, TTL, max-size eviction, stats를 가진 in-memory cache를 제공합니다. |
| Config schema | 있음 | `runtimeconfig`가 `pii`, `sanitize`, `cache`, `inject`를 `config.yaml`에서 읽습니다. |
| Parameter drop | inject schema로 있음 | `inject[].remove`가 top-level parameter drop을 표현합니다. 자동 적용은 아직 대기 상태입니다. |
| JSON mode | provider-level workaround 있음 | OpenAI 호환 provider는 `extra.request_defaults`로 `response_format`을 보낼 수 있습니다. Top-level `inject[].set.response_format`은 parse되지만 전역 적용은 아직 연결 전입니다. |
| Header changes | static header 있음 | Provider `headers:`는 이미 upstream으로 전송됩니다. Kong-style add/set/remove/rename/replace transform은 아직 대기 상태입니다. |
| Request path wiring | 대기 | 아직 모든 request path에 자동 적용되지는 않습니다. `runProvider`, router dispatch, streaming wrapper에 연결해야 runtime behavior로 볼 수 있습니다. |
| `/v1/providers/status` | 대기 | 아직 mount되지 않았습니다. 목표 shape는 provider health, request/error count, quota state, response time, cache stats입니다. |

## 설정

`$XDG_CONFIG_HOME/agentbridge/config.yaml` 또는 `AGENTBRIDGE_CONFIG_FILE`로
지정한 파일에 둡니다.

```yaml
pii:
  enabled: true
  mask: true
  disable_defaults: false
  routing:
    reject: false
    reject_message: "PII detected"
    route_to: local-private-model
  patterns:
    - name: account_id
      regex: '\bACCT-[0-9]{8}\b'
      mask: '[MASK_ACCOUNT_{n}]'

sanitize:
  strip_think_tags: true
  tags: [think, thinking, reasoning, reflection]

cache:
  enabled: true
  ttl: 1h
  max_size: 10000
  models_to_cache: [gpt-*, claude-*]

inject:
  - when: "grok-*, glm-*"
    system_prompt: "Return concise operational answers."
    system_prompt_mode: prepend
    user_suffix: "\n\nReturn only the final answer."
    remove: [logprobs, top_logprobs]
    request_regex:
      - pattern: '\bSECRET:\s*\S+'
        replace: 'SECRET: [redacted]'
        roles: [user]
```

## PII Detection

Default pattern은 아래를 대상으로 합니다.

- 한국 주민등록번호 형태
- 한국 휴대전화 및 유선전화 번호
- Email 주소
- Credit-card-like 숫자
- IPv4 주소
- JWT처럼 보이는 token

Custom `pii.patterns`는 default에 추가됩니다. 같은 `name`을 쓰면 해당 default를
대체합니다. `mask` 문자열은 `{n}` counter를 사용하고, AgentBridge는 request별
salt를 붙여 placeholder가 일반 사용자 텍스트와 충돌하지 않게 합니다.

`pii.mask: false`는 detect-only mode를 위한 설정입니다. 이 경우 routing이나
reject는 가능하지만 upstream으로 보내는 prompt는 바꾸지 않습니다. Request path
wiring이 끝나면 `pii.routing.reject`가 `route_to`보다 우선해야 합니다.

## Message Sanitization

`sanitize.strip_think_tags`는 assistant text에서 model-visible chain-of-thought
tag를 제거합니다. 기본 tag catalog는 아래입니다.

- `think`
- `thinking`
- `reasoning`
- `reflection`

Streaming stripper는 tail buffer를 유지하므로 SSE chunk 사이에서 tag가 갈라져도
제거할 수 있습니다. Provider-native structured reasoning은 별도입니다. 설정이
연결되면 adapter layer에서 thinking delta를 버리거나 생략하는 방식으로 처리해야
합니다.

## Inject Rules

Inject rule은 model name으로 match되는 declarative request edit입니다. `when`은
정확한 이름, `*` wildcard, comma-separated pattern을 받습니다.

지원 필드:

| Field | 용도 |
| --- | --- |
| `set` | Top-level request field overlay. |
| `prepend_messages` | Client history 앞에 message 삽입. |
| `system_prompt` | 첫 system message 추가 또는 수정. |
| `system_prompt_mode` | `prepend`, `append`, `replace`. |
| `user_prefix` | 첫 user message prefix. |
| `user_suffix` | 마지막 user message suffix. |
| `remove` | Top-level request field 제거. |
| `request_regex` | Role별 message text regex replace. |

이 기능은 request normalize, unsupported parameter 제거, 작은 policy prompt에
적합합니다. 큰 agent prompt는 agent profile에 두는 편이 낫습니다.

## JSON Mode

관련 메커니즘은 두 가지입니다.

- Provider-level JSON mode: OpenAI Chat Completions 호환 provider에서는
  `providers.<name>.extra.request_defaults.response_format`을 사용합니다.
- Pipeline-level JSON mode: `inject`가 전역으로 연결되면
  `inject[].set.response_format`으로 public model name별 JSON 강제를 걸 수
  있습니다.

Provider-level 예시:

```yaml
providers:
  openrouter:
    kind: openai-chat
    extra:
      request_defaults:
        response_format:
          type: json_object
```

Pipeline-level 목표 예시:

```yaml
inject:
  - when: "json-*"
    set:
      response_format:
        type: json_object
```

JSON validation은 아직 강제하지 않습니다. 현재 의미는 request shaping이며,
post-response validation은 별도 구현이 필요합니다.

## Header Changes

Static provider header는 이미 지원됩니다.

```yaml
providers:
  openrouter:
    headers:
      HTTP-Referer: https://example.com
      X-Title: AgentBridge
```

목표 transform model은 `gollm`과 같은 action을 지원해야 합니다.

| Action | 용도 |
| --- | --- |
| `add` | Header value append. |
| `set` | Header value replace. |
| `remove` | Header 삭제. |
| `rename` | 한 header 이름의 값을 다른 이름으로 이동. |
| `replace` | Match되는 header value 치환. |

이 transform layer는 아직 대기 상태입니다. 그 전까지는 upstream header에 static
provider `headers:`를 사용하고, dynamic downstream rewrite에 의존하지 마세요.

## Response Cache

Response cache는 작고 deterministic한 cache입니다.

- In-memory only.
- Namespace와 canonical JSON payload의 SHA-256 hash로 key 생성.
- TTL 만료는 read 시 lazy 처리.
- `max_size` 초과 시 가장 이른 expiry부터 evict.
- Streaming response는 cache하지 않아야 합니다.
- Tool 사용 요청 또는 PII 감지 요청은 cache하지 않아야 합니다.

목표는 동일한 non-sensitive, non-streaming 요청의 upstream 호출을 줄이는
것입니다. Durable storage가 아닙니다.

## Provider Status 목표

`/v1/providers/status`는 이 pipeline의 운영자용 상태 화면으로 추가할 예정입니다.
목표 응답에는 아래 정보가 들어가야 합니다.

- provider name, kind, 필요한 경우 origin 수준으로 redacted base URL
- health state: `healthy`, `unhealthy`, `rate_limited`, `unknown`
- request/success/error count
- error rate 및 최근 latency 추정값
- quota state: remaining requests/tokens, reset time, last quota reason
- 가능한 경우 active concurrency/session count
- response cache stats

Endpoint가 mount되기 전까지는 `/metrics`, router log, provider error를
운영 가시성에 사용합니다.

## Rollout Notes

현재 코드는 reusable primitive와 config shape를 먼저 추가한 상태입니다. 다음
구현 단계는 protocol별로 중복하지 않고 공통 HTTP/A2A/ACP execution path에
연결하는 것입니다.

연결할 때 아래 invariant를 유지해야 합니다.

```text
request inject/drop -> PII mask -> provider call -> PII unmask -> sanitize -> cache/store
```

Storage policy를 명확히 검토하기 전에는 masked request를 response state나
response cache에 저장하지 않습니다.
