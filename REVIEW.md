# Code Review — agentbridge (Deep Review)

**Date**: 2026-05-21
**Reviewer**: agentbridge (self-review via sub-agent deep analysis)
**Scope**: 전체 코드베이스 (~39,000줄 Go)
**Overall rating**: B+ (중상급)

---

## 전반적 평가

상용 수준의 실무적 Go 프로젝트로, 아키텍처 설계 의도는 명확하고 기능 범위도 넓습니다. 하지만 확장 과정에서 생긴 구조적 부채가 눈에 띕니다.

---

## 강점

### 1. 인터페이스 설계가 깔끔함

`internal/provider/provider.go`의 `Provider` 인터페이스 계층이 잘 분리되어 있습니다:

- 핵심 `Provider` 인터페이스는 5개 메서드로 최소화
- `ConversationCompactor`, `NativeAgentProvider`, `IntentionProber` 등 선택적 인터페이스로 확장
- `pipeline.WrapFromConfig`로 데코레이터 패턴 적용

### 2. 테스트 커버리지가 꽤 좋음

- `recorderConn`, `fakeConn`, `streamingServer`, `nativeTestProvider` 등 고품질 테스트 더블
- 권한 거부/취소/승인 분기, 최대 턴 소진, 컨텍스트 취소 등 엣지 케이스 테스트
- HTTP 스트리밍 서버를 활용한 통합 테스트

### 3. 동시성 제어가 정확함

- `sync.Mutex`로 `sessions` 맵 보호, `promptMu`로 프롬프트 직렬화
- `cancelMu`로 취소 함수 보호, `sync.Once`로 연결 종료
- `atomic.Int64`로 RPC ID 생성

### 4. 파일 퍼미션/보안 의식

- 세션 파일 `0600`, 디렉토리 `0700`
- 세션 ID 패턴 검증 (`^[a-zA-Z0-9_-]+$`)
- 경로 해석이 `SessionCwd` 기준 상대 경로로 동작

### 5. 프로바이더 어댑터 구현이 견고함

`openaichat`과 `anthropic` 어댑터가 SSE 스트리밍, 툴 콜 어셈블리, 프롬프트 캐싱, 컨텍스트 오버플로우 감지를 각각 정확하게 구현합니다. 프로바이더 간 번역 로직(OpenAI↔Anthropic 메시지 변환)이 명확하고 테스트 가능한 단일 함수로 캡슐화되어 있습니다.

### 6. 설정 레이어링이 유연함

`config.Load()`의 4단계 오버레이(임베디드 기본값 → XDG 사용자 파일 → 오버라이드 파일 → 환경변수)와 `${VAR:-default}` 확장은 프로덕션 배포에서 매우 실용적입니다.

### 7. 플러그인 아키텍처가 확장 가능함

`plugin__<name>__<tool>` 네임스페이스 컨벤션, init-time 레지스트리, disabled-set 필터링이 깔끔하게 설계되어 있습니다.

---

## 약점 및 개선 필요 사항

### 1. `agent.go`가 God Object가 되어가고 있음 (~900줄)

`Agent` 구조체가 너무 많은 책임을 집중합니다:

- 세션 생명주기 (New/Load/Resume/Fork/Close/List)
- 프롬프트 루프 (표준 + 네이티브)
- 런타임 명령 (skill/btw/compact/context/subagent)
- OAuth 해석, 플러그인 로딩, 비전 클라이언트 초기화
- 모델/프로필/툴 관리

`runtime_commands.go`, `subagent.go`로 일부 분리한 건 좋은 시작이지만, 세션 관리와 프롬프트 루프도 별도 타입으로 추출하면 가독성이 크게 좋아질 것입니다.

### 2. LoadSession / ResumeSession의 대규모 코드 중복

두 메서드가 거의 동일한 ~50줄 블록을 복제하고 있습니다. MCP 연결, 모드 판별, 세션 복원 로직이 3번 반복됩니다 (NewSession까지 포함하면 4번). 공통 헬퍼로 추출이 필요합니다.

