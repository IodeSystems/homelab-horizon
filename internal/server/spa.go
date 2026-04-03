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

	mux.HandleFunc("/app/", func(w http.ResponseWriter, r *http.Request) {
		// Strip /app/ prefix to get the path within dist/
		path := strings.TrimPrefix(r.URL.Path, "/app/")
		if path == "" {
			path = "index.html"
		}

		// If the file exists in the embedded FS, serve it directly
		if f, err := distFS.Open(path); err == nil {
			f.Close()
			// Serve the static file
			http.ServeFileFS(w, r, distFS, path)
			return
		}

		// File not found — serve index.html for SPA client-side routing
		// This preserves the URL so TanStack Router picks up the correct route
		http.ServeFileFS(w, r, distFS, "index.html")
	})
}
