// Package sqliteplugin exposes a catalogue-style SQLite plugin to the ACP
// harness. It scans one or more directories for `*.db`, `*.sqlite`, and
// `*.sqlite3` files, lets the model list/load/unload them, then inspects
// schemas and runs read-only queries.
//
// Activation:    add `sqlite` to ACP_HARNESS_PLUGINS.
// Catalog dirs:  ACP_HARNESS_SQLITE_DIRS (comma-separated). Defaults to
//                $XDG_DATA_HOME/acp-harness/sqlite or
//                ~/.local/share/acp-harness/sqlite.
// Read-only by default. Set ACP_HARNESS_SQLITE_RW=1 to allow INSERT/UPDATE/
// DELETE/CREATE/DROP statements via sqlite_exec.
//
// Tools exposed via plugin__sqlite__<name>:
//   - sqlite_list:    {} -> catalog listing
//   - sqlite_load:    {file: string} -> open a handle; returns tables
//   - sqlite_unload:  {file: string} -> close the handle
//   - sqlite_tables:  {file: string} -> table list
//   - sqlite_schema:  {file: string, table: string} -> CREATE TABLE DDL
//   - sqlite_query:   {file: string, sql: string, limit?: int} -> rows
//   - sqlite_exec:    {file: string, sql: string} -> rowsAffected (RW only)
package sqliteplugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	_ "modernc.org/sqlite"

	"github.com/ziozzang/glm-acp/internal/logger"
	"github.com/ziozzang/glm-acp/internal/plugins"
)

// Name is the plugin identifier and the segment used in ACP_HARNESS_PLUGINS.
const Name = "sqlite"

func init() {
	plugins.Register(Name, func() plugins.Plugin {
		return New(defaultCatalogDirs(), envIsTrue("ACP_HARNESS_SQLITE_RW"))
	})
}

// Plugin is the concrete SQLite plugin.
type Plugin struct {
	dirs       []string
	readOnly   bool
	mu         sync.Mutex
	openHandles map[string]*sql.DB
}

// New returns a new Plugin instance. catalogDirs may be empty, in which
// case sqlite_load still works for absolute paths under those dirs.
func New(catalogDirs []string, readWrite bool) *Plugin {
	return &Plugin{
		dirs:        catalogDirs,
		readOnly:    !readWrite,
		openHandles: map[string]*sql.DB{},
	}
}

// Name implements plugins.Plugin.
func (p *Plugin) Name() string { return Name }

