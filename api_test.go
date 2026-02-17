package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	app := &App{
		db:     db,
		logger: log.New(io.Discard, "", 0),
	}
	if err := app.initSchema(); err != nil {
		t.Fatalf("initSchema: %v", err)
	}
	return app
}

func newTestMux(app *App) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", app.handleHealth)
	mux.HandleFunc("/api/me", app.withAuth(app.handleMe))
	mux.HandleFunc("/api/entries", app.withAuth(app.handleEntries))
	return app.withCORS(mux)
}

func createUser(t *testing.T, app *App, username, token string) {
	t.Helper()
	_, err := app.db.Exec(
		`INSERT INTO users(username, token_hash, created_at) VALUES(?, ?, ?)`,
		username,
		hashToken(token),
		nowUTC(),
	)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
}

func authedReq(t *testing.T, method, path string, body any, token string) *http.Request {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func TestAPIHealth(t *testing.T) {
	app := newTestApp(t)
	h := newTestMux(app)
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal health response: %v", err)
	}
	if got["status"] != "ok" {
		t.Fatalf("expected status=ok, got %q", got["status"])
	}
}

func TestAPIMeRequiresAuth(t *testing.T) {
	app := newTestApp(t)
	h := newTestMux(app)
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/me", nil))

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestAPIMeAuthenticated(t *testing.T) {
	app := newTestApp(t)
	h := newTestMux(app)
	createUser(t, app, "alice", "PUDABCDEF12")

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(t, http.MethodGet, "/api/me", nil, "PUDABCDEF12"))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var got AuthedUser
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal me response: %v", err)
	}
	if got.Username != "alice" {
		t.Fatalf("expected username alice, got %q", got.Username)
	}
}

func TestAPICreateAndListEntries(t *testing.T) {
	app := newTestApp(t)
	h := newTestMux(app)
	token := "PUDZXCVBNM1"
	createUser(t, app, "bob", token)

	createRR := httptest.NewRecorder()
	h.ServeHTTP(createRR, authedReq(t, http.MethodPost, "/api/entries", map[string]string{
		"content": "shipped api tests",
	}, token))
	if createRR.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", createRR.Code, createRR.Body.String())
	}

	day := time.Now().UTC().Format("2006-01-02")
	listRR := httptest.NewRecorder()
	h.ServeHTTP(listRR, authedReq(t, http.MethodGet, "/api/entries?day="+day, nil, token))
	if listRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", listRR.Code, listRR.Body.String())
	}

	var got struct {
		Entries []entryRow `json:"entries"`
		Day     string     `json:"day"`
	}
	if err := json.Unmarshal(listRR.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	if got.Day != day {
		t.Fatalf("expected day %s, got %s", day, got.Day)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got.Entries))
	}
	if got.Entries[0].Content != "shipped api tests" {
		t.Fatalf("unexpected content: %q", got.Entries[0].Content)
	}
}

func TestAPICreateEntryLocked(t *testing.T) {
	app := newTestApp(t)
	h := newTestMux(app)
	token := "PUDQWERTYU1"
	createUser(t, app, "eve", token)
	app.writeLocked.Store(true)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(t, http.MethodPost, "/api/entries", map[string]string{
		"content": "blocked write",
	}, token))

	if rr.Code != http.StatusLocked {
		t.Fatalf("expected 423, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAPIListEntriesInvalidDay(t *testing.T) {
	app := newTestApp(t)
	h := newTestMux(app)
	token := "PUDDAYTEST1"
	createUser(t, app, "chris", token)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(t, http.MethodGet, "/api/entries?day=17-02-2026", nil, token))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

