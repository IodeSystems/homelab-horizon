package server

import (
	"io/fs"
	"net/http"
	"strings"

	homelabUI "homelab-horizon/ui"
)

func (s *Server) setupSPA(mux *http.ServeMux) {
	distFS, err := fs.Sub(homelabUI.DistFS, "dist")
	if err != nil {
		return
	}

	fileServer := http.StripPrefix("/app", http.FileServer(http.FS(distFS)))

	mux.HandleFunc("/app/", func(w http.ResponseWriter, r *http.Request) {
		// Check if the requested file exists in the embedded FS
		path := strings.TrimPrefix(r.URL.Path, "/app/")
		if path == "" {
			path = "index.html"
		}

		if _, err := fs.Stat(distFS, path); err != nil {
			// File not found — serve index.html for SPA client-side routing
			r.URL.Path = "/app/index.html"
		}

		fileServer.ServeHTTP(w, r)
	})
}
