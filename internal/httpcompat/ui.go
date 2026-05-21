package httpcompat

import (
	"net/http"

	"github.com/ziozzang/agentbridge/internal/observability"
)

func (h *handler) providersStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, observability.SnapshotState())
}

func (h *handler) uiStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, observability.SnapshotState())
}

func (h *handler) ui(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/ui" && r.URL.Path != "/ui/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(uiHTML))
}

const uiHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>AgentBridge Status</title>
  <style>
    :root { color-scheme: dark; }
    body { margin: 0; font: 14px/1.5 system-ui, sans-serif; background: #0b1020; color: #dbe3f0; }
    main { max-width: 1120px; margin: 0 auto; padding: 24px; }
    h1,h2 { margin: 0 0 12px; font-weight: 600; }
    .grid { display: grid; gap: 16px; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); }
    .panel { background: #121a2b; border: 1px solid #22304a; border-radius: 8px; padding: 16px; }
    .metric { font-size: 28px; font-weight: 700; }
    .muted { color: #93a4bf; font-size: 12px; }
    table { width: 100%; border-collapse: collapse; }
    th, td { text-align: left; padding: 8px 0; border-bottom: 1px solid #22304a; vertical-align: top; }
    code { font-family: ui-monospace, monospace; font-size: 12px; color: #c7d2fe; }
    .pill { display: inline-block; padding: 2px 8px; border-radius: 999px; background: #1d4ed8; font-size: 12px; }
  </style>
</head>
<body>
  <main>
    <h1>AgentBridge Status</h1>
    <div class="muted" id="timestamp"></div>
    <div class="grid" style="margin-top:16px">
      <section class="panel">
        <h2>Provider</h2>
        <div id="provider"></div>
      </section>
      <section class="panel">
        <h2>HTTP</h2>
        <div class="metric" id="httpCompleted">0</div>
        <div class="muted">completed requests</div>
        <div style="margin-top:8px"><span class="pill" id="httpFailed">0 failed</span></div>
      </section>
      <section class="panel">
        <h2>ACP Sessions</h2>
        <div class="metric" id="sessionCount">0</div>
        <div class="muted">active in-memory sessions</div>
      </section>
      <section class="panel">
        <h2>Inflight HTTP</h2>
        <div class="metric" id="requestCount">0</div>
        <div class="muted">active requests</div>
      </section>
    </div>
    <div class="grid" style="margin-top:16px">
      <section class="panel">
        <h2>Active Requests</h2>
        <table id="requests"></table>
      </section>
      <section class="panel">
        <h2>Active Sessions</h2>
        <table id="sessions"></table>
      </section>
    </div>
  </main>
  <script>
    function esc(value) {
      return String(value ?? "").replace(/[&<>"]/g, (ch) => ({ "&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;" }[ch]));
    }
    function providerHTML(p) {
      if (!p || !p.kind) return '<div class="muted">Provider has not been initialized yet.</div>';
      return [
        '<div><strong>' + esc(p.name || p.kind) + '</strong></div>',
        '<div class="muted">kind: <code>' + esc(p.kind) + '</code></div>',
        '<div class="muted">model: <code>' + esc(p.model) + '</code></div>',
        '<div class="muted">base: <code>' + esc(p.base_url || '-') + '</code></div>',
        '<div class="muted">agent loop: <code>' + esc(p.native_agent ? 'provider_native' : 'agentbridge_builtin') + '</code></div>'
      ].join('');
    }
    function renderRows(target, rows, columns, emptyText) {
      if (!rows.length) {
        target.innerHTML = '<tr><td class="muted">' + esc(emptyText) + '</td></tr>';
        return;
      }
      const head = '<tr>' + columns.map((col) => '<th>' + esc(col.label) + '</th>').join('') + '</tr>';
      const body = rows.map((row) => '<tr>' + columns.map((col) => '<td>' + (col.render ? col.render(row[col.key], row) : esc(row[col.key])) + '</td>').join('') + '</tr>').join('');
      target.innerHTML = head + body;
    }
    async function refresh() {
      const res = await fetch('/ui/status', { cache: 'no-store' });
      const data = await res.json();
      document.getElementById('timestamp').textContent = 'updated ' + data.now;
      document.getElementById('provider').innerHTML = providerHTML(data.provider);
      document.getElementById('httpCompleted').textContent = String(data.completed_http_requests || 0);
      document.getElementById('httpFailed').textContent = String(data.failed_http_requests || 0) + ' failed';
      document.getElementById('sessionCount').textContent = String((data.active_sessions || []).length);
      document.getElementById('requestCount').textContent = String((data.active_requests || []).length);
      renderRows(document.getElementById('requests'), data.active_requests || [], [
        { key: 'method', label: 'Method', render: (v) => '<code>' + esc(v) + '</code>' },
        { key: 'path', label: 'Path', render: (v) => '<code>' + esc(v) + '</code>' },
        { key: 'duration_ms', label: 'Age', render: (v) => esc(v) + ' ms' }
      ], 'No active HTTP requests.');
      renderRows(document.getElementById('sessions'), data.active_sessions || [], [
        { key: 'session_id', label: 'Session', render: (v) => '<code>' + esc(v) + '</code>' },
        { key: 'model', label: 'Model', render: (v) => '<code>' + esc(v || '-') + '</code>' },
        { key: 'mode', label: 'Mode', render: (v, row) => '<code>' + esc(row.native_agent ? 'provider_native' : (v || '-')) + '</code>' },
        { key: 'message_count', label: 'Msgs' }
      ], 'No active ACP sessions.');
    }
    refresh();
    setInterval(refresh, 2000);
  </script>
</body>
</html>`
