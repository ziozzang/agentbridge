# Testing

Run the full suite:

```bash
go test ./...
```

Run static checks and build:

```bash
go vet ./...
go build -o agentbridge ./cmd/agentbridge
```

Package-level iteration:

```bash
go test ./internal/httpcompat
go test ./internal/grpccompat
go test ./internal/provider/openaichat
```

Real GLM gRPC smoke test:

```bash
ACP_GRPC_REAL_SMOKE=1 \
AGENTBRIDGE_PROVIDER=glm \
AGENTBRIDGE_GLM_MODEL=glm-5.1 \
Z_AI_API_KEY=... \
go test ./internal/grpccompat -run TestRealGLMSmoke -count=1 -v
```

Manual HTTP smoke:

```bash
AGENTBRIDGE_PROVIDER=glm Z_AI_API_KEY=... \
agentbridge --http-listen 127.0.0.1:8766

curl -sS http://127.0.0.1:8766/health
curl -sS http://127.0.0.1:8766/openapi.json
curl -sS http://127.0.0.1:8766/metrics
```

HTTP catalog and compaction smoke:

```bash
curl -sS http://127.0.0.1:8766/v1/models | jq '.data[] | {id, owned_by, metadata}'
curl -sS http://127.0.0.1:8766/v1/tool-catalog | jq '.tools[] | {name, source, owner}'
curl -sS -X POST http://127.0.0.1:8766/v1/responses/compact \
  -H 'Content-Type: application/json' \
  -d '{"strategy":"prune","target_tokens":64,"messages":[{"role":"system","content":"Be concise."},{"role":"user","content":"Long history to compact."},{"role":"assistant","content":"Intermediate answer."},{"role":"user","content":"Keep recent context."}]}'
```

Safety primitive package tests:

```bash
go test ./internal/pii ./internal/sanitize ./internal/responsecache ./internal/runtimeconfig
```

The safety pipeline primitives are intentionally tested separately until they
are wired into every request path. Add integration tests when connecting them
to HTTP, A2A, ACP, or router dispatch.

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

This requires a valid `~/.grok/auth.json` or
`AGENTBRIDGE_XAI_OAUTH_ACCESS_TOKEN`. OAuth inference may return HTTP 403
when xAI gates the account tier; in that case test `AGENTBRIDGE_PROVIDER=xai`
with `XAI_API_KEY`.

HTTP agent loop smoke:

Use `agent:<model>` or set request metadata `{"agent": true}` to make the
HTTP compatibility endpoint run the same local tool loop used by ACP before
returning the final assistant text. `metadata.cwd` controls the working
directory for file and shell tools.

```bash
AGENTBRIDGE_PROVIDER=xai-oauth agentbridge --http-listen 0.0.0.0:8766
curl -sS -X POST http://127.0.0.1:8766/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"agent:grok-4.3","metadata":{"cwd":"'$PWD'","max_turns":6},"messages":[{"role":"user","content":"Use list_files and run_command to inspect the current directory and summarize the environment."}]}'
```

Regression policy:

- Add tests beside the package you change.
- Mock upstream LLM APIs with `httptest.Server`.
- Keep real-provider tests opt-in through environment variables.
