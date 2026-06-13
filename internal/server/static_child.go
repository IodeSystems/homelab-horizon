package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// StaticServerEnvAddr is the environment variable hz sets when re-execing
// itself as the unprivileged static-file-server child. Its value is the
// loopback address the child binds.
const StaticServerEnvAddr = "HZ_STATIC_SERVER_ADDR"

// RunStaticServerChild runs the unprivileged static file server. hz re-execs
// itself with StaticServerEnvAddr set, dropped to an unprivileged user, and
// this is the child's entire job — so even a bug in the file handler cannot
// read files the unprivileged user can't. The host->site map arrives as
// newline-delimited JSON on stdin (pushed by the parent on each config sync);
// when stdin closes (parent gone) the child shuts down.
func RunStaticServerChild(addr string) {
	ss := newStaticServer()

	srv := &http.Server{
		Addr:              addr,
		Handler:           ss,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		ss.consumeSites(os.Stdin)
		// Parent closed the pipe (shutting down): drain and exit.
		_ = srv.Shutdown(context.Background())
	}()

	slog.Info("static file server child listening", "addr", addr, "uid", os.Geteuid())
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("static file server child failed", "err", err)
		os.Exit(1)
	}
}

// consumeSites reads newline-delimited JSON host->site maps from r and applies
// each to ss, returning when r is exhausted.
func (ss *staticServer) consumeSites(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // allow large maps
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var sites map[string]staticSite
		if err := json.Unmarshal(line, &sites); err != nil {
			slog.Warn("static child: bad site map", "err", err)
			continue
		}
		ss.setSites(sites)
	}
}
