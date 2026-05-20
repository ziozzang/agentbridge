# 플러그인

플러그인은 AgentBridge에 선택적 tool definition을 추가합니다.

```bash
AGENTBRIDGE_PLUGINS=sqlite,duckdb,jina,ollama_search,xai,openai_embed agentbridge
```

기존 `ACP_HARNESS_PLUGINS`도 alias로 동작합니다.

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

HTTP compatibility server는 활성 plugin을 MCP로 노출합니다. LLM provider를
선택하거나 호출하지 않아도 tool만 사용할 수 있습니다.

```bash
AGENTBRIDGE_PLUGINS=xai,openai_embed agentbridge --http-listen 127.0.0.1:8766
```

`POST /mcp` 또는 `POST /v1/mcp`에서 MCP `tools/list`, `tools/call`을
사용하세요. `chat` MCP tool도 계속 존재하지만, plugin tool은 독립적으로
목록 조회와 호출이 가능합니다.

## 보안

플러그인은 AgentBridge 프로세스 안에서 같은 권한으로 실행됩니다. 신뢰할 수
있는 플러그인만 활성화하고, write 권한은 필요한 경우에만 켜세요.