**중복 패턴** (4개 메서드에서 반복):
```go
// 모드 결정 로직 (4회 반복)
nativeAgent := provider.UsesNativeAgentLoop(a.Provider)
if mode == "" {
    if nativeAgent { mode = ModeProviderNative } else { mode = ModeDefault }
}
if nativeAgent { mode = ModeProviderNative } else if mode == "" { mode = ModeDefault }

// MCP 연결 로직 (4회 반복)
var mcpClient sessionMcpClient
if nativeAgent { tools = nil } else if specs, err := configuredMCPServers(p.MCPServers); err != nil { ... }
```

### 3. `Content any` 타입 사용

`Message.Content`가 `any`라서 런타임 타입 스위치가 여러 곳에 흩어져 있습니다:

```go
func stringContent(content any) string {
    switch c := content.(type) {
    case string: ...
    case []any: ...
```

`Content`를 구조체 합집합(union)으로 정의하거나 최소한 `ContentString() string` 메서드를 `Message`에 두면 타입 안전성이 올라갑니다.

**영향받는 위치**:
- `agent.go`: `stringContent()`
- `openaichat/openaichat.go`: `markMessageCacheControl()` — `content any`에 대해 `string`/`[]any`/`[]map[string]any`/`nil` 분기
- `anthropic/anthropic.go`: `contentToString()` — `nil`/`string`/`[]any` 분기
- `compaction/compaction.go`: `EstimateTokens()` — `string`/`[]any`/`default` 분기

### 4. 에러 처리의 불일치

- 어떤 곳은 `*acp.RPCError`를 반환 (`session not found`)
- 어떤 곳은 `fmt.Errorf`를 반환 (`model stream failed: %w`)
- `Store.Load`는 파일 에러를 조용히 무시 (`return nil, nil // mirror TS swallow`)
- `persistSession`의 에러를 `_ = a.persistSession(s)`로 무시
- `acp.Conn.handleInbound`에서 `_ = json.Unmarshal(msg.Params, &p)`로 언마샬 에러 무시

일관된 에러 래핑 전략이 필요합니다.

### 5. `cmd/acp-agent/main.go`가 단일 파일에 ~800줄

클라이언트 타입, JSON-RPC 레이어, REPL, 명령 핸들러, Lua 실행이 한 파일에 섞여 있습니다. 이미 `lua.go`, `shell.go`가 untracked로 존재하므로 분리를 진행 중인 것으로 보입니다.

### 6. 매직 맵으로 업데이트 전달

```go
a.notifyUpdate(p.SessionID, map[string]any{
    "sessionUpdate": "agent_message_chunk",
    "content":       map[string]any{"type": "text", "text": c.Text},
})
```

모든 업데이트가 `map[string]any`로 전달되어 필드명 오타를 컴파일 타임에 잡을 수 없습니다. 구조체 타입을 정의하고 `json.Marshal`에 맡기는 것이 안전합니다.

**영향받는 타입 시그니처**:
- `acp.SessionUpdateParams.Update map[string]any`
- `acp.RequestPermissionParams.ToolCall map[string]any`

### 7. GLM 레거시 의존성 잔존

`internal/provider/glm` 패키지가 `Message`, `Chunk`, `ModelInfo`, `StreamOptions` 등의 타입을 정의하고, `internal/provider`에서 이를 alias로 재사용하는 구조입니다. AGENTS.md에 "provider-neutral"이라고 명시되어 있지만 실제로는 `glm.Message`가 코드 전반에 퍼져 있습니다. `provider.Message`로의 마이그레이션이 완료되지 않은 상태입니다.

**영향 범위**: `glm.go`의 SSE 파싱/툴콜 어셈블리 로직이 `openaichat/openaichat.go`와 거의 동일합니다. 둘 다 OpenAI Chat Completions 형식을 사용하므로 `glm`을 `openai-chat` 설정 프리셋으로 통합할 수 있습니다.

