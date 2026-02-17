package main

import (
	_ "embed"
	"net/http"
)

//go:embed webui.html
var uiHTML string

func (a *App) handleUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(uiHTML))
}