// Tools implements plugins.Plugin.
func (p *Plugin) Tools() []plugins.ToolDef {
	return []plugins.ToolDef{
		{
			Name:        "sqlite_list",
			Description: "List all SQLite database files in the configured catalog directories.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		},
		{
			Name:        "sqlite_load",
			Description: "Open a SQLite database file by catalog name (basename) or absolute path. Returns the list of tables.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"file":{"type":"string"}},"required":["file"]}`),
		},
		{
			Name:        "sqlite_unload",
			Description: "Close a previously loaded SQLite database file.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"file":{"type":"string"}},"required":["file"]}`),
		},
		{
			Name:        "sqlite_tables",
			Description: "Return the list of tables in a loaded SQLite database.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"file":{"type":"string"}},"required":["file"]}`),
		},
		{
			Name:        "sqlite_schema",
			Description: "Return the CREATE TABLE DDL for a given table in a loaded SQLite database.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"file":{"type":"string"},"table":{"type":"string"}},"required":["file","table"]}`),
		},
		{
			Name:        "sqlite_query",
			Description: "Run a SELECT query against a loaded SQLite database. Only read-only SELECT/EXPLAIN/PRAGMA statements are accepted.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"file":{"type":"string"},"sql":{"type":"string"},"limit":{"type":"integer"}},"required":["file","sql"]}`),
		},
		{
			Name:        "sqlite_exec",
			Description: "Execute an INSERT/UPDATE/DELETE statement. Disabled by default; requires ACP_HARNESS_SQLITE_RW=1.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"file":{"type":"string"},"sql":{"type":"string"}},"required":["file","sql"]}`),
		},
	}
}

// Call implements plugins.Plugin.
func (p *Plugin) Call(ctx context.Context, tool string, args json.RawMessage) (string, error) {
	switch tool {
	case "sqlite_list":
		return p.handleList(ctx)
	case "sqlite_load":
		return p.handleLoad(ctx, args)
	case "sqlite_unload":
		return p.handleUnload(ctx, args)
	case "sqlite_tables":
		return p.handleTables(ctx, args)
	case "sqlite_schema":
		return p.handleSchema(ctx, args)
	case "sqlite_query":
		return p.handleQuery(ctx, args)
	case "sqlite_exec":
		return p.handleExec(ctx, args)
	default:
		return "", fmt.Errorf("sqlite: unknown tool %q", tool)
	}
}

// CatalogEntry is one file in the SQLite catalog.
type CatalogEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Catalog returns the merged list of SQLite files under the configured dirs.
// Exported for tests.
func (p *Plugin) Catalog() ([]CatalogEntry, error) {
	var out []CatalogEntry
	seen := map[string]bool{}
	for _, dir := range p.dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			low := strings.ToLower(e.Name())
			if !(strings.HasSuffix(low, ".db") || strings.HasSuffix(low, ".sqlite") || strings.HasSuffix(low, ".sqlite3")) {
				continue
			}
			full := filepath.Join(dir, e.Name())
			if seen[full] {
				continue
			}
			seen[full] = true
			info, _ := e.Info()
			var size int64
			if info != nil {
				size = info.Size()
			}
			out = append(out, CatalogEntry{Name: e.Name(), Path: full, Size: size})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (p *Plugin) handleList(_ context.Context) (string, error) {
	entries, err := p.Catalog()
	if err != nil {
		return "", err
	}
	return jsonOut(map[string]any{"dirs": p.dirs, "files": entries})
}

// resolvePath maps a tool's "file" arg to an absolute path inside the
// catalog. Bare basenames are searched across the configured dirs;
// otherwise the input is taken as-is.
func (p *Plugin) resolvePath(name string) (string, error) {
	if name == "" {
		return "", errors.New("sqlite: 'file' is required")
	}
	if filepath.IsAbs(name) {
		return name, nil
	}
	// Try each catalog dir.
	for _, dir := range p.dirs {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	// Relative path that does not exist under any catalog dir — return
	// the absolute path against CWD so the error message is informative.
	abs, _ := filepath.Abs(name)
	return abs, nil
}

func (p *Plugin) open(path string) (*sql.DB, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if db, ok := p.openHandles[path]; ok {
		return db, nil
	}
	uri := "file:" + path + "?mode=rw"
	if p.readOnly {
		uri = "file:" + path + "?mode=ro&_pragma=query_only(true)"
	}
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	p.openHandles[path] = db
	logger.Debugf("sqlite plugin opened %s (readOnly=%v)", path, p.readOnly)
	return db, nil
}

func (p *Plugin) closeHandle(path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	db, ok := p.openHandles[path]
	if !ok {
		return nil
	}
	delete(p.openHandles, path)
	return db.Close()
}

type fileArg struct {
	File  string `json:"file"`
	Table string `json:"table"`
	SQL   string `json:"sql"`
	Limit int    `json:"limit"`
}

func (p *Plugin) handleLoad(ctx context.Context, args json.RawMessage) (string, error) {
	var a fileArg
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	path, err := p.resolvePath(a.File)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("sqlite: file not found: %s", path)
	}
	db, err := p.open(path)
	if err != nil {
		return "", err
	}
	tables, err := tableList(ctx, db)
	if err != nil {
		return "", err
	}
	return jsonOut(map[string]any{"path": path, "tables": tables})
}

func (p *Plugin) handleUnload(_ context.Context, args json.RawMessage) (string, error) {
	var a fileArg
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	path, err := p.resolvePath(a.File)
	if err != nil {
		return "", err
	}
	if err := p.closeHandle(path); err != nil {
		return "", err
	}
	return jsonOut(map[string]any{"unloaded": path})
}

func (p *Plugin) handleTables(ctx context.Context, args json.RawMessage) (string, error) {
	var a fileArg
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	path, err := p.resolvePath(a.File)
	if err != nil {
		return "", err
	}
	db, err := p.open(path)
	if err != nil {
		return "", err
	}
	tables, err := tableList(ctx, db)
	if err != nil {
		return "", err
	}
	return jsonOut(map[string]any{"tables": tables})
}

func (p *Plugin) handleSchema(ctx context.Context, args json.RawMessage) (string, error) {
	var a fileArg
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.Table == "" {
		return "", errors.New("sqlite: 'table' is required")
	}
	path, err := p.resolvePath(a.File)
	if err != nil {
		return "", err
	}
	db, err := p.open(path)
	if err != nil {
		return "", err
	}
	var ddl sql.NullString
	row := db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name=?", a.Table)
	if err := row.Scan(&ddl); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("sqlite: table %q not found", a.Table)
		}
		return "", err
	}
	// Column info via PRAGMA table_info.
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%q)", a.Table))
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type col struct {
		Cid     int            `json:"cid"`
		Name    string         `json:"name"`
		Type    string         `json:"type"`
		NotNull int            `json:"notnull"`
		Default sql.NullString `json:"dflt_value"`
		PK      int            `json:"pk"`
	}
	var cols []col
	for rows.Next() {
		var c col
		if err := rows.Scan(&c.Cid, &c.Name, &c.Type, &c.NotNull, &c.Default, &c.PK); err != nil {
			return "", err
		}
		cols = append(cols, c)
	}
	return jsonOut(map[string]any{"ddl": ddl.String, "columns": cols})
}

