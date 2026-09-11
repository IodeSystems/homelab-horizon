package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/hzlog"
	"github.com/iodesystems/homelab-horizon/internal/probe"
)

// Default paths. install writes the unit against these, so an operator who
// takes every default never types a path.
const (
	defaultTokenFile = "/etc/hz-probe/token"
	defaultStatePath = "/var/lib/hz-probe/state.json"
	defaultCertPath  = "/etc/hz-probe/cert.pem"
	defaultKeyPath   = "/etc/hz-probe/key.pem"
	defaultListen    = ":8443"
)

// serveFlags is the set shared by serve, install and show-systemd: install
// writes a unit that runs serve, so the two must not drift.
type serveFlags struct {
	listen    string
	vantage   string
	token     string
	tokenFile string
	statePath string
	tlsCert   string
	tlsKey    string
	pushTo    string
}

func (f *serveFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.listen, "listen", defaultListen, "address to listen on")
	fs.StringVar(&f.vantage, "vantage", "", "name for this vantage point (default: hostname)")
	fs.StringVar(&f.token, "token", "", "shared token hz must present (prefer --token-file)")
	fs.StringVar(&f.tokenFile, "token-file", defaultTokenFile, "file holding the shared token")
	fs.StringVar(&f.statePath, "state", defaultStatePath, "target cache; keeps probing across a restart while hz is down")
	fs.StringVar(&f.tlsCert, "tls-cert", defaultCertPath, "TLS certificate file")
	fs.StringVar(&f.tlsKey, "tls-key", defaultKeyPath, "TLS key file")
	fs.StringVar(&f.pushTo, "push-to", "", "hz base URL to report results to; when set, the agent reports rather than listening")
}

// pushMode reports whether the agent reports to hz instead of waiting to be
// asked. It is the default the installer configures, because it needs no
// public address, no inbound rule and no certificate of its own.
func (f *serveFlags) pushMode() bool { return strings.TrimSpace(f.pushTo) != "" }

// vantageName is the configured name, or this host's.
func (f *serveFlags) vantageName() string {
	if f.vantage != "" {
		return f.vantage
	}
	if h, _ := os.Hostname(); h != "" {
		return h
	}
	return "probe"
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	var f serveFlags
	f.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	hzlog.Setup()

	tok, err := resolveToken(f.token, f.tokenFile)
	if err != nil {
		return err
	}

	agent := probe.NewAgent(f.vantageName(), Version, tok, f.statePath)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go agent.Loop(ctx)

	// Push mode: report to hz and do not listen at all. Nothing has to reach
	// this host, so there is no port to open, no certificate to serve and no
	// address to keep stable.
	if f.pushMode() {
		pusher := &probe.Pusher{URL: f.pushTo, Token: tok}
		held := agent.Targets()
		slog.Info("hz-probe reporting to hz",
			"hz", f.pushTo, "vantage", f.vantageName(), "version", Version,
			"targets", len(held.Targets), "targets_version", held.Version)
		agent.PushLoop(ctx, pusher, 0)
		return nil
	}

	srv := &http.Server{
		Addr:              f.listen,
		Handler:           agent.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	held := agent.Targets()
	serveTLS := fileExists(f.tlsCert) && fileExists(f.tlsKey)
	slog.Info("hz-probe listening",
		"addr", f.listen, "vantage", f.vantageName(), "version", Version,
		"tls", serveTLS, "targets", len(held.Targets), "targets_version", held.Version)

	// Plain HTTP is allowed but never silently: it puts the token in cleartext
	// on the public internet, which is the one mistake this program makes easy.
	if !serveTLS {
		slog.Warn("hz-probe: serving plain HTTP; the token will cross the network in cleartext. " +
			"Run 'hz-probe gen-cert', or point --tls-cert/--tls-key at a real certificate.")
		err = srv.ListenAndServe()
	} else {
		err = srv.ListenAndServeTLS(f.tlsCert, f.tlsKey)
	}
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// fileExists reports whether a path is readable as a file.
func fileExists(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// resolveToken reads the token from a file, the environment, or a flag, in
// that order of preference. A file and the environment both keep it off the
// process list, where any local user can read it.
func resolveToken(tokenFlag, tokenFile string) (string, error) {
	if tokenFile != "" {
		b, err := os.ReadFile(tokenFile)
		switch {
		case err == nil:
			if tok := strings.TrimSpace(string(b)); tok != "" {
				return tok, nil
			}
			return "", fmt.Errorf("token file %s is empty", tokenFile)
		case !os.IsNotExist(err):
			return "", fmt.Errorf("could not read token file: %w", err)
		}
		// A missing file at the default path is not an error yet — the
		// environment or a flag may still carry the token.
	}
	if tok := strings.TrimSpace(os.Getenv("HZ_PROBE_TOKEN")); tok != "" {
		return tok, nil
	}
	if tok := strings.TrimSpace(tokenFlag); tok != "" {
		return tok, nil
	}
	return "", fmt.Errorf("no token: run 'hz-probe install' to mint one, set HZ_PROBE_TOKEN, or pass --token")
}
