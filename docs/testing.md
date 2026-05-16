# Testing

The harness is fully covered by Go unit and integration tests using
`httptest.Server` to drive realistic SSE/NDJSON streams.

## Run the suite

```bash
go test ./...
```

About 24 packages, finishes in under 10 seconds on a laptop.

## Single-package iteration

```bash
go test ./internal/provider/openaichat/...
go test ./internal/plugins/sqlite/...
go test ./internal/oauth/codex/...
```

## With verbose output / race detector

```bash
go test -race -v ./...
```

## End-to-end smoke (manual)

```bash
go build -o /tmp/acp-harness ./cmd/glm-acp-agent
ACP_HARNESS_LOG_LEVEL=debug \
ACP_HARNESS_LOG_FILE=/tmp/harness.log \
ACP_HARNESS_PROVIDER=openai \
ACP_HARNESS_API_KEY=sk-... \
/tmp/acp-harness < some-acp-script.ndjson
```

Many editors include an "ACP CLI" you can use to drive the harness for
exploratory testing — see the upstream
[Agent Client Protocol](https://github.com/zed-industries/agent-client-protocol)
repo.

## Coverage philosophy

- Every provider adapter has at least one streaming test that mocks the
  upstream API end-to-end (`httptest.Server` → real SSE/NDJSON →
  channel assertions).
- Every plugin has unit tests for load/dispatch/error paths.
- Configuration tests exercise env-precedence, layered merge, and
  variable expansion.
- The logger has tests for level filtering and file rotation.
- The Codex OAuth resolver has tests for cached-token, expired-token
  refresh, and env-only refresh paths.

If you add or change behaviour, add a test — see the equivalent file in
the same package for the established pattern.

## Linting

```bash
go vet ./...
gofmt -l .
```

`go vet` is part of CI; treat warnings as errors.

## Adding regression tests

When you fix a bug, add a regression test that fails on the previous
version and passes on yours. Place it next to the existing tests in
the same package.
