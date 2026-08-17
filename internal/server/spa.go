package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// The admin SPA, served from disk rather than from the binary (WEB-5/DEPLOY-5).
//
// hz used to embed ui/dist with //go:embed. Canon serves a deployed service's
// assets from the payload instead, so the UI ships beside the binary and is
// installed with it.
//
// The tradeoff is real and worth stating where the code is: a single binary
// with the UI inside it could be scp'd onto a broken gateway and still render.
// Now it cannot, so the failure has to be legible — a missing directory
// explains itself below rather than 404ing the login page.

// DefaultUIDir is where the deploy installs the built frontend.
const DefaultUIDir = "/usr/local/share/homelab-horizon/ui"

// uiDir resolves where the SPA lives, in precedence order:
//
//  1. STATIC_DIR — the canon env var, so a slot deploy can point at its payload
//  2. ui_dir in config — for an operator who keeps it somewhere else
//  3. ./ui/dist — the working tree, so `go run` and `make dev` serve the app
//     without installing anything
//  4. DefaultUIDir — where the deploy puts it
func (s *Server) uiDir() string {
	if dir := strings.TrimSpace(os.Getenv("STATIC_DIR")); dir != "" {
		return dir
	}
	if dir := strings.TrimSpace(s.cfg().UIDir); dir != "" {
		return dir
	}
	if _, err := os.Stat(filepath.Join("ui", "dist", "index.html")); err == nil {
		return filepath.Join("ui", "dist")
	}
	return DefaultUIDir
}

func (s *Server) setupSPA(mux *http.ServeMux) {
	mux.HandleFunc("/app/", func(w http.ResponseWriter, r *http.Request) {
		dir := s.uiDir()
		index := filepath.Join(dir, "index.html")
		if _, err := os.Stat(index); err != nil {
			s.serveMissingUI(w, dir)
			return
		}

		// Strip /app/ to get the path within the UI directory.
		rel := strings.TrimPrefix(r.URL.Path, "/app/")
		if rel == "" {
			rel = "index.html"
		}

		// Resolve inside dir and refuse anything that escapes it. The embedded
		// FS could not be walked out of; a directory on disk can, so "../"
		// has to be rejected rather than trusted.
		clean := filepath.Join(dir, filepath.Clean("/"+rel))
		if !strings.HasPrefix(clean, filepath.Clean(dir)+string(os.PathSeparator)) {
			http.NotFound(w, r)
			return
		}

		if info, err := os.Stat(clean); err == nil && !info.IsDir() {
			// Vite emits content-hashed filenames under assets/, so those are
			// safe to cache forever. Everything else (notably index.html) must
			// revalidate every load — otherwise a new deploy leaves clients on
			// a stale index.html pointing at hashed chunks that no longer
			// exist, and the app white-screens.
			if strings.HasPrefix(rel, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			http.ServeFile(w, r, clean)
			return
		}

		// Not a file — serve index.html so TanStack Router can handle the
		// route client-side, preserving the URL.
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, index)
	})
}

// serveMissingUI explains an absent frontend instead of 404ing it.
//
// This is the failure un-embedding introduces, and the one place an operator
// meets it is the page they were trying to log into. A bare 404 there reads as
// "hz is broken"; naming the directory and the fix turns it into a two-minute
// problem. The API and the hz CLI are unaffected, which is worth saying too,
// because that is how the box gets administered until this is fixed.
func (s *Server) serveMissingUI(w http.ResponseWriter, dir string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = fmt.Fprintf(w, `<!doctype html>
<title>homelab-horizon — UI not installed</title>
<style>
 body{font:16px/1.5 system-ui,sans-serif;max-width:44rem;margin:4rem auto;padding:0 1.5rem;color:#222}
 code{background:#f4f4f5;padding:.15rem .35rem;border-radius:3px}
 .muted{color:#666}
</style>
<h1>The admin UI is not installed</h1>
<p>hz is running, but it found no frontend at <code>%s</code>.</p>
<p>The UI ships beside the binary rather than inside it, so an upgrade that
copied only the binary lands here. Redeploy with <code>./bin/deploy</code>,
which installs both, or copy <code>ui/dist</code> to that path.</p>
<p class="muted">The API and the <code>hz</code> CLI are unaffected — the box is
still administrable while you sort this out.</p>
`, dir)
}