### 8. SSE 스트리밍 파서 중복 (심층 분석)

`glm.go`의 `StreamChat`과 `openaichat.go`의 `StreamChat`이 SSE 라인 파싱, `streamChunk` 구조체, `deltaToolCall` 집계, 툴콜 인덱스 정렬+플러시 로직을 90% 이상 동일하게 복제합니다. `glm`은 사실상 `openai-chat`의 하드코딩된 GLM 설정이므로, `glm` 패키지를 `openaichat`의 프리셋으로 통합하면 수백 줄의 중복이 제거됩니다.

### 9. 컨텍스트 오버플로우 감지 로직 분산

컨텍스트 오버플로우 감지가 3곳에 개별적으로 구현되어 있습니다:

| 위치 | 방식 |
|------|------|
| `openaichat/openaichat.go` | `isContextOverflowCode(code, message)` — code "1261"/"context_length_exceeded" + 메시지 키워드 |
| `anthropic/anthropic.go` | `isContextOverflowText(msg)` — "prompt is too long" + 메시지 키워드 |
| `agent/agent.go` | `provider.IsContextOverflow(err) \|\| (errors.As(err, &apiErr) && apiErr.IsContextOverflow())` |

`glm.APIError.IsContextOverflow()`도 별도로 존재합니다. 이 로직이 provider 인터페이스 계층으로 통합되면 에이전트 루프의 fallback 로직이 단순해집니다.

### 10. 설정에서 `Extra map[string]any` 남용

`provider.Config.Extra`가 `map[string]any`로, 프로바이더별 설정을 런타임에 느슨하게 접근합니다:

```go
func (c *Client) extraString(key string) string { ... }  // openaichat에 있음
func (c *Client) extraString(key string) string { ... }  // anthropic에도 있음 (중복)
func (c *Client) extraBoolValue(key string) (bool, bool) { ... }
```

- `openaichat.go`: `extraString`, `extraBoolValue`, `extraIntString`, `asMap` (4개 헬퍼)
- `anthropic.go`: `extraString` (1개 헬퍼, 동일 구현)

이 헬퍼들이 패키지 간에 복제되어 있습니다. `Config`에 typed accessor를 추가하거나 `extraString`을 공유 유틸로 추출해야 합니다.

### 11. 프롬프트 캐시 로직의 복잡도

`openaichat`의 `shouldApplyPromptCache`가 프로바이더 이름과 모델 이름 기반 휴리스틱으로 캐시 적용 여부를 결정합니다:

```go
isClaude := strings.Contains(m, "claude")
isQwen := strings.Contains(m, "qwen")
isOpenRouter := strings.Contains(base, "openrouter.ai")
```

이러한 하드코딩된 문자열 매칭은 새 프로바이더가 추가될 때마다 분산되어 유지보수가 어렵습니다. 캐시 전략을 설정 파일에서 선언적으로 제어하는 것이 좋습니다.

### 12. `firstNonEmpty` 중복 정의

같은 시그니처의 `firstNonEmpty` 함수가 5개 패키지에 독립적으로 정의되어 있습니다:

- `internal/agent/agent.go`
- `internal/tools/executor/executor.go`
- `internal/provider/openaichat/openaichat.go`
- `internal/provider/anthropic/anthropic.go`
- `internal/config/config.go`

공통 유틸 패키지(`internal/util` 또는 `internal/helper`)로 추출해야 합니다.

### 13. 서브에이전트 구현의 제한점

`subagent.go`의 `runSubagent`가 부모 세션의 툴 정의를 전달하지 않고 `Tools: nil`로 고정합니다:

```go
system := systemprompt.Build(systemprompt.Input{
    Cwd:      parent.Cwd,
    Tools:    nil,  // 툴 없음
    AgentsMD: systemprompt.LoadProjectContext(parent.Cwd),
})
```