const defaultQueryLimit = 200

func (p *Plugin) handleQuery(ctx context.Context, args json.RawMessage) (string, error) {
	var a fileArg
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.SQL == "" {
		return "", errors.New("sqlite: 'sql' is required")
	}
	if !isReadOnly(a.SQL) {
		return "", errors.New("sqlite_query: only SELECT/EXPLAIN/PRAGMA statements are accepted (use sqlite_exec for writes)")
	}
	limit := a.Limit
	if limit <= 0 || limit > 10000 {
		limit = defaultQueryLimit
	}
	path, err := p.resolvePath(a.File)
	if err != nil {
		return "", err
	}
	db, err := p.open(path)
	if err != nil {
		return "", err
	}
	rows, err := db.QueryContext(ctx, a.SQL)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	result := make([]map[string]any, 0, 32)
	for rows.Next() {
		if len(result) >= limit {
			break
		}
		dest := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range dest {
			ptrs[i] = &dest[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return "", err
		}
		row := map[string]any{}
		for i, c := range cols {
			row[c] = normalizeSQLValue(dest[i])
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return jsonOut(map[string]any{"columns": cols, "rows": result, "truncated": len(result) == limit})
}

func (p *Plugin) handleExec(ctx context.Context, args json.RawMessage) (string, error) {
	if p.readOnly {
		return "", errors.New("sqlite_exec is disabled (read-only mode); set ACP_HARNESS_SQLITE_RW=1 to enable")
	}
	var a fileArg
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.SQL == "" {
		return "", errors.New("sqlite: 'sql' is required")
	}
	path, err := p.resolvePath(a.File)
	if err != nil {
		return "", err
	}
	db, err := p.open(path)
	if err != nil {
		return "", err
	}
	res, err := db.ExecContext(ctx, a.SQL)
	if err != nil {
		return "", err
	}
	affected, _ := res.RowsAffected()
	lastID, _ := res.LastInsertId()
	return jsonOut(map[string]any{"rows_affected": affected, "last_insert_id": lastID})
}

// isReadOnly performs a coarse statement-type check sufficient to gate
// sqlite_query. The plugin also opens the database in `mode=ro` when
// running in read-only mode, so this is defence-in-depth.
func isReadOnly(stmt string) bool {
	s := strings.TrimSpace(stmt)
	if s == "" {
		return false
	}
	// Strip a leading parenthesis if it's a `(SELECT …)` form.
	for strings.HasPrefix(s, "(") {
		s = strings.TrimSpace(s[1:])
	}
	upper := strings.ToUpper(s)
	for _, prefix := range []string{"SELECT ", "WITH ", "EXPLAIN ", "PRAGMA "} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	// A bare keyword (no trailing space) – e.g. "SELECT 1".
	for _, prefix := range []string{"SELECT", "WITH", "EXPLAIN", "PRAGMA"} {
		if upper == prefix {
			return true
		}
	}
	return false
}

func tableList(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func defaultCatalogDirs() []string {
	if v := os.Getenv("ACP_HARNESS_SQLITE_DIRS"); v != "" {
		var out []string
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	if h := os.Getenv("XDG_DATA_HOME"); h != "" {
		return []string{filepath.Join(h, "acp-harness", "sqlite")}
	}
	if h, err := os.UserHomeDir(); err == nil {
		return []string{filepath.Join(h, ".local", "share", "acp-harness", "sqlite")}
	}
	return nil
}

func envIsTrue(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// normalizeSQLValue converts driver-specific scan values into JSON-safe
// types. modernc.org/sqlite returns []byte for TEXT/BLOB; we surface TEXT
// as string and BLOB as base64-equivalent string.
func normalizeSQLValue(v any) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	default:
		return v
	}
}

func jsonOut(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
