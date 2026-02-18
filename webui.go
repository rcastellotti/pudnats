package main

import (
	"embed"
	"html/template"
	"net/http"
)

type uiPageData struct {
	Title string
}

//go:embed templates/*.html
var uiTemplatesFS embed.FS

//go:embed oat.min.css
var oatCSS []byte

//go:embed oat.min.js
var oatJS []byte

func (a *App) handleUI(w http.ResponseWriter, _ *http.Request) {
	renderUI(w, "templates/index.html", uiPageData{Title: "Pudnats"})
}

func (a *App) handleEntriesViewUI(w http.ResponseWriter, _ *http.Request) {
	renderUI(w, "templates/entries-view.html", uiPageData{Title: "Pudnats Entries"})
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

func renderUI(w http.ResponseWriter, pagePath string, data uiPageData) {
	t, err := template.ParseFS(uiTemplatesFS, "templates/base.html", pagePath)
	if err != nil {
		http.Error(w, "failed to load template", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "base", data); err != nil {
		http.Error(w, "failed to render template", http.StatusInternalServerError)
	}
}
