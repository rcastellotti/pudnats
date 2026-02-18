package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const (
	defaultDBPath  = "./devlog.db"
	defaultLogPath = "-"
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
	Role     string `json:"role"`
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
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "serve":
			return runServe(os.Args[2:])
		case "admin":
			return runAdmin(os.Args[2:])
		case "help", "-h", "--help":
			printRootUsage(os.Stdout)
			return nil
		}
	}
	return runServe(os.Args[1:])
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s serve [options]\n\n", binName())
		fmt.Fprintln(fs.Output(), "Runs API (:9173) and web UI (:9172).")
		fmt.Fprintln(fs.Output())
		fmt.Fprintln(fs.Output(), "Options:")
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", defaultDBPath, "sqlite db path")
	logPath := fs.String("log", defaultLogPath, "log target path ('-' for stdout only)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
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
	apiMux.HandleFunc("/api/admin/users", app.withAuth(app.withAdmin(app.handleAdminUsers)))

	uiMux := http.NewServeMux()
	uiMux.HandleFunc("/", app.handleUI)
	uiMux.HandleFunc("/entries-view", app.handleEntriesViewUI)
	uiMux.HandleFunc("/assets/oat.min.css", app.handleOatCSS)
	uiMux.HandleFunc("/assets/oat.min.js", app.handleOatJS)

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

func printRootUsage(w io.Writer) {
	fmt.Fprintf(w, "Usage: %s <command> [options]\n\n", binName())
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  serve        Run API and web UI servers (default if no command is provided)")
	fmt.Fprintln(w, "  admin        Administrative commands (user/token management)")
	fmt.Fprintln(w, "  help         Show this help")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Try: %s admin --help\n", binName())
}

func binName() string {
	return filepath.Base(os.Args[0])
}

func buildLogger(path string) (*log.Logger, func(), error) {
	if path == "" || path == "-" {
		return log.New(os.Stdout, "", log.LstdFlags|log.LUTC), func() {}, nil
	}

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
	role TEXT NOT NULL DEFAULT 'member',
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
	if _, err := a.db.Exec(schema); err != nil {
		return err
	}
	return a.ensureUsersRoleColumn()
}

func (a *App) ensureUsersRoleColumn() error {
	hasRole, err := a.usersRoleColumnExists()
	if err != nil {
		return err
	}
	if !hasRole {
		if _, err := a.db.Exec(`ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'member'`); err != nil {
			return err
		}
	}
	_, err = a.db.Exec(`UPDATE users SET role='member' WHERE role IS NULL OR role=''`)
	return err
}

func (a *App) usersRoleColumnExists() (bool, error) {
	rows, err := a.db.Query(`PRAGMA table_info(users)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == "role" {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
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

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func normalizeRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		return "member"
	}
	return role
}

func validRole(role string) bool {
	return role == "member" || role == "admin"
}

func (a *App) createUserWithRole(username, role string) (int64, string, error) {
	username = strings.TrimSpace(username)
	role = normalizeRole(role)
	if username == "" {
		return 0, "", errors.New("username is required")
	}
	if !validRole(role) {
		return 0, "", errors.New("invalid role")
	}

	token, err := generateToken()
	if err != nil {
		return 0, "", err
	}
	hash := hashToken(token)

	res, err := a.db.Exec(`INSERT INTO users(username, token_hash, role, created_at) VALUES(?, ?, ?, ?)`, username, hash, role, nowUTC())
	if err != nil {
		return 0, "", err
	}
	uid, _ := res.LastInsertId()
	return uid, token, nil
}
