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

회귀 테스트 원칙:

- 변경한 package 근처에 테스트를 추가합니다.
- upstream LLM API는 `httptest.Server`로 mock합니다.
- 실제 provider 테스트는 환경 변수로 opt-in 하도록 둡니다.
