package server

import (
	"log/slog"
	"net"
	"net/http"
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
	// http.Dir + FileServer reject path-traversal ("../") and serve index.html
	// for directory requests.
	http.FileServer(http.Dir(root)).ServeHTTP(w, r)
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
