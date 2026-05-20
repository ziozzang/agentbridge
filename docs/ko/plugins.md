# 플러그인

플러그인은 AgentBridge에 선택적 tool definition을 추가합니다.

```bash
AGENTBRIDGE_PLUGINS=sqlite,duckdb,jina,ollama_search,xai,openai_embed agentbridge
```

기존 `ACP_HARNESS_PLUGINS`도 alias로 동작합니다.

활성화된 플러그인을 끄려면 다음처럼 지정합니다.

```bash
AGENTBRIDGE_DISABLED_PLUGINS=xai,sqlite agentbridge
```

검색 기능은 검색 플러그인을 켰을 때 MCP tool로 사용할 수 있습니다.

| 플러그인 | MCP tool 이름 |
| --- | --- |
| `jina` | `plugin__jina__jina_search` |
| `ollama_search` | `plugin__ollama_search__ollama_search` |
| `xai` | `plugin__xai__xai_x_search` |

## SQLite

SQLite plugin은 기본적으로 읽기 전용 catalogue browser입니다.

| 변수 | 용도 |
| --- | --- |
| `AGENTBRIDGE_SQLITE_DIRS` | catalogue directory 목록. 쉼표로 구분. |
| `AGENTBRIDGE_SQLITE_RW` | `1`이면 write statement 허용. |

기본 catalogue:

- `$XDG_DATA_HOME/agentbridge/sqlite`
- `~/.local/share/agentbridge/sqlite`

도구:

- `sqlite_list`
- `sqlite_load`
- `sqlite_unload`
- `sqlite_tables`
- `sqlite_schema`
- `sqlite_query`
- `sqlite_exec` (`AGENTBRIDGE_SQLITE_RW=1`일 때)

## DuckDB

DuckDB plugin은 현재 placeholder입니다. 실제 DuckDB runtime은 CGO와 더 큰
binary가 필요하므로, 이름과 tool surface만 예약되어 있습니다.

## Jina

Jina 플러그인은 공식 Reader, Search, Embeddings API를 도구로 노출합니다.

- Reader: `https://r.jina.ai`
- Search: `https://s.jina.ai`
- Embeddings: `https://api.jina.ai/v1/embeddings`

활성화:

```bash
AGENTBRIDGE_PLUGINS=jina agentbridge
```

| 변수 | 용도 |
| --- | --- |
| `AGENTBRIDGE_JINA_API_KEY` | 선택적 Jina API key. `JINA_API_KEY`도 허용. |
| `AGENTBRIDGE_JINA_READER_BASE_URL` | Reader base URL override. 기본값 `https://r.jina.ai`. |
| `AGENTBRIDGE_JINA_SEARCH_BASE_URL` | Search base URL override. 기본값 `https://s.jina.ai`. |
| `AGENTBRIDGE_JINA_EMBEDDINGS_BASE_URL` | Embeddings API base override. 기본값 `https://api.jina.ai/v1`. |
| `AGENTBRIDGE_JINA_EMBEDDINGS_MODEL` | 기본 embedding model. 기본값 `jina-embeddings-v3`. |

도구:

- `jina_reader`
- `jina_search`
- `jina_embed`

## Ollama Search

Ollama Search 플러그인은 Ollama Cloud 공식 web API를 도구로 노출합니다.

- `POST https://ollama.com/api/web_search`
- `POST https://ollama.com/api/web_fetch`

활성화:

```bash
AGENTBRIDGE_PLUGINS=ollama_search OLLAMA_API_KEY=... agentbridge
```

| 변수 | 용도 |
| --- | --- |
| `AGENTBRIDGE_OLLAMA_SEARCH_API_KEY` | Ollama API key. `OLLAMA_API_KEY`도 허용. |
| `AGENTBRIDGE_OLLAMA_SEARCH_BASE_URL` | Base URL override. 기본값 `https://ollama.com`. |

도구:

- `ollama_search`
- `ollama_fetch`

## xAI Direct Tools

xAI plugin은 `AGENTBRIDGE_PROVIDER=xai`를 쓰지 않아도 xAI 보조 API를
도구로 노출합니다. 가능한 경우 `~/.grok/auth.json` / Hermes 호환
`xai-oauth` credential을 먼저 사용하고, 없으면 `XAI_API_KEY`를 사용합니다.

