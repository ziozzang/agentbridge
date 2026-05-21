package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ziozzang/agentbridge/internal/protocol/systemprompt"

	lua "github.com/yuin/gopher-lua"
)

const cliLuaTimeout = 30 * time.Minute

type runLuaParams struct {
	Code  string         `json:"code,omitempty"`
	Path  string         `json:"path,omitempty"`
	Args  []string       `json:"args,omitempty"`
	State map[string]any `json:"state,omitempty"`
}

type runLuaResult struct {
	Output string `json:"output"`
}

type clientToolCallParams struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

func (c *client) callClientTool(ctx context.Context, p clientToolCallParams) (runLuaResult, error) {
	switch strings.TrimSpace(p.Name) {
	case "run_lua":
		rawArgs := p.Args
		var luaArgs []string
		if raw, ok := rawArgs["args"].([]any); ok {
			for _, v := range raw {
				luaArgs = append(luaArgs, fmt.Sprint(v))
			}
		}
		return c.runLua(ctx, runLuaParams{
			Code: stringFromAny(rawArgs["code"]),
			Path: stringFromAny(rawArgs["path"]),
			Args: luaArgs,
		})
	case "run_command":
		return c.runClientShellCommand(ctx, stringFromAny(p.Args["command"]))
	default:
		return runLuaResult{}, fmt.Errorf("unknown client tool: %s", p.Name)
	}
}