이것은 의도된 제한일 수 있지만(AGENTS.md에 "should stay small"으로 명시), 툴이 필요한 하위 작업에는 사용할 수 없습니다. 문서화가 명시적이지 않습니다.

### 14. `Store.Load`의 조용한 실패

```go
func (s *Store) Load(sessionID string) (*PersistedSession, error) {
    ...
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            return nil, nil
        }
        return nil, nil // mirror TS swallow
    }
```

`os.ErrNotExist`가 아닌 에러(예: 권한 거부, 디스크 오류)도 조용히 무시됩니다. 최소한 로그를 남기거나 `os.ErrNotExist`만 무시해야 합니다.

### 15. 레이스 컨디션 가능성

`Agent.visionClient()`가 `a.Vision`을 lazy-init하면서 뮤텍스 보호 없이 필드를 읽고 씁니다:

```go
func (a *Agent) visionClient() imagepre.VisionClient {
    if a.Vision == nil {       // 읽기 (락 없음)
        ...
        a.Vision = visionmcp.New(apiKey)  // 쓰기 (락 없음)
    }
```

`ensureClient()`도 `a.mu`로 보호하지만, `visionClient()`는 별도로 호출될 수 있어 같은 락 범위 밖에서 실행됩니다.

---

## 영역별 평가

| 영역 | 평가 | 비고 |
|------|------|------|
| 아키텍처 설계 | ★★★★☆ | 계층 분리 의도는 좋으나 agent.go 집중 |
| 인터페이스 추상화 | ★★★★★ | Provider 계층, Plugin 계층 모두 우수 |
| 테스트 품질 | ★★★★☆ | 핵심 경로는 잘 커버, provider 테스트 격리 우수 |
| 동시성 안전성 | ★★★★☆ | 대부분 정확하나 visionClient 락 누락 |
| 코드 중복 | ★★☆☆☆ | SSE 파서, 설정 접근자, 세션 관리 로직 중복 심함 |
| 타입 안전성 | ★★★☆☆ | Content any, map[string]any 남용 |
| 에러 처리 일관성 | ★★★☆☆ | Store.Load 조용한 실패, persistSession 무시 |
| 모듈 분리 | ★★★☆☆ | glm/openaichat 분리 불완전 |
| 설정 시스템 | ★★★★☆ | 레이어링 우수, Extra 타입 안전성 부족 |
| 보안 | ★★★★☆ | 퍼미션, 파일 권한, 입력 검증 양호 |

---

## 권장 리팩토링 우선순위

### P0 (즉시)
1. **세션 관리 코드 중복 제거** — `NewSession`/`LoadSession`/`ResumeSession`의 공통 로직을 `restoreSession` 헬퍼로 추출
2. **glm/openaichat 통합** — `glm`의 SSE 파싱 로직을 `openaichat` 프리셋으로 통합, 중복 코드 수백 줄 제거
3. **`firstNonEmpty` 공통화** — 5개 패키지에 분산된 동일 함수를 공유 유틸로 추출

### P1 (단기)
4. **`agent.go` 책임 분리** — 세션 매니저, 프롬프트 루프, 툴 매니저를 별도 타입으로 분리
5. **`Message.Content` 타입 안전화** — `any` 대신 구조체 기반 합집합 타입 도입, `ContentString()` 메서드 추가
6. **`Store.Load` 에러 처리** — `os.ErrNotExist`만 무시하고 나머지는 로그/전파

### P2 (중기)
7. **업데이트 알림 구조체화** — `map[string]any`를 타입화된 구조체로 교체
8. **에러 처리 표준화** — 일관된 에러 래핑과 `persistSession` 에러 전파
9. **`Config.Extra` 타입 안전화** — typed accessor를 공유 유틸로 추출
10. **컨텍스트 오버플로우 감지 통합** — provider 계층으로 통합
11. **`visionClient` 뮤텍스 보호** — lazy-init에 락 추가
12. **프롬프트 캐시 설정 선언화** — 프로바이더 이름 기반 휴리스틱을 설정으로 대체
