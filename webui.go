package main

import (
	_ "embed"
	"net/http"
)

//go:embed webui.html
var uiHTML string

//go:embed entries_view.html
var entriesViewHTML string

func (a *App) handleUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(uiHTML))
}

func (a *App) handleEntriesViewUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(entriesViewHTML))
}
