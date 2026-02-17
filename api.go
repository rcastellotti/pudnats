package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

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

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	jsonOut(w, code, map[string]string{"error": msg})
}
