package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// StaticServerEnvAddr is the environment variable that puts this binary into
// static-file-server mode. Its value is the loopback address to bind.
//
// TWO TRANSPORTS FOR ONE MAP, and which one is in use is the whole of the
// difference between the two ways this process gets started:
//
//   - stdin, when hz FORKED it (staticSupervisor). The parent holds the map
//     and pushes it down a pipe on every config change; stdin closing means
//     the parent is gone and the child should stop.
//   - a FILE, when systemd started it as the agent-managed unit. There is no
//     parent to push anything, so hz declares the map as a file in the agent
//     payload (internal/server/static_unit.go) and the agent restarts the unit
//     when the file moves. See plan/design/privilege-audit.md §7.1 decision 3.
//
// The handler is the same object either way: only where the map comes from
// changes, which is why the child survives the supervisor's retirement rather
// than being deleted with it.
const StaticServerEnvAddr = "HZ_STATIC_SERVER_ADDR"

// StaticServerEnvSites names the file holding the host->site map. Set, the
// child reads the map from that file and ignores stdin; unset, it reads the
// newline-delimited push from stdin.
const StaticServerEnvSites = "HZ_STATIC_SITES"

// RunStaticServerChild runs the static file server: this binary's entire job
// when it is started in static mode, so even a bug in the file handler can
// only reach what the serving process itself can open.
//
// sitesPath selects the transport (see StaticServerEnvAddr). It returns when
// the server stops; a non-nil error is a reason to exit non-zero.
func RunStaticServerChild(addr, sitesPath string) error {
	ss := newStaticServer()

	// UNREADABLE IS NOT EMPTY, and this is where the difference is decided.
	//
	// Serving with no map answers 404 for every host, which is exactly what hz
	// wants when it has declared that this machine has no static sites — and
	// is a silent outage when the file is merely missing or corrupt. So an
	// empty map is served and an unreadable one is refused: systemd's
	// Restart=on-failure then keeps trying, and the unit is visibly failed
	// rather than quietly answering 404 to a live site.
	if sitesPath != "" {
		sites, err := loadSiteMap(sitesPath)
		if err != nil {
			return err
		}
		ss.setSites(sites)
		slog.Info("static file server: site map loaded", "path", sitesPath, "sites", len(sites))
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           ss,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	if sitesPath == "" {
		go func() {
			ss.consumeSites(os.Stdin)
			// Parent closed the pipe (shutting down): drain and exit.
			_ = srv.Shutdown(context.Background())
		}()
	}

	slog.Info("static file server listening", "addr", addr, "uid", os.Geteuid(), "sites_from", siteSource(sitesPath))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("static file server: %w", err)
	}
	return nil
}

// siteSource names the transport for the log line, so a box says which of the
// two ways it was started without anybody having to read the unit.
func siteSource(sitesPath string) string {
	if sitesPath == "" {
		return "stdin (forked by hz)"
	}
	return sitesPath
}

// loadSiteMap reads the host->site map from path.
//
// A file holding `{}` is a valid answer — hz saying this machine serves no
// static sites — and is NOT an error. Anything that stops the map being read
// at all is, for the reason RunStaticServerChild gives.
func loadSiteMap(path string) (map[string]staticSite, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("static file server: reading the site map: %w", err)
	}
	var sites map[string]staticSite
	if err := json.Unmarshal(b, &sites); err != nil {
		return nil, fmt.Errorf("static file server: parsing the site map %s: %w", path, err)
	}
	if sites == nil {
		// `null` parses without error and is not a map hz would write. Treat it
		// as the unreadable case rather than as an empty one.
		return nil, fmt.Errorf("static file server: the site map %s is null, not a map of hosts", path)
	}
	return sites, nil
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