활성화:

```bash
AGENTBRIDGE_PLUGINS=xai agentbridge --http-listen 127.0.0.1:8766
```

| 변수 | 용도 |
| --- | --- |
| `AGENTBRIDGE_XAI_API_KEY` | xAI API key. `XAI_API_KEY`도 허용. |
| `AGENTBRIDGE_XAI_BASE_URL` | API base override. 기본값 `https://api.x.ai/v1`. |
| `AGENTBRIDGE_XAI_SEARCH_MODEL` | X Search Responses 기본 모델. 기본값 `grok-4.3`. |
| `AGENTBRIDGE_XAI_IMAGE_MODEL` | 이미지 기본 모델. 기본값 `grok-imagine-image`. |
| `AGENTBRIDGE_XAI_OAUTH_PATH` | OAuth auth store 경로 override. 기본값 `~/.grok/auth.json`. |

도구:

- `xai_x_search`: Responses API의 hosted `x_search` tool로 X를 검색합니다.
- `xai_image_generate`: `/v1/images/generations` 호출.
- `xai_image_edit`: `/v1/images/edits` 호출.

## OpenAI 호환 Embeddings

`openai_embed` plugin은 OpenAI 호환 `/embeddings` endpoint를 도구로
노출합니다. LiteLLM, OpenAI, 로컬 vLLM 같은 gateway 테스트에 사용할 수
있습니다.

LiteLLM 예시:

```bash
AGENTBRIDGE_PLUGINS=openai_embed \
AGENTBRIDGE_EMBEDDINGS_BASE_URL=http://127.0.0.1:4000/v1 \
AGENTBRIDGE_EMBEDDINGS_API_KEY=... \
AGENTBRIDGE_EMBEDDINGS_MODEL=text-embedding-3-small \
agentbridge
```

| 변수 | 용도 |
| --- | --- |
| `AGENTBRIDGE_EMBEDDINGS_BASE_URL` | OpenAI 호환 API base. `LITELLM_BASE_URL`, `OPENAI_BASE_URL`, `http://localhost:4000/v1` 순서로 fallback. |
| `AGENTBRIDGE_EMBEDDINGS_API_KEY` | Bearer token. `LITELLM_API_KEY`, `OPENAI_API_KEY`, `AGENTBRIDGE_API_KEY` fallback. |
| `AGENTBRIDGE_EMBEDDINGS_MODEL` | 기본 embedding model. `LITELLM_EMBEDDINGS_MODEL`, `OPENAI_EMBEDDINGS_MODEL`, `text-embedding-3-small` fallback. |
| `AGENTBRIDGE_EMBEDDINGS_FILE` | 외부 model mapping 파일. 있으면 기본값은 `$XDG_CONFIG_HOME/agentbridge/embeddings.json`. |

도구:

- `embed`

외부 model mapping은 gateway가 OpenAI와 다른 model ID를 노출하거나, alias별로
다른 endpoint에 라우팅해야 할 때 사용합니다.

```json
{
  "default": "fast",
  "models": {
    "fast": {
      "base_url": "${LITELLM_OPENAI_BASE_URL}",
      "api_key_env": "LITELLM_OPENAI_API_KEY",
      "model": "jina-embeddings-v5-text-small"
    },
    "local-gemma": {
      "base_url": "http://127.0.0.1:4000/v1",
      "api_key_env": "LITELLM_API_KEY",
      "model": "embeddinggemma-300m"
    }
  }
}
```

사용자가 넘기는 `model` 값은 upstream model ID일 수도 있고 mapping alias일 수도
있습니다. Mapping field는 `${VAR}` 환경 변수 확장을 지원합니다. 파일에
`api_key`를 직접 넣기보다는 `api_key_env`를 쓰는 것을 권장합니다.

## MCP Tool-Only Mode

HTTP compatibility server는 활성 plugin과 설정된 외부 MCP server를 MCP로
노출합니다. LLM provider를 선택하거나 호출하지 않아도 tool만 사용할 수
있습니다.

```bash
AGENTBRIDGE_PLUGINS=xai,openai_embed agentbridge --http-listen 127.0.0.1:8766
```

