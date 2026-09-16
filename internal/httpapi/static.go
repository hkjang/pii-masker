package httpapi

import (
	"embed"
	"net/http"
)

//go:embed static/*
var staticFiles embed.FS

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	content, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "static assets unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The page is embedded in the binary, so it changes exactly when the server is
	// upgraded. Without a freshness directive a browser keeps serving the old page
	// from its heuristic cache and drives the new API with stale script.
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}
