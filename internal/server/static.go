package server

import (
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// staticSite is the serving config for one host. It is also the JSON wire form
// sent from the root parent process to the unprivileged child over its stdin,
// hence the exported, tagged fields.
type staticSite struct {
	Root string `json:"root"` // document root directory
	SPA  bool   `json:"spa"`  // serve index.html for unknown non-asset paths (client-side routing)
}

// deriveStaticSites builds the host->site map from the services in cfg.
func deriveStaticSites(cfg *config.Config) map[string]staticSite {
	sites := make(map[string]staticSite)
	for _, svc := range cfg.Services {
		if svc.Proxy == nil || svc.Proxy.StaticRoot == "" {
			continue
		}
		for _, d := range svc.Domains {
			sites[strings.ToLower(d)] = staticSite{Root: svc.Proxy.StaticRoot, SPA: svc.Proxy.SPA}
		}
	}
	return sites
}

// staticServer is the HTTP handler that serves per-service static folders. The
// document root is selected by the request Host header. It holds no privileges
// of its own; in production it runs inside the unprivileged child process.
type staticServer struct {
	mu    sync.RWMutex
	sites map[string]staticSite // lowercased host -> serving config
}

func newStaticServer() *staticServer {
	return &staticServer{sites: make(map[string]staticSite)}
}

// setSites atomically swaps the host->site map for in-flight requests.
func (ss *staticServer) setSites(sites map[string]staticSite) {
	ss.mu.Lock()
	ss.sites = sites
	ss.mu.Unlock()
}

// Rebuild refreshes the map from cfg (used in-process / by tests).
func (ss *staticServer) Rebuild(cfg *config.Config) {
	ss.setSites(deriveStaticSites(cfg))
}

// siteFor returns the serving config for the given request host, stripping any
// port. ok is false when no static service claims the host.
func (ss *staticServer) siteFor(host string) (staticSite, bool) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	s, ok := ss.sites[strings.ToLower(host)]
	return s, ok
}

func (ss *staticServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Security headers on every response, including errors. nosniff is the
	// important one — it stops a served file from being reinterpreted as a
	// different (executable) type regardless of our Content-Type.
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.Set("Allow", "GET, HEAD")
		ss.writeError(w, http.StatusMethodNotAllowed)
		return
	}

	site, ok := ss.siteFor(r.Host)
	if !ok {
		ss.writeError(w, http.StatusNotFound)
		return
	}
	ss.serveFile(w, r, site)
}

// serveFile serves a single file for the request. Because hz runs as root (for
// now), this handler is deliberately strict beyond the loopback bind:
//   - os.Root confines every open to the document root — no "../" and no
//     symlink can escape it.
//   - hidden path segments (.git, .env, .ssh, dotfiles) are never served.
//   - directories are never listed; a directory serves its index.html or 404s.
//   - Content-Type is set explicitly from the extension (no content sniffing).
func (ss *staticServer) serveFile(w http.ResponseWriter, r *http.Request, site staticSite) {
	name := cleanRequestPath(r.URL.Path)

	// Refuse any hidden segment outright (also covers a residual "..").
	for _, seg := range strings.Split(name, "/") {
		if seg != "." && strings.HasPrefix(seg, ".") {
			ss.writeError(w, http.StatusNotFound)
			return
		}
	}

	// os.Root pins all opens inside the document root, rejecting symlink/".."
	// escape even though hz runs as root.
	root, err := os.OpenRoot(site.Root)
	if err != nil {
		slog.Warn("static: cannot open root", "dir", site.Root, "err", err)
		ss.writeError(w, http.StatusInternalServerError)
		return
	}
	defer func() { _ = root.Close() }()

	f, info, err := openServable(root, name)
	if err != nil {
		// SPA fallback: a browser refresh on a client-side route (no file
		// extension) serves index.html so the app boots and routes itself.
		if site.SPA && path.Ext(name) == "" {
			if idx, idxInfo, idxErr := openServable(root, "index.html"); idxErr == nil {
				serveContent(w, r, "index.html", idxInfo, idx)
				_ = idx.Close()
				return
			}
		}
		ss.notFound(w, r, root)
		return
	}
	defer func() { _ = f.Close() }()

	serveContent(w, r, info.Name(), info, f)
}

// notFound serves the site's own 404.html (with a 404 status) when present,
// falling back to the built-in error page.
func (ss *staticServer) notFound(w http.ResponseWriter, r *http.Request, root *os.Root) {
	if f, err := root.Open("404.html"); err == nil {
		info, statErr := f.Stat()
		if statErr == nil && !info.IsDir() {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			if r.Method != http.MethodHead {
				_, _ = io.Copy(w, f)
			}
			_ = f.Close()
			return
		}
		_ = f.Close()
	}
	ss.writeError(w, http.StatusNotFound)
}

// openServable opens name within root, resolving a directory to its index.html.
// It never returns a directory — callers get a regular file or an error, so
// there is no directory listing.
func openServable(root *os.Root, name string) (*os.File, os.FileInfo, error) {
	if name == "" {
		name = "."
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if info.IsDir() {
		_ = f.Close()
		f, err = root.Open(path.Join(name, "index.html"))
		if err != nil {
			return nil, nil, err
		}
		info, err = f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		if info.IsDir() {
			_ = f.Close()
			return nil, nil, fmt.Errorf("index.html is a directory")
		}
	}
	return f, info, nil
}

// serveContent sets an explicit Content-Type from the file extension before
// delegating to http.ServeContent (which then handles Range and
// If-Modified-Since). Setting it ourselves sidesteps content-sniffing and
// system mime.types inconsistencies.
func serveContent(w http.ResponseWriter, r *http.Request, name string, info os.FileInfo, f *os.File) {
	if ct := contentType(name); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// staticContentTypes pins the Content-Type for common web assets so serving
// does not depend on the host's /etc/mime.types. mime.TypeByExtension is the
// fallback for anything not listed.
var staticContentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".htm":   "text/html; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".json":  "application/json",
	".map":   "application/json",
	".xml":   "application/xml",
	".txt":   "text/plain; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".avif":  "image/avif",
	".ico":   "image/x-icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".wasm":  "application/wasm",
	".pdf":   "application/pdf",
}

func contentType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if ct, ok := staticContentTypes[ext]; ok {
		return ct
	}
	return mime.TypeByExtension(ext)
}

// cleanRequestPath normalizes the URL path to a root-relative, cleaned name
// with no leading slash (e.g. "/a/../b/" -> "b").
func cleanRequestPath(p string) string {
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return strings.TrimPrefix(path.Clean(p), "/")
}

// writeError renders the static server's standard error page for code.
func (ss *staticServer) writeError(w http.ResponseWriter, code int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	text := http.StatusText(code)
	_, _ = fmt.Fprintf(w, errorPageTmpl, code, text, code, text)
}

const errorPageTmpl = `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%d %s</title>
<style>html,body{height:100%%;margin:0}body{font:16px/1.5 system-ui,-apple-system,sans-serif;background:#0d1117;color:#e6edf3;display:grid;place-items:center}div{text-align:center}h1{font-size:3.5rem;margin:0 0 .2em}p{color:#9da7b3;margin:0}</style>
</head><body><div><h1>%d</h1><p>%s</p></div></body></html>
`
