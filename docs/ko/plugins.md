# 플러그인

플러그인은 AgentBridge에 선택적 tool definition을 추가합니다.

```bash
AGENTBRIDGE_PLUGINS=sqlite,duckdb,jina,ollama_search agentbridge
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

## 보안

플러그인은 AgentBridge 프로세스 안에서 같은 권한으로 실행됩니다. 신뢰할 수
있는 플러그인만 활성화하고, write 권한은 필요한 경우에만 켜세요.
