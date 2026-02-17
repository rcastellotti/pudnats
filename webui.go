package main

import (
	_ "embed"
	"net/http"
)

//go:embed webui.html
var uiHTML string

//go:embed entries_view.html
var entriesViewHTML string

//go:embed oat.min.css
var oatCSS []byte

//go:embed oat.min.js
var oatJS []byte

func (a *App) handleUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(uiHTML))
}

func (a *App) handleEntriesViewUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(entriesViewHTML))
}

func (a *App) handleOatCSS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(oatCSS)
}

func (a *App) handleOatJS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(oatJS)
}
