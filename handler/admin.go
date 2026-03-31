package handler

import (
	"llm-gateway/registry"
	"net/http"
)

// AdminHandler serves a simple web-based management panel.
//
// Design choice: embedded HTML instead of a separate frontend build.
// This keeps the project as a single binary with zero external dependencies,
// which is important for a demo/interview project. The panel auto-refreshes
// every 3 seconds to show live connection counts and status changes.
type AdminHandler struct {
	Registry *registry.Registry
}

func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(adminHTML))
}

const adminHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>LLM Gateway — Admin Panel</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; background: #0f172a; color: #e2e8f0; padding: 24px; }
  h1 { font-size: 24px; margin-bottom: 8px; }
  .subtitle { color: #94a3b8; margin-bottom: 24px; font-size: 14px; }
  .stats { display: flex; gap: 16px; margin-bottom: 24px; flex-wrap: wrap; }
  .stat-card { background: #1e293b; border-radius: 12px; padding: 16px 24px; min-width: 140px; }
  .stat-card .label { color: #94a3b8; font-size: 12px; text-transform: uppercase; }
  .stat-card .value { font-size: 28px; font-weight: 700; margin-top: 4px; }
  table { width: 100%; border-collapse: collapse; background: #1e293b; border-radius: 12px; overflow: hidden; }
  th { background: #334155; text-align: left; padding: 12px 16px; font-size: 13px; color: #94a3b8; text-transform: uppercase; }
  td { padding: 12px 16px; border-top: 1px solid #334155; font-size: 14px; }
  .badge { display: inline-block; padding: 2px 10px; border-radius: 99px; font-size: 12px; font-weight: 600; }
  .badge-ready { background: #065f46; color: #6ee7b7; }
  .badge-updating { background: #92400e; color: #fcd34d; }
  .badge-deleted { background: #7f1d1d; color: #fca5a5; }
  .badge-loading { background: #1e3a5f; color: #93c5fd; }
  .badge-unloaded { background: #374151; color: #9ca3af; }
  .badge-shadow { background: #4c1d95; color: #c4b5fd; margin-left: 6px; }
  .active { color: #38bdf8; font-weight: 600; }
  .refresh { color: #64748b; font-size: 12px; margin-top: 16px; }
  .empty { text-align: center; padding: 48px; color: #64748b; }
</style>
</head>
<body>
<h1>🚀 LLM Gateway</h1>
<p class="subtitle">Model Management Panel — Auto-refreshes every 3s</p>

<div class="stats">
  <div class="stat-card"><div class="label">Models</div><div class="value" id="modelCount">-</div></div>
  <div class="stat-card"><div class="label">Versions</div><div class="value" id="versionCount">-</div></div>
  <div class="stat-card"><div class="label">Active Conns</div><div class="value active" id="activeCount">-</div></div>
</div>

<table>
  <thead>
    <tr>
      <th>Model</th><th>Version</th><th>Backend</th><th>Status</th>
      <th>Weight</th><th>Active / Max</th><th>Last Used</th>
    </tr>
  </thead>
  <tbody id="tbody"><tr><td colspan="7" class="empty">Loading...</td></tr></tbody>
</table>
<p class="refresh" id="lastRefresh"></p>

<script>
async function refresh() {
  try {
    const res = await fetch('/models');
    const models = await res.json();
    const tbody = document.getElementById('tbody');
    let html = '';
    let totalVersions = 0, totalActive = 0;

    if (!models || models.length === 0) {
      html = '<tr><td colspan="7" class="empty">No models registered. Use POST /models to register one.</td></tr>';
    } else {
      for (const m of models) {
        if (!m.versions) continue;
        for (const v of m.versions) {
          totalVersions++;
          totalActive += v.active_count || 0;
          const statusClass = 'badge-' + v.status;
          const shadow = v.shadow ? '<span class="badge badge-shadow">SHADOW</span>' : '';
          const maxC = v.max_concurrent > 0 ? v.max_concurrent : '∞';
          const lastUsed = new Date(v.last_used_at).toLocaleTimeString();
          html += '<tr>' +
            '<td>' + m.name + '</td>' +
            '<td>' + v.version + '</td>' +
            '<td>' + v.backend_type + '</td>' +
            '<td><span class="badge ' + statusClass + '">' + v.status.toUpperCase() + '</span>' + shadow + '</td>' +
            '<td>' + v.weight + '</td>' +
            '<td><span class="active">' + (v.active_count||0) + '</span> / ' + maxC + '</td>' +
            '<td>' + lastUsed + '</td>' +
          '</tr>';
        }
      }
    }

    tbody.innerHTML = html;
    document.getElementById('modelCount').textContent = models ? models.length : 0;
    document.getElementById('versionCount').textContent = totalVersions;
    document.getElementById('activeCount').textContent = totalActive;
    document.getElementById('lastRefresh').textContent = 'Last refresh: ' + new Date().toLocaleTimeString();
  } catch (e) {
    document.getElementById('tbody').innerHTML = '<tr><td colspan="7" class="empty">Error: ' + e.message + '</td></tr>';
  }
}
refresh();
setInterval(refresh, 3000);
</script>
</body>
</html>`
