# 테스트

전체 테스트:

```bash
go test ./...
```

정적 검사와 빌드:

```bash
go vet ./...
go build -o agentbridge ./cmd/agentbridge
```

패키지 단위 반복:

```bash
go test ./internal/httpcompat
go test ./internal/grpccompat
go test ./internal/provider/openaichat
```

실제 GLM gRPC smoke:

```bash
ACP_GRPC_REAL_SMOKE=1 \
AGENTBRIDGE_PROVIDER=glm \
AGENTBRIDGE_GLM_MODEL=glm-5.1 \
Z_AI_API_KEY=... \
go test ./internal/grpccompat -run TestRealGLMSmoke -count=1 -v
```

수동 HTTP smoke:

```bash
AGENTBRIDGE_PROVIDER=glm Z_AI_API_KEY=... \
agentbridge --http-listen 127.0.0.1:8766

curl -sS http://127.0.0.1:8766/health
curl -sS http://127.0.0.1:8766/openapi.json
curl -sS http://127.0.0.1:8766/metrics
```

HTTP catalog / compaction smoke:

```bash
curl -sS http://127.0.0.1:8766/v1/models | jq '.data[] | {id, owned_by, metadata}'
curl -sS http://127.0.0.1:8766/v1/tool-catalog | jq '.tools[] | {name, source, owner}'
curl -sS -X POST http://127.0.0.1:8766/v1/responses/compact \
  -H 'Content-Type: application/json' \
  -d '{"strategy":"prune","target_tokens":64,"messages":[{"role":"system","content":"Be concise."},{"role":"user","content":"Long history to compact."},{"role":"assistant","content":"Intermediate answer."},{"role":"user","content":"Keep recent context."}]}'
```

Safety primitive package 테스트:

```bash
go test ./internal/pii ./internal/sanitize ./internal/responsecache ./internal/runtimeconfig
```

Safety pipeline primitive는 모든 request path에 연결되기 전까지 별도 package
단위로 검증합니다. HTTP, A2A, ACP, router dispatch에 연결할 때 integration
test를 추가하세요.

Codex live web search smoke:

```bash
AGENTBRIDGE_PROVIDER=codex \
CODEX_WEB_SEARCH=live \
CODEX_WEB_SEARCH_CONTEXT_SIZE=low \
agentbridge --http-listen 127.0.0.1:8766

curl -sS -X POST http://127.0.0.1:8766/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.5","input":"Use live web search. What is the current top headline title on the OpenAI News page? Include the source URL.","store":false}'
```

xAI Grok OAuth resolver smoke:

```bash
AGENTBRIDGE_PROVIDER=xai-oauth agentbridge --http-listen 127.0.0.1:8766
curl -sS -X POST http://127.0.0.1:8766/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"grok-4.3","input":"Say OK","store":false}'
```

이 테스트는 유효한 `~/.grok/auth.json` 또는
`AGENTBRIDGE_XAI_OAUTH_ACCESS_TOKEN`이 필요합니다. xAI가 계정 tier를
제한하면 OAuth inference가 HTTP 403을 반환할 수 있습니다. 그 경우
`XAI_API_KEY`와 `AGENTBRIDGE_PROVIDER=xai` 경로를 테스트하세요.

HTTP agent loop smoke:

HTTP 호환 엔드포인트에서 ACP와 같은 로컬 도구 루프까지 실행하려면
`agent:<model>`을 쓰거나 request metadata에 `{"agent": true}`를 넣습니다.
`metadata.cwd`는 파일/쉘 도구의 작업 디렉토리입니다.

반대로 `codex-app` 같은 native agent provider는 ACP에서 이미 로컬 harness를
bypass합니다. 활성 provider가 native-agent capable이면
`/v1/chat/completions`도 provider 자체의 session/runtime을 사용합니다.

```bash
AGENTBRIDGE_PROVIDER=xai-oauth agentbridge --http-listen 0.0.0.0:8766
curl -sS -X POST http://127.0.0.1:8766/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"agent:grok-4.3","metadata":{"cwd":"'$PWD'","max_turns":6},"messages":[{"role":"user","content":"Use list_files and run_command to inspect the current directory and summarize the environment."}]}'
```

Streaming smoke:

Token flush 확인에는 `curl -N`을 사용합니다. Agent-loop streaming에서는
`agent_event` chunk에서 `turn_start`, `tool_call`, `session/update`,
`tool_result`를 확인합니다. 이 event들은 의도적으로 raw tool input/output을
포함하지 않습니다.

```bash
curl -N -sS http://127.0.0.1:8766/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"agent:glm-5.1","stream":true,"metadata":{"cwd":"'$PWD'","max_turns":3},"messages":[{"role":"user","content":"Use list_files on . and answer with the first three names."}]}'
```

Permission smoke:

Runtime config에 `agent.yolo_mode: false`를 설정한 뒤 HTTP agent loop에 파일
쓰기를 요청합니다. Stream에는 `session/request_permission`, failed tool status가
나와야 하고 실제 파일은 생성되지 않아야 합니다. `yolo_mode: true` 또는 기존처럼
설정을 생략한 경우 write/execute tool은 permission prompt를 bypass합니다.

회귀 테스트 원칙:

- 변경한 package 근처에 테스트를 추가합니다.
- upstream LLM API는 `httptest.Server`로 mock합니다.
- 실제 provider 테스트는 환경 변수로 opt-in 하도록 둡니다.