func (c *client) runLua(ctx context.Context, p runLuaParams) (runLuaResult, error) {
	code := p.Code
	if strings.TrimSpace(code) == "" {
		if strings.TrimSpace(p.Path) == "" {
			return runLuaResult{}, fmt.Errorf("lua code or path is required")
		}
		path := p.Path
		if !filepath.IsAbs(path) {
			c.mu.Lock()
			cwd := c.state.Cwd
			c.mu.Unlock()
			path = filepath.Join(cwd, path)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return runLuaResult{}, err
		}
		code = string(body)
	}
	runCtx, cancel := context.WithTimeout(ctx, cliLuaTimeout)
	defer cancel()
	db, err := openOrchestrationDB()
	if err != nil {
		return runLuaResult{}, err
	}
	defer db.Close()

	L := lua.NewState(lua.Options{SkipOpenLibs: true})
	defer L.Close()
	for _, open := range []struct {
		name string
		fn   lua.LGFunction
	}{
		{lua.LoadLibName, lua.OpenPackage},
		{lua.BaseLibName, lua.OpenBase},
		{lua.TabLibName, lua.OpenTable},
		{lua.StringLibName, lua.OpenString},
		{lua.MathLibName, lua.OpenMath},
	} {
		L.Push(L.NewFunction(open.fn))
		L.Push(lua.LString(open.name))
		L.Call(1, 0)
	}

	var out strings.Builder
	state := map[string]lua.LValue{}
	for k, v := range p.State {
		state[k] = goValueToLua(L, v)
	}
	cli := L.NewTable()
	L.SetField(cli, "say", L.NewFunction(func(L *lua.LState) int {
		out.WriteString(L.CheckString(1))
		out.WriteByte('\n')
		return 0
	}))
	L.SetField(cli, "status", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LString(c.statusString()))
		return 1
	}))
	L.SetField(cli, "structure", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LString(c.structureString()))
		return 1
	}))
	L.SetField(cli, "prompt", L.NewFunction(func(L *lua.LState) int {
		text := L.CheckString(1)
		c.mu.Lock()
		sessionID := c.state.SessionID
		c.mu.Unlock()
		if err := c.Prompt(runCtx, sessionID, text); err != nil {
			L.RaiseError("%s", err.Error())
			return 0
		}
		return 0
	}))
	L.SetField(cli, "attach", L.NewFunction(func(L *lua.LState) int {
		res, err := c.attachPath(L.CheckString(1))
		if err != nil {
			L.RaiseError("%s", err.Error())
			return 0
		}
		L.Push(lua.LString(res.Path))
		return 1
	}))
	L.SetField(cli, "files", L.NewFunction(func(L *lua.LState) int {
		c.mu.Lock()
		files := append([]attachment(nil), c.files...)
		c.mu.Unlock()
		t := L.NewTable()
		for i, f := range files {
			L.RawSetInt(t, i+1, lua.LString(f.Resource.Path))
		}
		L.Push(t)
		return 1
	}))
	L.SetField(cli, "clear_files", L.NewFunction(func(L *lua.LState) int {
		c.mu.Lock()
		c.files = nil
		c.mu.Unlock()
		return 0
	}))
	L.SetField(cli, "command", L.NewFunction(func(L *lua.LState) int {
		if err := c.runCommand(runCtx, strings.TrimSpace(L.CheckString(1))); err != nil {
			L.RaiseError("%s", err.Error())
			return 0
		}
		return 0
	}))
	L.SetField(cli, "choose", L.NewFunction(func(L *lua.LState) int {
		title := L.CheckString(1)
		detail := ""
		optionsValue := L.Get(2)
		if L.GetTop() >= 3 {
			detail = L.CheckString(3)
		}
		var opts []choiceOption
		if tbl, ok := optionsValue.(*lua.LTable); ok {
			tbl.ForEach(func(k lua.LValue, v lua.LValue) {
				switch vv := v.(type) {
				case *lua.LTable:
					key := vv.RawGetString("key").String()
					label := vv.RawGetString("label").String()
					if label == lua.LNil.String() {
						label = vv.RawGetString("text").String()
					}
					opts = append(opts, choiceOption{Key: key, Label: label})
				default:
					opts = append(opts, choiceOption{Key: k.String(), Label: v.String()})
				}
			})
		}
		choice, err := c.choose(title, detail, opts)
		if err != nil {
			L.RaiseError("%s", err.Error())
			return 0
		}
		L.Push(lua.LString(choice))
		return 1
	}))
	L.SetField(cli, "sleep_ms", L.NewFunction(func(L *lua.LState) int {
		ms := L.CheckInt(1)
		if ms < 0 {
			ms = 0
		}
		timer := time.NewTimer(time.Duration(ms) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-runCtx.Done():
			L.RaiseError("%s", runCtx.Err().Error())
		case <-timer.C:
		}
		return 0
	}))
	L.SetField(cli, "time_unix", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LNumber(time.Now().Unix()))
		return 1
	}))
	L.SetField(cli, "now", L.NewFunction(func(L *lua.LState) int {
		now := time.Now()
		t := L.NewTable()
		L.SetField(t, "unix", lua.LNumber(now.Unix()))
		L.SetField(t, "unix_ms", lua.LNumber(now.UnixMilli()))
		L.SetField(t, "rfc3339", lua.LString(now.Format(time.RFC3339Nano)))
		L.SetField(t, "local", lua.LString(now.Format("2006-01-02 15:04:05 -0700 MST")))
		L.Push(t)
		return 1
	}))
	L.SetField(cli, "snapshot", L.NewFunction(func(L *lua.LState) int {
		L.Push(c.snapshotTable(L))
		return 1
	}))
	L.SetField(cli, "emit", L.NewFunction(func(L *lua.LState) int {
		name := L.CheckString(1)
		payload := ""
		if L.GetTop() >= 2 {
			payload = L.Get(2).String()
		}
		_, _ = db.ExecContext(runCtx, `insert into events(name, payload, created_at) values(?, ?, ?)`, name, payload, time.Now().UTC().Format(time.RFC3339Nano))
		if payload == "" {
			out.WriteString("[event] " + name + "\n")
		} else {
			out.WriteString("[event] " + name + " " + payload + "\n")
		}
		return 0
	}))
	L.SetField(cli, "get", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		if v, ok := state[key]; ok {
			L.Push(v)
		} else {
			L.Push(lua.LNil)
		}
		return 1
	}))
	L.SetField(cli, "set", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		state[key] = L.Get(2)
		return 0
	}))
	L.SetField(cli, "mem_get", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		if v, ok := state[key]; ok {
			L.Push(v)
		} else {
			L.Push(lua.LNil)
		}
		return 1
	}))
	L.SetField(cli, "mem_set", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		state[key] = L.Get(2)
		return 0
	}))
	L.SetField(cli, "mem_delete", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		delete(state, key)
		return 0
	}))
	L.SetField(cli, "mem_list", L.NewFunction(func(L *lua.LState) int {
		t := L.NewTable()
		for k, v := range state {
			L.SetField(t, k, v)
		}
		L.Push(t)
		return 1
	}))
	L.SetField(cli, "kv_get", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		var value string
		err := db.QueryRowContext(runCtx, `select value from kv where key = ?`, key).Scan(&value)
		if err == sql.ErrNoRows {
			L.Push(lua.LNil)
			return 1
		}
		if err != nil {
			L.RaiseError("%s", err.Error())
			return 0
		}
		L.Push(lua.LString(value))
		return 1
	}))
	L.SetField(cli, "kv_set", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		value := L.CheckString(2)
		_, err := db.ExecContext(runCtx, `insert into kv(key, value, updated_at) values(?, ?, ?) on conflict(key) do update set value = excluded.value, updated_at = excluded.updated_at`, key, value, time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			L.RaiseError("%s", err.Error())
		}
		return 0
	}))
	L.SetField(cli, "kv_delete", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		if _, err := db.ExecContext(runCtx, `delete from kv where key = ?`, key); err != nil {
			L.RaiseError("%s", err.Error())
		}
		return 0
	}))
	L.SetField(cli, "kv_list", L.NewFunction(func(L *lua.LState) int {
		prefix := ""
		if L.GetTop() >= 1 {
			prefix = L.CheckString(1)
		}
		rows, err := db.QueryContext(runCtx, `select key, value from kv where key like ? order by key`, prefix+"%")
		if err != nil {
			L.RaiseError("%s", err.Error())
			return 0
		}
		defer rows.Close()
		t := L.NewTable()
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				L.RaiseError("%s", err.Error())
				return 0
			}
			L.SetField(t, key, lua.LString(value))
		}
		L.Push(t)
		return 1
	}))
	L.SetField(cli, "sql_query", L.NewFunction(func(L *lua.LState) int {
		query := L.CheckString(1)
		if !isReadOnlySQL(query) {
			L.RaiseError("sql_query only accepts select, with, explain, or pragma")
			return 0
		}
		out, err := queryRowsLua(L, runCtx, db, query)
		if err != nil {
			L.RaiseError("%s", err.Error())
			return 0
		}
		L.Push(out)
		return 1
	}))
	L.SetField(cli, "sql_exec", L.NewFunction(func(L *lua.LState) int {
		query := L.CheckString(1)
		res, err := db.ExecContext(runCtx, query)
		if err != nil {
			L.RaiseError("%s", err.Error())
			return 0
		}
		n, _ := res.RowsAffected()
		L.Push(lua.LNumber(n))
		return 1
	}))
	L.SetField(cli, "memory_put", L.NewFunction(func(L *lua.LState) int {
		text := L.CheckString(1)
		meta := ""
		if L.GetTop() >= 2 {
			meta = L.Get(2).String()
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		res, err := db.ExecContext(runCtx, `insert into memories(text, metadata, created_at) values(?, ?, ?)`, text, meta, now)
		if err != nil {
			L.RaiseError("%s", err.Error())
			return 0
		}
		id, _ := res.LastInsertId()
		L.Push(lua.LNumber(id))
		return 1
	}))
	L.SetField(cli, "memory_search", L.NewFunction(func(L *lua.LState) int {
		query := L.CheckString(1)
		limit := 10
		if L.GetTop() >= 2 {
			limit = L.CheckInt(2)
		}
		rows, err := db.QueryContext(runCtx, `select id, text, metadata, created_at from memories where text like ? or metadata like ? order by id desc limit ?`, "%"+query+"%", "%"+query+"%", limit)
		if err != nil {
			L.RaiseError("%s", err.Error())
			return 0
		}
		defer rows.Close()
		out := L.NewTable()
		i := 1
		for rows.Next() {
			var id int64
			var text, meta, created string
			if err := rows.Scan(&id, &text, &meta, &created); err != nil {
				L.RaiseError("%s", err.Error())
				return 0
			}
			item := L.NewTable()
			L.SetField(item, "id", lua.LNumber(id))
			L.SetField(item, "text", lua.LString(text))
			L.SetField(item, "metadata", lua.LString(meta))
			L.SetField(item, "created_at", lua.LString(created))
			L.RawSetInt(out, i, item)
			i++
		}
		L.Push(out)
		return 1
	}))
	L.SetGlobal("cli", cli)

	argTable := L.NewTable()
	for i, arg := range p.Args {
		L.RawSetInt(argTable, i+1, lua.LString(arg))
	}
	L.SetGlobal("arg", argTable)

	if err := L.DoString(orchestrationPrelude); err != nil {
		return runLuaResult{}, err
	}
	if err := L.DoString(code); err != nil {
		return runLuaResult{}, err
	}
	return runLuaResult{Output: strings.TrimSpace(out.String())}, nil
}

