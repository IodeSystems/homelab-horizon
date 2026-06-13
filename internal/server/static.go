package server

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// staticServer serves per-service static folders on an internal loopback
// listener. HAProxy routes a static-folder service's domains here (see
// Config.StaticServeAddr); the document root is selected by the request Host
// header. The listener binds to 127.0.0.1 only, so it is reachable solely by
// the local HAProxy — never directly from the network.
type staticServer struct {
	mu    sync.RWMutex
	roots map[string]string // lowercased host -> filesystem root directory
}

func newStaticServer() *staticServer {
	return &staticServer{roots: make(map[string]string)}
}

// Rebuild refreshes the host->root map from the current config. Safe to call on
// every config sync; the swap is atomic for in-flight requests.
func (ss *staticServer) Rebuild(cfg *config.Config) {
	roots := make(map[string]string)
	for _, svc := range cfg.Services {
		if svc.Proxy == nil || svc.Proxy.StaticRoot == "" {
			continue
		}
		for _, d := range svc.Domains {
			roots[strings.ToLower(d)] = svc.Proxy.StaticRoot
		}
	}
	ss.mu.Lock()
	ss.roots = roots
	ss.mu.Unlock()
}

// rootFor returns the document root configured for the given request host,
// stripping any port. ok is false when no static service claims the host.
func (ss *staticServer) rootFor(host string) (string, bool) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	root, ok := ss.roots[strings.ToLower(host)]
	return root, ok
}

func (ss *staticServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	root, ok := ss.rootFor(r.Host)
	if !ok {
		http.Error(w, "no static service for host", http.StatusNotFound)
		return
	}
	ss.serveFile(w, r, root)
}

// serveFile serves a single file from dir for the request. Because hz runs as
// root, this handler is deliberately strict beyond the loopback bind:
//   - os.Root confines every open to dir — no "../" and no symlink can escape
//     it, so a stray symlink inside the root can't leak /etc/shadow et al.
//   - hidden path segments (.git, .env, .ssh, dotfiles) are never served.
//   - directories are never listed; a directory serves its index.html or 404s.
func (ss *staticServer) serveFile(w http.ResponseWriter, r *http.Request, dir string) {
	upath := r.URL.Path
	if !strings.HasPrefix(upath, "/") {
		upath = "/" + upath
	}
	name := strings.TrimPrefix(path.Clean(upath), "/")

	// Refuse any hidden segment outright (also covers a residual "..").
	for _, seg := range strings.Split(name, "/") {
		if seg != "." && strings.HasPrefix(seg, ".") {
			http.NotFound(w, r)
			return
		}
	}
	if name == "" {
		name = "."
	}

	// os.Root pins all subsequent opens inside dir, rejecting symlink/".." escape.
	root, err := os.OpenRoot(dir)
	if err != nil {
		slog.Warn("static: cannot open root", "dir", dir, "err", err)
		http.NotFound(w, r)
		return
	}
	defer func() { _ = root.Close() }()

	f, err := root.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// No directory listing: a directory request must resolve to its index.html.
	if info.IsDir() {
		_ = f.Close()
		f, err = root.Open(path.Join(name, "index.html"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		info, err = f.Stat()
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
	}

	// ServeContent handles Content-Type sniffing, Range, and If-Modified-Since.
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

// Start binds the loopback listener and serves in the background. A bind
// failure is logged and non-fatal: only static-folder services are affected,
// and the rest of hz continues to run.
func (ss *staticServer) Start(addr string) {
	srv := &http.Server{
		Addr:              addr,
		Handler:           ss,
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Warn("static file server: could not bind, static services disabled", "addr", addr, "err", err)
		return
	}
	slog.Info("static file server listening", "addr", addr)
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Warn("static file server stopped", "err", err)
		}
	}()
}
