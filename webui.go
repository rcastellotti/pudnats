package main

import "net/http"

func (a *App) handleUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(uiHTML))
}

const uiHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>Team Dev Log</title>
  <style>
    :root { --bg:#f5f1e8; --card:#fffdf8; --ink:#1f1c17; --muted:#6d665d; --line:#d8cfbf; --acc:#0b7285; }
    * { box-sizing: border-box; }
    body { margin:0; font-family: ui-sans-serif,system-ui,-apple-system,Segoe UI,Roboto,Arial,sans-serif; color:var(--ink); background: radial-gradient(1200px 500px at 10% -20%, #efe3cb 20%, transparent 70%), var(--bg); }
    .wrap { max-width: 860px; margin: 24px auto; padding: 16px; }
    .card { background: var(--card); border:1px solid var(--line); border-radius: 12px; padding: 14px; margin-bottom: 14px; box-shadow: 0 8px 20px rgba(0,0,0,.05); }
    h1 { margin: 0 0 8px; font-size: 28px; }
    h2 { margin: 0 0 10px; font-size: 18px; }
    input, textarea, button { width:100%; border:1px solid var(--line); border-radius:10px; padding:10px; background:#fff; font:inherit; }
    textarea { min-height: 120px; resize: vertical; }
    button { cursor:pointer; background:var(--acc); color:#fff; font-weight:600; border:none; }
    button.secondary { background:#fff; color:var(--ink); border:1px solid var(--line); }
    .row { display:grid; gap:8px; grid-template-columns: 1fr auto; }
    .entry { border-top:1px dashed var(--line); padding:10px 0; }
    .meta { color:var(--muted); font-size:12px; margin-bottom:6px; }
    .status { font-size: 13px; color: var(--muted); min-height: 1.2em; }
    @media (max-width:640px){ .row{ grid-template-columns:1fr; } }
  </style>
</head>
<body>
  <div class="wrap">
    <div class="card">
      <h1>Team Dev Log</h1>
      <div class="status" id="status">Ready</div>
    </div>

    <div class="card">
      <h2>Auth Token</h2>
      <div class="row">
        <input id="token" placeholder="Paste token from admin CLI" />
        <button class="secondary" id="saveToken">Save</button>
      </div>
    </div>

    <div class="card">
      <h2>Write Entry</h2>
      <textarea id="content" placeholder="What did you build, debug, or learn today?"></textarea>
      <div style="height:8px"></div>
      <button id="postEntry">Post Entry</button>
    </div>

    <div class="card">
      <h2>Query Entries</h2>
      <div class="row">
        <input id="day" type="date" />
        <button class="secondary" id="loadEntries">Load</button>
      </div>
      <div id="entries"></div>
    </div>
  </div>

  <script>
    const api = 'http://localhost:9173';
    const tokenEl = document.getElementById('token');
    const statusEl = document.getElementById('status');
    const entriesEl = document.getElementById('entries');
    const dayEl = document.getElementById('day');
    dayEl.value = new Date().toISOString().slice(0, 10);

    const saved = localStorage.getItem('devlog_token') || '';
    tokenEl.value = saved;

    function setStatus(v){ statusEl.textContent = v; }
    function getToken(){ return (tokenEl.value || '').trim(); }

    function headers() {
      return {
        'Content-Type': 'application/json',
        'Authorization': 'Bearer ' + getToken()
      };
    }

    document.getElementById('saveToken').onclick = () => {
      localStorage.setItem('devlog_token', getToken());
      setStatus('Token saved in localStorage');
    };

    document.getElementById('postEntry').onclick = async () => {
      try {
        const content = document.getElementById('content').value.trim();
        if (!content) { setStatus('Content is required'); return; }
        const res = await fetch(api + '/api/entries', { method:'POST', headers: headers(), body: JSON.stringify({content}) });
        const body = await res.json();
        if (!res.ok) throw new Error(body.error || 'request failed');
        document.getElementById('content').value = '';
        setStatus('Entry posted');
      } catch (e) {
        setStatus('Post failed: ' + e.message);
      }
    };

    document.getElementById('loadEntries').onclick = loadEntries;

    async function loadEntries() {
      try {
        const day = dayEl.value;
        const res = await fetch(api + '/api/entries?day=' + encodeURIComponent(day), { headers: headers() });
        const body = await res.json();
        if (!res.ok) throw new Error(body.error || 'request failed');
        renderEntries(body.entries || []);
        setStatus('Loaded ' + (body.entries || []).length + ' entries');
      } catch (e) {
        entriesEl.innerHTML = '';
        setStatus('Load failed: ' + e.message);
      }
    }

    function esc(s) {
      return String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#039;'}[c]));
    }

    function renderEntries(entries) {
      if (!entries.length) {
        entriesEl.innerHTML = '<div class="entry"><div class="meta">No entries</div></div>';
        return;
      }
      entriesEl.innerHTML = entries.map(e => {
        return '<div class="entry">'
          + '<div class="meta">[' + esc(e.entry_type) + '] ' + esc(e.user) + ' @ ' + esc(e.created_at) + '</div>'
          + '<div>' + esc(e.content) + '</div>'
          + '</div>';
      }).join('');
    }
  </script>
</body>
</html>`