func openOrchestrationDB() (*sql.DB, error) {
	path := os.Getenv("AGENTBRIDGE_CLI_ORCH_DB")
	if path == "" {
		base := os.Getenv("XDG_STATE_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				home = "."
			}
			base = filepath.Join(home, ".local", "state")
		}
		path = filepath.Join(base, "agentbridge", "acp-agent", "orchestration.sqlite")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		return nil, err
	}
	schema := []string{
		`create table if not exists kv(key text primary key, value text not null, updated_at text not null)`,
		`create table if not exists events(id integer primary key autoincrement, name text not null, payload text, created_at text not null)`,
		`create table if not exists jobs(id text primary key, status text not null, payload text, result text, updated_at text not null)`,
		`create table if not exists memories(id integer primary key autoincrement, text text not null, metadata text, created_at text not null)`,
		`create table if not exists artifacts(id integer primary key autoincrement, kind text not null, uri text, content text, metadata text, created_at text not null)`,
	}
	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return db, nil
}

func isReadOnlySQL(query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	return strings.HasPrefix(q, "select") || strings.HasPrefix(q, "with") || strings.HasPrefix(q, "explain") || strings.HasPrefix(q, "pragma")
}

func queryRowsLua(L *lua.LState, ctx context.Context, db *sql.DB, query string) (*lua.LTable, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := L.NewTable()
	idx := 1
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := L.NewTable()
		for i, col := range cols {
			L.SetField(row, col, lua.LString(sqlValueString(values[i])))
		}
		L.RawSetInt(out, idx, row)
		idx++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func sqlValueString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	case time.Time:
		return x.Format(time.RFC3339Nano)
	default:
		return fmt.Sprint(x)
	}
}

