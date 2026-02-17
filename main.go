package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const (
	defaultDBPath  = "./devlog.db"
	defaultLogPath = "./devlog.log"
)

type App struct {
	db          *sql.DB
	logger      *log.Logger
	writeLocked atomic.Bool
	compactMu   sync.Mutex
}

type AuthedUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type entryRow struct {
	ID        int64  `json:"id"`
	User      string `json:"user"`
	EntryType string `json:"entry_type"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) >= 2 && os.Args[1] == "serve" {
		return runServe(os.Args[2:])
	}
	if len(os.Args) >= 2 && os.Args[1] == "admin" {
		return runAdmin(os.Args[2:])
	}
	return runServe(os.Args[1:])
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	dbPath := fs.String("db", defaultDBPath, "sqlite db path")
	logPath := fs.String("log", defaultLogPath, "log file path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger, closeLog, err := buildLogger(*logPath)
	if err != nil {
		return err
	}
	defer closeLog()

	db, err := openDB(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	app := &App{db: db, logger: logger}
	if err := app.initSchema(); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go app.compactionLoop(ctx)

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/api/health", app.handleHealth)
	apiMux.HandleFunc("/api/me", app.withAuth(app.handleMe))
	apiMux.HandleFunc("/api/entries", app.withAuth(app.handleEntries))

	uiMux := http.NewServeMux()
	uiMux.HandleFunc("/", app.handleUI)

	apiServer := &http.Server{Addr: ":9173", Handler: app.withCORS(apiMux)}
	uiServer := &http.Server{Addr: ":9172", Handler: uiMux}

	errCh := make(chan error, 2)
	go func() {
		app.logger.Printf("event=server_start kind=api port=9173")
		errCh <- apiServer.ListenAndServe()
	}()
	go func() {
		app.logger.Printf("event=server_start kind=ui port=9172")
		errCh <- uiServer.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		app.logger.Printf("event=shutdown signal=%s", sig.String())
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = apiServer.Shutdown(shutdownCtx)
	_ = uiServer.Shutdown(shutdownCtx)
	return nil
}

func buildLogger(path string) (*log.Logger, func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	mw := io.MultiWriter(os.Stdout, f)
	logger := log.New(mw, "", log.LstdFlags|log.LUTC)
	return logger, func() { _ = f.Close() }, nil
}

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func (a *App) initSchema() error {
	schema := `
CREATE TABLE IF NOT EXISTS users (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	username TEXT NOT NULL UNIQUE,
	token_hash TEXT NOT NULL UNIQUE,
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS entries (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER,
	entry_type TEXT NOT NULL DEFAULT 'normal',
	content TEXT NOT NULL,
	created_at TEXT NOT NULL,
	FOREIGN KEY(user_id) REFERENCES users(id)
);
CREATE INDEX IF NOT EXISTS idx_entries_created_at ON entries(created_at);
CREATE TABLE IF NOT EXISTS action_logs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	actor_type TEXT NOT NULL,
	actor_username TEXT NOT NULL,
	action TEXT NOT NULL,
	metadata TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS compactions (
	day TEXT PRIMARY KEY,
	ran_at TEXT NOT NULL
);
`
	_, err := a.db.Exec(schema)
	return err
}

func (a *App) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Auth-Token")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) withAuth(next func(http.ResponseWriter, *http.Request, AuthedUser)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := a.authUser(r)
		if err != nil {
			jsonErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r, u)
	}
}

func (a *App) authUser(r *http.Request) (AuthedUser, error) {
	tok := strings.TrimSpace(r.Header.Get("X-Auth-Token"))
	if tok == "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			tok = strings.TrimSpace(auth[7:])
		}
	}
	if tok == "" {
		return AuthedUser{}, errors.New("missing token")
	}
	hash := hashToken(tok)
	var u AuthedUser
	err := a.db.QueryRow(`SELECT id, username FROM users WHERE token_hash = ?`, hash).Scan(&u.ID, &u.Username)
	if err != nil {
		return AuthedUser{}, err
	}
	return u, nil
}

func (a *App) handleHealth(w http.ResponseWriter, _ *http.Request) {
	jsonOut(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request, u AuthedUser) {
	_ = a.logAction("api_user", u.Username, "whoami", "path=/api/me")
	jsonOut(w, http.StatusOK, u)
}

func (a *App) handleEntries(w http.ResponseWriter, r *http.Request, u AuthedUser) {
	switch r.Method {
	case http.MethodPost:
		a.handleCreateEntry(w, r, u)
	case http.MethodGet:
		a.handleListEntries(w, r, u)
	default:
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleCreateEntry(w http.ResponseWriter, r *http.Request, u AuthedUser) {
	if a.writeLocked.Load() {
		jsonErr(w, http.StatusLocked, "writes are temporarily locked for daily compaction")
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.Content = strings.TrimSpace(req.Content)
	if req.Content == "" {
		jsonErr(w, http.StatusBadRequest, "content is required")
		return
	}
	if len(req.Content) > 20000 {
		jsonErr(w, http.StatusBadRequest, "content too large")
		return
	}

	res, err := a.db.Exec(`INSERT INTO entries(user_id, entry_type, content, created_at) VALUES(?, 'normal', ?, ?)`, u.ID, req.Content, nowUTC())
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to store entry")
		return
	}
	id, _ := res.LastInsertId()
	_ = a.logAction("api_user", u.Username, "create_entry", fmt.Sprintf("entry_id=%d size=%d", id, len(req.Content)))
	jsonOut(w, http.StatusCreated, map[string]any{"id": id, "status": "created"})
}

func (a *App) handleListEntries(w http.ResponseWriter, r *http.Request, u AuthedUser) {
	day := strings.TrimSpace(r.URL.Query().Get("day"))
	if day == "" {
		day = time.Now().Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", day); err != nil {
		jsonErr(w, http.StatusBadRequest, "day must be YYYY-MM-DD")
		return
	}
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	rows, err := a.db.Query(`
SELECT e.id,
       COALESCE(u.username, 'system') AS username,
       e.entry_type,
       e.content,
       e.created_at
FROM entries e
LEFT JOIN users u ON u.id = e.user_id
WHERE date(e.created_at) = ?
ORDER BY e.created_at DESC, e.id DESC
LIMIT ?`, day, limit)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to query entries")
		return
	}
	defer rows.Close()

	entries := make([]entryRow, 0, 32)
	for rows.Next() {
		var e entryRow
		if err := rows.Scan(&e.ID, &e.User, &e.EntryType, &e.Content, &e.CreatedAt); err != nil {
			jsonErr(w, http.StatusInternalServerError, "failed to parse entries")
			return
		}
		entries = append(entries, e)
	}
	_ = a.logAction("api_user", u.Username, "list_entries", fmt.Sprintf("day=%s limit=%d", day, limit))
	jsonOut(w, http.StatusOK, map[string]any{"entries": entries, "day": day})
}

func (a *App) compactionLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			if now.Hour() < 17 {
				continue
			}
			day := now.Format("2006-01-02")
			ran, err := a.compactionAlreadyRan(day)
			if err != nil {
				a.logger.Printf("event=compaction_check_error day=%s err=%v", day, err)
				continue
			}
			if ran {
				continue
			}
			if err := a.compactDay(day); err != nil {
				a.logger.Printf("event=compaction_failed day=%s err=%v", day, err)
			}
		}
	}
}

func (a *App) compactionAlreadyRan(day string) (bool, error) {
	var v string
	err := a.db.QueryRow(`SELECT day FROM compactions WHERE day = ?`, day).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (a *App) compactDay(day string) error {
	a.compactMu.Lock()
	defer a.compactMu.Unlock()

	a.writeLocked.Store(true)
	defer a.writeLocked.Store(false)

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var exists string
	err = tx.QueryRow(`SELECT day FROM compactions WHERE day = ?`, day).Scan(&exists)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	rows, err := tx.Query(`
SELECT e.id,
       COALESCE(u.username, 'system') AS username,
       e.content,
       e.created_at
FROM entries e
LEFT JOIN users u ON u.id = e.user_id
WHERE date(e.created_at) = ?
  AND e.entry_type = 'normal'
ORDER BY e.created_at ASC, e.id ASC`, day)
	if err != nil {
		return err
	}

	type sourceEntry struct {
		ID        int64
		Username  string
		Content   string
		CreatedAt string
	}
	entries := make([]sourceEntry, 0, 64)
	for rows.Next() {
		var e sourceEntry
		if err := rows.Scan(&e.ID, &e.Username, &e.Content, &e.CreatedAt); err != nil {
			_ = rows.Close()
			return err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	if len(entries) > 0 {
		var b strings.Builder
		b.WriteString("Daily compact for ")
		b.WriteString(day)
		b.WriteString("\n\n")
		for _, e := range entries {
			b.WriteString("[")
			b.WriteString(e.CreatedAt)
			b.WriteString("][")
			b.WriteString(e.Username)
			b.WriteString("] ")
			b.WriteString(strings.ReplaceAll(e.Content, "\n", "\\n"))
			b.WriteString("\n")
		}
		if _, err := tx.Exec(`INSERT INTO entries(user_id, entry_type, content, created_at) VALUES(NULL, 'daily_compact', ?, ?)`, b.String(), nowUTC()); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM entries WHERE date(created_at) = ? AND entry_type = 'normal'`, day); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(`INSERT INTO compactions(day, ran_at) VALUES(?, ?)`, day, nowUTC()); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO action_logs(actor_type, actor_username, action, metadata, created_at) VALUES('system', 'scheduler', 'daily_compact', ?, ?)`, fmt.Sprintf("day=%s merged=%d", day, len(entries)), nowUTC()); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	a.logger.Printf("event=daily_compact day=%s merged=%d", day, len(entries))
	return nil
}

func (a *App) logAction(actorType, actorUsername, action, metadata string) error {
	if actorType == "" {
		actorType = "unknown"
	}
	if actorUsername == "" {
		actorUsername = "unknown"
	}
	if action == "" {
		action = "unknown"
	}
	if metadata == "" {
		metadata = "-"
	}
	_, err := a.db.Exec(`INSERT INTO action_logs(actor_type, actor_username, action, metadata, created_at) VALUES(?, ?, ?, ?, ?)`, actorType, actorUsername, action, metadata, nowUTC())
	if err != nil {
		a.logger.Printf("event=action_log_insert_failed actor_type=%s actor_username=%s action=%s err=%v", actorType, actorUsername, action, err)
		return err
	}
	a.logger.Printf("event=action actor_type=%s actor_username=%s action=%s metadata=%q", actorType, actorUsername, action, metadata)
	return nil
}

func (a *App) handleUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(uiHTML))
}

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	jsonOut(w, code, map[string]string{"error": msg})
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
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
