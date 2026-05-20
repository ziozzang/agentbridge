# 플러그인

플러그인은 AgentBridge에 선택적 tool definition을 추가합니다.

```bash
AGENTBRIDGE_PLUGINS=sqlite,duckdb agentbridge
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

## 보안

플러그인은 AgentBridge 프로세스 안에서 같은 권한으로 실행됩니다. 신뢰할 수
있는 플러그인만 활성화하고, write 권한은 필요한 경우에만 켜세요.
