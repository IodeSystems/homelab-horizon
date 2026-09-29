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
	defaultNtfyFile  = "/etc/hz-probe/ntfy-url"
	defaultNtfyToken = "/etc/hz-probe/ntfy-token"
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

	ntfyURL       string
	ntfyFile      string
	ntfyAfter     int
	ntfyToken     string
	ntfyTokenFile string
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
	fs.StringVar(&f.ntfyURL, "ntfy-url", "", "ntfy topic URL to alert when hz is unreachable (prefer --ntfy-url-file); push mode only — in pull mode the agent never dials hz, so it cannot see hz down")
	fs.StringVar(&f.ntfyFile, "ntfy-url-file", defaultNtfyFile, "file holding the ntfy topic URL; absent means no vantage-side alerting. Push mode only")
	fs.IntVar(&f.ntfyAfter, "ntfy-after", 3, "consecutive failed reports to hz before alerting ntfy. Push mode only")
	fs.StringVar(&f.ntfyToken, "ntfy-token", "", "ntfy access token, sent as a Bearer header (prefer --ntfy-token-file). Push mode only")
	fs.StringVar(&f.ntfyTokenFile, "ntfy-token-file", defaultNtfyToken, "file holding the ntfy access token; absent means ntfy is posted to without one. Push mode only")
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
		fileSet, tokenFileSet := false, false
		fs.Visit(func(fl *flag.Flag) {
			switch fl.Name {
			case "ntfy-url-file":
				fileSet = true
			case "ntfy-token-file":
				tokenFileSet = true
			}
		})
		ntfy, err := resolveNtfyURL(f.ntfyURL, f.ntfyFile, fileSet)
		if err != nil {
			return err
		}
		ntfyToken, err := resolveNtfyToken(f.ntfyToken, f.ntfyTokenFile, tokenFileSet)
		if err != nil {
			return err
		}
		// Precedence: an ntfy URL on THIS host (file, environment, flag) wins
		// over the channel the operator set in hz. Without one, the agent
		// alerts wherever hz last said — cached, so it still works with hz
		// down — and follows hz's changes without a restart.
		if f.ntfyAfter >= 1 {
			pusher.AlertThreshold = f.ntfyAfter
		}
		if ntfy != "" {
			if f.ntfyAfter < 1 {
				return fmt.Errorf("--ntfy-after must be at least 1, got %d", f.ntfyAfter)
			}
			pusher.Alert = &probe.Alerter{URL: ntfy, Token: ntfyToken, Vantage: f.vantageName(), Threshold: f.ntfyAfter}
			// The URL and the token are secrets; log that they are set,
			// never what they are.
			slog.Info("hz-probe will alert ntfy if hz is unreachable or rejects this vantage",
				"source", "host", "after_failures", f.ntfyAfter, "ntfy_token", ntfyToken != "",
				"precedence", "this host's ntfy URL wins over the alert channel set in hz")
		} else {
			if ntfyToken != "" {
				slog.Warn("hz-probe: an ntfy token is set on this host but no ntfy URL; it is ignored — " +
					"a channel set in hz carries its own token")
			}
			slog.Info("hz-probe will alert the ntfy channel set in hz, if any",
				"source", "hz", "cached_channel", agent.AlertChannel() != nil,
				"precedence", "an ntfy URL on this host would win; none is set")
		}
		held := agent.Targets()
		slog.Info("hz-probe reporting to hz",
			"hz", f.pushTo, "vantage", f.vantageName(), "version", Version,
			"targets", len(held.Targets), "targets_version", held.Version)
		agent.PushLoop(ctx, pusher, 0)
		return nil
	}

	if strings.TrimSpace(f.ntfyURL) != "" || os.Getenv("HZ_PROBE_NTFY_URL") != "" {
		slog.Warn("hz-probe: an ntfy URL is set but this agent is in pull mode, where it never dials hz " +
			"and cannot tell hz is down; it is ignored")
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

// resolveNtfyURL reads the optional ntfy topic URL the way resolveToken reads
// the token: file, then HZ_PROBE_NTFY_URL, then the flag. Unlike the token,
// finding none is not an error — it is the feature switched off.
//
// A file somebody NAMED (`fileSet`) that is empty or unreadable IS an error:
// that is an intent gone wrong, and silently running without the alert they
// asked for is the failure this feature exists to prevent.
//
// THE DEFAULT PATH IS NOT AN INTENT, and must never stop the agent. Found in
// production 2026-09-28: a vantage installed before this feature updated
// itself, its old unit passed no --ntfy-url-file, and the default
// /etc/hz-probe/ntfy-url sits in a root-only directory — so the DynamicUser
// got EACCES, not ENOENT, the agent exited, and it crash-looped 16,446 times.
// An old unit plus a new binary is exactly what self-update produces.
func resolveNtfyURL(urlFlag, file string, fileSet bool) (string, error) {
	raw, err := resolveOptional("ntfy URL", "vantage-side alerting is off",
		urlFlag, "HZ_PROBE_NTFY_URL", file, fileSet)
	if err != nil || raw == "" {
		return "", err
	}
	if !probe.ValidAlertURL(raw) {
		// Not echoing the value: it is a secret.
		return "", fmt.Errorf("the ntfy URL is not an http(s) URL with a host")
	}
	return raw, nil
}

// resolveNtfyToken reads the optional ntfy access token by exactly the rules
// of resolveNtfyURL: --ntfy-token-file, then HZ_PROBE_NTFY_TOKEN, then
// --ntfy-token. None is not an error — the topic is posted to without
// authentication, as before tokens existed. The same 2026-09-28 rule
// applies: an old unit on a new binary names no --ntfy-token-file, so an
// unreadable DEFAULT path must warn and carry on, never stop the agent.
func resolveNtfyToken(tokenFlag, file string, fileSet bool) (string, error) {
	return resolveOptional("ntfy token", "ntfy is posted to without a token",
		tokenFlag, "HZ_PROBE_NTFY_TOKEN", file, fileSet)
}

// resolveOptional is the shared file → environment → flag resolution for an
// optional secret. `what` names it in errors and `off` says in the warning
// what running without it means. The value itself is never in a message.
func resolveOptional(what, off, flagVal, env, file string, fileSet bool) (string, error) {
	raw := ""
	if file != "" {
		b, err := os.ReadFile(file)
		switch {
		case err == nil:
			raw = strings.TrimSpace(string(b))
			if raw == "" && fileSet {
				return "", fmt.Errorf("%s file %s is empty", what, file)
			}
		case os.IsNotExist(err):
		case fileSet:
			return "", fmt.Errorf("could not read %s file: %w", what, err)
		default:
			slog.Warn("probe: the default "+what+" file cannot be read; "+off,
				"path", file, "error", err)
		}
	}
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(env))
	}
	if raw == "" {
		raw = strings.TrimSpace(flagVal)
	}
	return raw, nil
}
