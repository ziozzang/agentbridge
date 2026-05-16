package sqliteplugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func seedDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, age INTEGER);
		INSERT INTO users (name, age) VALUES ('alice', 30), ('bob', 28);
	`); err != nil {
		t.Fatal(err)
	}
}

func TestSqlitePluginEndToEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	seedDB(t, path)

	p := New([]string{dir}, false /*read-only*/)

	// sqlite_list
	out, err := p.Call(context.Background(), "sqlite_list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "test.db") {
		t.Errorf("list missing file: %s", out)
	}

	// sqlite_load (catalog name)
	out, err = p.Call(context.Background(), "sqlite_load", json.RawMessage(`{"file":"test.db"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"users"`) {
		t.Errorf("load missing users table: %s", out)
	}

	// sqlite_schema
	out, err = p.Call(context.Background(), "sqlite_schema", json.RawMessage(`{"file":"test.db","table":"users"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "CREATE TABLE") {
		t.Errorf("schema missing DDL: %s", out)
	}

	// sqlite_query
	out, err = p.Call(context.Background(), "sqlite_query",
		json.RawMessage(`{"file":"test.db","sql":"SELECT name, age FROM users ORDER BY age"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "bob") || !strings.Contains(out, "alice") {
		t.Errorf("query missing rows: %s", out)
	}

	// sqlite_query rejects non-SELECT
	_, err = p.Call(context.Background(), "sqlite_query",
		json.RawMessage(`{"file":"test.db","sql":"INSERT INTO users(name,age) VALUES('x',1)"}`))
	if err == nil {
		t.Errorf("expected sqlite_query to reject non-SELECT")
	}

	// sqlite_exec disabled in read-only
	_, err = p.Call(context.Background(), "sqlite_exec",
		json.RawMessage(`{"file":"test.db","sql":"INSERT INTO users(name,age) VALUES('x',1)"}`))
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("expected sqlite_exec to be disabled, got %v", err)
	}

	// sqlite_tables
	out, err = p.Call(context.Background(), "sqlite_tables", json.RawMessage(`{"file":"test.db"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "users") {
		t.Errorf("tables missing users: %s", out)
	}

	// sqlite_unload
	if _, err := p.Call(context.Background(), "sqlite_unload", json.RawMessage(`{"file":"test.db"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestSqliteExecEnabledInRW(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rw.db")
	seedDB(t, path)
	p := New([]string{dir}, true /*read-write*/)

	out, err := p.Call(context.Background(), "sqlite_exec",
		json.RawMessage(`{"file":"rw.db","sql":"UPDATE users SET age = age + 1"}`))
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.Contains(out, `"rows_affected":2`) {
		t.Errorf("rows_affected missing: %s", out)
	}
}

func TestSqliteMissingFile(t *testing.T) {
	p := New([]string{os.TempDir()}, false)
	_, err := p.Call(context.Background(), "sqlite_load", json.RawMessage(`{"file":"nope.db"}`))
	if err == nil {
		t.Errorf("expected error for missing file")
	}
}

func TestIsReadOnly(t *testing.T) {
	for _, c := range []struct {
		stmt string
		want bool
	}{
		{"SELECT * FROM t", true},
		{"select 1", true},
		{"  with t as (select 1) select * from t ", true},
		{"EXPLAIN SELECT 1", true},
		{"PRAGMA table_info(t)", true},
		{"INSERT INTO t VALUES (1)", false},
		{"DELETE FROM t", false},
		{"UPDATE t SET x=1", false},
		{"DROP TABLE t", false},
		{"", false},
	} {
		if got := isReadOnly(c.stmt); got != c.want {
			t.Errorf("isReadOnly(%q) = %v want %v", c.stmt, got, c.want)
		}
	}
}