func (c *client) snapshotTable(L *lua.LState) *lua.LTable {
	c.mu.Lock()
	state := c.state
	files := append([]attachment(nil), c.files...)
	opts := c.opts
	c.mu.Unlock()
	t := L.NewTable()
	L.SetField(t, "session_id", lua.LString(state.SessionID))
	L.SetField(t, "cwd", lua.LString(state.Cwd))
	L.SetField(t, "addr", lua.LString(state.Addr))
	L.SetField(t, "model", lua.LString(state.Model))
	L.SetField(t, "mode", lua.LString(state.Mode))
	L.SetField(t, "permission", lua.LString(firstNonEmpty(opts.Permission, "prompt")))
	L.SetField(t, "thinking", lua.LBool(opts.ShowThinking))
	L.SetField(t, "tools", lua.LBool(opts.ShowTools))
	L.SetField(t, "raw", lua.LBool(opts.RawUpdates))
	L.SetField(t, "project_context", lua.LString(systemprompt.ProjectContextPath(state.Cwd)))
	ft := L.NewTable()
	for i, f := range files {
		rt := L.NewTable()
		L.SetField(rt, "path", lua.LString(f.Resource.Path))
		L.SetField(rt, "name", lua.LString(f.Resource.Name))
		L.SetField(rt, "mime", lua.LString(f.Resource.MimeType))
		L.SetField(rt, "chars", lua.LNumber(len(f.Resource.Text)))
		L.SetField(rt, "truncated", lua.LBool(f.Resource.Truncated))
		L.RawSetInt(ft, i+1, rt)
	}
	L.SetField(t, "files", ft)
	return t
}

