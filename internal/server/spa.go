package server

import (
	"io/fs"
	"net/http"
	"strings"

	homelabUI "github.com/iodesystems/homelab-horizon/ui"
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
			_ = f.Close()
			// Vite emits content-hashed filenames under assets/, so those are
			// safe to cache forever. Everything else (notably index.html) must
			// revalidate every load — otherwise a new deploy leaves clients on a
			// stale index.html pointing at hashed chunks that no longer exist,
			// and the app white-screens.
			if strings.HasPrefix(path, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			http.ServeFileFS(w, r, distFS, path)
			return
		}

		// File not found — serve index.html for SPA client-side routing.
		// This preserves the URL so TanStack Router picks up the correct route.
		// no-cache so the shell always revalidates and can't go stale post-deploy.
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, distFS, "index.html")
	})
}