`POST /mcp` 또는 `POST /v1/mcp`에서 MCP `tools/list`, `tools/call`을
사용하세요. `chat` MCP tool도 계속 존재하지만, plugin tool은 독립적으로
목록 조회와 호출이 가능합니다.

전역 외부 MCP server는 `AGENTBRIDGE_MCP_FILE`로 지정하거나
`$XDG_CONFIG_HOME/agentbridge` 아래 `mcp.yaml` / `mcp.json`으로 등록합니다.

```yaml
mcp_servers:
  - name: search
    type: http
    url: http://127.0.0.1:8090/mcp
    allow_tools: [foo, search*]
    deny_tools: [search_debug]
    inject_tools:
      - name: forced_search
        source_name: search
        description: Search through the upstream MCP server.
        inputSchema:
          type: object
          properties:
            query:
              type: string
    headers:
      Authorization: Bearer ${MCP_TOKEN}
    enabled: true
```

CLI / stdio MCP server도 지원합니다.

```yaml
mcp_servers:
  - name: filesystem
    type: stdio
    command: npx
    args: [-y, "@modelcontextprotocol/server-filesystem", /workspace]
    env:
      NODE_OPTIONS: --no-warnings
    allow_tools: [read_file, list_directory]
```

기존 MCP 설정 호환을 위해 `mcpServers`도 허용하며, list와 이름-keyed object
형식을 모두 받을 수 있습니다. 파일에서 제거하지 않고 끄려면
`disabled: true`, `enabled: false`, 또는
`AGENTBRIDGE_DISABLED_MCPS=search,docs`를 사용하세요.

`allow_tools`를 쓰면 특정 upstream tool 이름만 노출할 수 있습니다.
`deny_tools`는 allow list 적용 후 특정 tool을 숨깁니다. 두 필드는 list 또는
쉼표/개행 구분 문자열을 받으며, `search*` 같은 단순 wildcard를 지원합니다.

`inject_tools`를 쓰면 upstream server가 `tools/list`에서 반환하지 않은 tool
definition도 ACP/MCP/OpenAPI에 강제로 넣을 수 있습니다. Injected tool은
`mcp__<server>__<name>`으로 노출되고 upstream MCP server의 `source_name`을
호출합니다.

외부 MCP tool은 `mcp__<server>__<tool>` 이름으로 노출되며, ACP session과 HTTP
MCP client 양쪽에서 사용할 수 있습니다.

ACP client가 session 생성 시 넘기는 `mcpServers`에도 `type: "stdio"`와 같은
`command`, `args`, `env`, `cwd`, `allow_tools`, `deny_tools` 필드를 사용할 수
있습니다.

## OpenAPI Tool 노출

`GET /openapi.json`, `GET /v1/openapi.json`은 현재 실행 중인 설정을 기준으로
동적으로 생성됩니다. 활성 plugin tool과 설정된 외부 MCP tool은 각 tool의 JSON
schema를 request body schema로 사용해 `POST /v1/tools/<tool-name>` operation으로
추가됩니다.

예:

```bash
curl -sS http://127.0.0.1:8766/v1/tools/plugin__jina__jina_search \
  -H 'content-type: application/json' \
  -d '{"query":"agent protocols"}'
```

고정 client generation이 필요하면 원하는 plugin/MCP 설정으로 AgentBridge를
실행한 뒤 해당 인스턴스의 `/openapi.json` 출력을 저장해서 사용하세요.

## Prometheus Metrics

`GET /metrics`, `GET /metric`은 Prometheus text metrics를 노출합니다. HTTP
route metric은 항상 출력됩니다. MCP와 plugin tool call은 아래 counter로
집계됩니다.

```text
agentbridge_tool_calls_total{kind="plugin",name="plugin__jina__jina_search",status="ok"} 1
agentbridge_tool_calls_total{kind="mcp",name="mcp__search__query",status="error"} 1
```

Tool metric은 HTTP MCP를 통한 호출과, 같은 프로세스에서 metrics endpoint를
제공하는 경우 ACP session 내부에서 모델이 수행한 MCP/plugin 호출을 포함합니다.

## 보안

플러그인은 AgentBridge 프로세스 안에서 같은 권한으로 실행됩니다. 신뢰할 수
있는 플러그인만 활성화하고, write 권한은 필요한 경우에만 켜세요.