func goValueToLua(L *lua.LState, v any) lua.LValue {
	switch x := v.(type) {
	case nil:
		return lua.LNil
	case bool:
		return lua.LBool(x)
	case string:
		return lua.LString(x)
	case float64:
		return lua.LNumber(x)
	case int:
		return lua.LNumber(x)
	case []any:
		t := L.NewTable()
		for i, item := range x {
			L.RawSetInt(t, i+1, goValueToLua(L, item))
		}
		return t
	case map[string]any:
		t := L.NewTable()
		for k, item := range x {
			L.SetField(t, k, goValueToLua(L, item))
		}
		return t
	default:
		return lua.LString(fmt.Sprint(v))
	}
}

func stringFromAny(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

const orchestrationPrelude = `
local orch = {}

function orch.plan(items)
  local p = { items = {}, cursor = 1, status = "pending", started_at = cli.time_unix() }
  for i, item in ipairs(items or {}) do
    if type(item) == "table" then
      p.items[i] = {
        id = item.id or tostring(i),
        title = item.title or item.task or tostring(item.id or i),
        task = item.task or item.title or tostring(item.id or i),
        status = item.status or "pending",
        result = item.result,
        attempts = item.attempts or 0,
      }
    else
      p.items[i] = { id = tostring(i), title = tostring(item), task = tostring(item), status = "pending", attempts = 0 }
    end
  end
  return p
end

function orch.next_job(plan)
  for i = plan.cursor or 1, #plan.items do
    local job = plan.items[i]
    if job.status == "pending" or job.status == "retry" then
      plan.cursor = i
      return job
    end
  end
  for i = 1, #(plan.items or {}) do
    local job = plan.items[i]
    if job.status == "pending" or job.status == "retry" then
      plan.cursor = i
      return job
    end
  end
  return nil
end

orch.fetch_next_job = orch.next_job

function orch.done(job, result)
  job.status = "done"
  job.result = result
  job.finished_at = cli.time_unix()
  return job
end

function orch.fail(job, reason)
  job.status = "failed"
  job.error = reason or "failed"
  job.finished_at = cli.time_unix()
  return job
end

function orch.retry(job, reason)
  job.status = "retry"
  job.error = reason
  job.attempts = (job.attempts or 0) + 1
  return job
end

function orch.check_status(plan)
  local counts = { pending = 0, running = 0, done = 0, failed = 0, retry = 0 }
  for _, job in ipairs(plan.items or {}) do
    counts[job.status] = (counts[job.status] or 0) + 1
  end
  if counts.failed > 0 then
    plan.status = "failed"
  elseif counts.pending == 0 and counts.running == 0 and counts.retry == 0 then
    plan.status = "done"
  elseif counts.done > 0 or counts.running > 0 then
    plan.status = "running"
  else
    plan.status = "pending"
  end
  return counts
end

function orch.status_line(plan)
  local c = orch.check_status(plan)
  return string.format("plan=%s pending=%d running=%d retry=%d done=%d failed=%d",
    plan.status, c.pending or 0, c.running or 0, c.retry or 0, c.done or 0, c.failed or 0)
end

function orch.trigger(name, predicate, action)
  return { name = name or "trigger", predicate = predicate, action = action, fired = 0 }
end

function orch.run_triggers(ctx, triggers)
  local fired = {}
  for _, trig in ipairs(triggers or {}) do
    local ok, should_fire = pcall(trig.predicate, ctx)
    if ok and should_fire then
      trig.fired = (trig.fired or 0) + 1
      local action_ok, result = pcall(trig.action, ctx)
      fired[#fired + 1] = { name = trig.name, ok = action_ok, result = result }
      if not action_ok then
        ctx.error = tostring(result)
      end
    elseif not ok then
      ctx.error = tostring(should_fire)
      fired[#fired + 1] = { name = trig.name, ok = false, result = ctx.error }
    end
  end
  return fired
end

function orch.steer(ctx, directive)
  ctx.steering = ctx.steering or {}
  ctx.steering[#ctx.steering + 1] = tostring(directive)
  return directive
end

function orch.run(job, fn)
  job.status = "running"
  job.started_at = cli.time_unix()
  local ok, result = pcall(fn, job)
  if ok then
    return orch.done(job, result)
  end
  return orch.fail(job, tostring(result))
end

function orch.loop(plan, fn, opts)
  opts = opts or {}
  local max_steps = opts.max_steps or 100
  local sleep_ms = opts.sleep_ms or 0
  local steps = 0
  while steps < max_steps do
    local job = orch.next_job(plan)
    if not job then break end
    steps = steps + 1
    orch.run(job, fn)
    orch.check_status(plan)
    if sleep_ms > 0 then cli.sleep_ms(sleep_ms) end
    if opts.stop_when and opts.stop_when(plan, job) then break end
  end
  orch.check_status(plan)
  return plan
end

function orch.control_loop(opts)
  opts = opts or {}
  local plan = opts.plan or orch.plan(opts.jobs or {})
  local ctx = opts.context or { plan = plan, steering = {} }
  ctx.plan = plan
  local max_steps = opts.max_steps or 100
  local steps = 0
  while steps < max_steps do
    ctx.counts = orch.check_status(plan)
    orch.run_triggers(ctx, opts.triggers)
    if ctx.stop == true then break end
    local job = (opts.fetch_next_job or orch.fetch_next_job)(plan, ctx)
    if not job then break end
    ctx.job = job
    steps = steps + 1
    if opts.before_run then opts.before_run(ctx, job) end
    orch.run(job, function(j)
      return opts.run(j, ctx)
    end)
    if opts.after_run then opts.after_run(ctx, job) end
    ctx.counts = orch.check_status(plan)
    orch.run_triggers(ctx, opts.triggers)
    if ctx.stop == true then break end
    if opts.sleep_ms and opts.sleep_ms > 0 then cli.sleep_ms(opts.sleep_ms) end
  end
  ctx.steps = steps
  ctx.counts = orch.check_status(plan)
  return ctx
end

function orch.cron(opts, fn)
  opts = opts or {}
  local interval_ms = opts.interval_ms or 1000
  local max_runs = opts.max_runs or 1
  local runs = 0
  while runs < max_runs do
    runs = runs + 1
    local ok, err = pcall(fn, runs)
    if not ok then return false, tostring(err) end
    if runs < max_runs then cli.sleep_ms(interval_ms) end
  end
  return true, runs
end

function orch.timer(opts)
  opts = opts or {}
  return {
    interval_ms = opts.interval_ms or 1000,
    max_ticks = opts.max_ticks or 1,
    ticks = 0,
    next_at_ms = 0,
  }
end

function orch.tick(timer)
  timer.ticks = (timer.ticks or 0) + 1
  timer.last_tick_unix = cli.time_unix()
  return timer.ticks
end

function orch.with_timeout(opts, fn)
  opts = opts or {}
  local timeout_ms = opts.timeout_ms or 0
  local start = cli.now().unix_ms
  local ok, result = pcall(fn)
  local elapsed = cli.now().unix_ms - start
  if timeout_ms > 0 and elapsed > timeout_ms then
    return false, "timeout", elapsed
  end
  if not ok then
    return false, result, elapsed
  end
  return true, result, elapsed
end

local function maybe_run(prompt, opts)
  opts = opts or {}
  if opts.store_key then cli.kv_set(opts.store_key, prompt) end
  if opts.memory then cli.memory_put(prompt, opts.memory) end
  if opts.run then
    cli.prompt(prompt)
    return prompt
  end
  return prompt
end

function orch.ask(prompt, opts)
  return maybe_run(prompt, opts)
end

function orch.reflect(ctx, opts)
  opts = opts or {}
  local body = "Reflect on the current orchestration state.\n"
  body = body .. "Return concise sections: summary, risks, next_action, stop_condition.\n\n"
  body = body .. "<status>\n" .. (ctx.status or "") .. "\n</status>\n"
  body = body .. "<steering>\n" .. table.concat(ctx.steering or {}, "\n") .. "\n</steering>\n"
  if ctx.plan then body = body .. "<plan_status>\n" .. orch.status_line(ctx.plan) .. "\n</plan_status>\n" end
  return maybe_run(body, opts)
end

function orch.judge(goal, evidence, opts)
  opts = opts or {}
  local body = "Judge whether the evidence satisfies the goal.\n"
  body = body .. "Return JSON with keys: pass, confidence, missing, evidence, next_action.\n\n"
  body = body .. "<goal>\n" .. tostring(goal or "") .. "\n</goal>\n"
  body = body .. "<evidence>\n" .. tostring(evidence or "") .. "\n</evidence>\n"
  return maybe_run(body, opts)
end

function orch.extract(text, schema, opts)
  opts = opts or {}
  local body = "Extract structured information from the text.\n"
  body = body .. "Return only JSON matching this schema or description:\n" .. tostring(schema or "{}") .. "\n\n"
  body = body .. "<text>\n" .. tostring(text or "") .. "\n</text>\n"
  return maybe_run(body, opts)
end

function orch.rank(candidates, criteria, opts)
  opts = opts or {}
  local body = "Rank the candidates by the criteria. Return JSON array with rank, candidate, reason, confidence.\n\n"
  body = body .. "<criteria>\n" .. tostring(criteria or "") .. "\n</criteria>\n<candidates>\n"
  for i, c in ipairs(candidates or {}) do body = body .. tostring(i) .. ". " .. tostring(c) .. "\n" end
  body = body .. "</candidates>\n"
  return maybe_run(body, opts)
end

function orch.summarize(text, opts)
  opts = opts or {}
  local body = "Summarize the following for future agent memory. Preserve decisions, constraints, facts, open questions, and next actions.\n\n"
  body = body .. "<text>\n" .. tostring(text or "") .. "\n</text>\n"
  return maybe_run(body, opts)
end

function orch.critic(plan_or_answer, opts)
  opts = opts or {}
  local body = "Critique the following plan or answer. Focus on bugs, missing evidence, weak assumptions, and verification gaps.\n\n"
  body = body .. "<candidate>\n" .. tostring(plan_or_answer or "") .. "\n</candidate>\n"
  return maybe_run(body, opts)
end

function orch.research_source(topic, opts)
  opts = opts or {}
  local body = "Research this topic. Identify likely sources, search queries, local files to inspect, and evidence needed.\n"
  body = body .. "Return a compact research plan with source priorities.\n\n<topic>\n" .. tostring(topic or "") .. "\n</topic>\n"
  return maybe_run(body, opts)
end

function orch.delegate(task, opts)
  opts = opts or {}
  local cmd = "/subagent "
  if opts.model then cmd = cmd .. "--model " .. tostring(opts.model) .. " " end
  cmd = cmd .. tostring(task or "")
  if opts.run == false then return cmd end
  cli.command(cmd)
  return cmd
end

cli.llm = {
  ask = orch.ask,
  reflect = orch.reflect,
  judge = orch.judge,
  extract = orch.extract,
  rank = orch.rank,
  summarize = orch.summarize,
  critic = orch.critic,
}

cli.data = {
  attach = cli.attach,
  files = cli.files,
  clear_files = cli.clear_files,
  extract = orch.extract,
  rank = orch.rank,
  research_source = orch.research_source,
}

cli.memory = {
  get = cli.mem_get,
  set = cli.mem_set,
  delete = cli.mem_delete,
  list = cli.mem_list,
  kv_get = cli.kv_get,
  kv_set = cli.kv_set,
  kv_delete = cli.kv_delete,
  kv_list = cli.kv_list,
  put = cli.memory_put,
  search = cli.memory_search,
}

cli.maint = {
  status = cli.status,
  structure = cli.structure,
  snapshot = cli.snapshot,
  context = function() return cli.command("/context") end,
  compact = function(target)
    if target then return cli.command("/compact " .. tostring(target)) end
    return cli.command("/compact")
  end,
  save = function(name) return cli.command("/save " .. tostring(name)) end,
}

cli.util = {
  now = cli.now,
  time_unix = cli.time_unix,
  sleep_ms = cli.sleep_ms,
  emit = cli.emit,
  sql_query = cli.sql_query,
  sql_exec = cli.sql_exec,
}

cli.orch = orch
`
