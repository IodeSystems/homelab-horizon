package server

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/server/uiembed"
)

// The admin SPA, served from the binary when it has one and from disk when it
// does not.
//
// hz embedded ui/dist, then un-embedded it (WEB-5) to match canon, which serves
// a deployed service's assets from its payload. That is right for a service hz
// deploys and wrong for hz itself: hz is the thing you scp onto a gateway that
// is already broken, and a release whose admin page needs a second artifact
// installed correctly is a release that can land half-working. So the binary
// carries the UI again, and the disk paths remain for the cases that need them
// — a slot deploy pointing at its own payload, and a working tree during
// development.
//
// Precedence is deliberate: an explicitly configured directory beats the
// working tree, which beats the embedded copy, which beats the legacy install
// directory. Embedded outranks DefaultUIDir specifically so a stale directory
// left by an older deploy cannot shadow the UI compiled into the new binary —
// that would be invisible and would look like the upgrade silently failed.

// DefaultUIDir is where deploys before v0.1.0 installed the built frontend.
// Still honoured so those installs keep working; no longer written to.
//
// A var rather than a const only so tests can point it at a temp directory —
// the ordering against the embedded copy is the thing worth proving and it
// cannot be proved against a path the test cannot create.
var DefaultUIDir = "/usr/local/share/homelab-horizon/ui"

// uiSource is where the SPA is being served from, and how to describe it if
// nothing can be found.
type uiSource struct {
	fsys fs.FS
	// from names the source for the operator: a path, or "compiled into the
	// binary". Only surfaced in the missing-UI page and the health card.
	from string
}

// resolveUI picks the first source that actually holds an index.html.
//
// Every candidate is probed rather than assumed, because the failure this
// avoids — a configured path that exists but is empty — used to produce a blank
// page instead of an explanation.
func (s *Server) resolveUI() (uiSource, bool) {
	var disk []string

	// 1. STATIC_DIR — the canon env var, so a slot deploy can point at its payload.
	if dir := strings.TrimSpace(os.Getenv("STATIC_DIR")); dir != "" {
		disk = append(disk, dir)
	}
	// 2. ui_dir in config — for an operator who keeps it somewhere else.
	if dir := strings.TrimSpace(s.cfg().UIDir); dir != "" {
		disk = append(disk, dir)
	}
	// 3. ./ui/dist — the working tree, so `make dev` serves what was just built
	//    rather than whatever was compiled in at build time.
	disk = append(disk, filepath.Join("ui", "dist"))

	for _, dir := range disk {
		fsys := os.DirFS(dir)
		if _, err := fs.Stat(fsys, "index.html"); err == nil {
			return uiSource{fsys: fsys, from: dir}, true
		}
	}

	// 4. Compiled in, for a release binary on a real gateway.
	if fsys, ok := uiembed.FS(); ok {
		return uiSource{fsys: fsys, from: "compiled into the binary"}, true
	}

	// 5. Where deploys before v0.1.0 put it.
	legacy := os.DirFS(DefaultUIDir)
	if _, err := fs.Stat(legacy, "index.html"); err == nil {
		return uiSource{fsys: legacy, from: DefaultUIDir}, true
	}

	return uiSource{}, false
}

func (s *Server) setupSPA(mux *http.ServeMux) {
	mux.HandleFunc("/app/", func(w http.ResponseWriter, r *http.Request) {
		src, ok := s.resolveUI()
		if !ok {
			s.serveMissingUI(w)
			return
		}

		// Strip /app/ to get the path within the UI.
		rel := strings.TrimPrefix(r.URL.Path, "/app/")
		if rel == "" {
			rel = "index.html"
		}

		// fs.FS rejects "..", absolute paths and empty elements by contract, so
		// this one check replaces the manual traversal guard the disk-only
		// version needed.
		if !fs.ValidPath(rel) {
			http.NotFound(w, r)
			return
		}

		if info, err := fs.Stat(src.fsys, rel); err == nil && !info.IsDir() {
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
			http.ServeFileFS(w, r, src.fsys, rel)
			return
		}

		// Not a file — serve index.html so TanStack Router can handle the
		// route client-side, preserving the URL.
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, src.fsys, "index.html")
	})
}

// serveMissingUI explains an absent frontend instead of 404ing it.
//
// Only reachable now for a binary built without the uiembed tag and run with no
// UI on disk — `go build ./...` output, or a hand-rolled build. The operator
// meets it on the page they were trying to log into, so it names the fix rather
// than 404ing.
func (s *Server) serveMissingUI(w http.ResponseWriter) {
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
<p>hz is running, but this build has no UI compiled in and none on disk.</p>
<p>Release binaries carry the UI inside them — build with <code>make</code>
rather than a plain <code>go build</code>, or point <code>STATIC_DIR</code> (or
<code>ui_dir</code> in the config) at a built frontend. Looked in
<code>%s</code> and <code>%s</code>.</p>
<p class="muted">The API and the <code>hz</code> CLI are unaffected — the box is
still administrable while you sort this out.</p>
`, filepath.Join("ui", "dist"), DefaultUIDir)
}
