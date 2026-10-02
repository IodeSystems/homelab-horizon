package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Self-update, done by something with privilege rather than by the agent.
//
// The agent runs as a DynamicUser: it cannot write /usr/local/bin and cannot
// restart its own unit, which is deliberate. An agent that fetched and
// executed what hz sent it would turn a compromised hz into code execution
// on a host outside the network, and the whole design otherwise keeps the
// agent unable to be told what to do.
//
// So the privileged half is a separate root unit that pulls and re-hoists:
// it asks hz what build it holds, downloads it if that differs from what is
// installed, checks the new binary actually runs and is the version hz
// named, swaps it in and restarts the service. hz is only ever a source of
// bytes over a verified TLS connection, never an instruction.
//
// TWO THINGS START IT, and both run the same unit with the same fixed
// command line:
//
//   - hz-probe-update.timer, daily. The fallback.
//   - hz-probe-update.path, when the agent raises update-wanted. The agent
//     learns on every report which build hz holds; once it has seen the same
//     differing build for probe.UpdateSettle (15 minutes) it writes the flag
//     in its own StateDirectory. A deploy rolled back inside 15 minutes
//     therefore never reaches the vantage. (Operator decision, 2026-10-02.)
//
// THE FLAG IS A DOORBELL, NOT AN INSTRUCTION. It holds a timestamp, which
// this side never reads: only its existence matters. The version, the bytes
// and the command all come from here — hz asked over TLS with this host's
// token, the download checked to run and to be the version hz named, the
// command fixed in a root-owned unit. The agent can make the updater run
// sooner; it cannot choose what it does. The flag is removed with an unlink,
// which does not follow a symlink, on every run that is not a dry run —
// success, no-op or failure — so the path unit cannot loop.
//
// AT MOST ONE ATTEMPT AN HOUR (updateRateLimit), from a timestamp in a
// root-owned directory the agent cannot write (defaultUpdateStampPath). A run
// inside the hour does nothing and exits 0. An attempt is recorded before the
// download, so a failure — including hz serving a build that is not the
// version it named — is not retried until the hour is up.
//
// THE TRADE-OFF: a vantage now lags a deploy by 15 minutes, plus up to an
// hour when an attempt ran recently, instead of up to a day (plus the
// timer's RandomizedDelaySec). The cost: a compromised agent can make the
// updater ask hz once an hour instead of once a day — and nothing more, since
// what it would install is still hz's build.

// updateTimeout bounds the whole operation, download included.
const updateTimeout = 3 * time.Minute

// updateRateLimit is the least time between two update attempts.
const updateRateLimit = time.Hour

// defaultUpdateStampPath records the last attempt. Root-owned and outside
// the agent's StateDirectory: the agent must not be able to reset its own
// rate limit.
const defaultUpdateStampPath = "/var/lib/hz-probe-update/last-attempt"

func runUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	var f serveFlags
	f.register(fs)
	dryRun := fs.Bool("dry-run", false, "report what would change, do nothing")
	stamp := fs.String("stamp", defaultUpdateStampPath, "root-owned file recording the last update attempt, for the hourly limit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !f.pushMode() {
		return fmt.Errorf("--push-to is required: update asks that hz for the build it holds")
	}

	tok, err := resolveToken(f.token, f.tokenFile)
	if err != nil {
		return err
	}

	root := os.Geteuid() == 0
	if root && !*dryRun {
		refreshUpdateUnits(&f)
	}

	// The agent's own token authorises the download. An install grant would
	// have expired long before a nightly timer ran.
	client := &http.Client{Timeout: updateTimeout}
	base := strings.TrimSuffix(f.pushTo, "/")
	exe := execPath()

	u := &updater{
		have:      strings.TrimSpace(Version),
		flagPath:  f.updateFlagPath(),
		stampPath: *stamp,
		tmpPath:   exe + ".new",
		dryRun:    *dryRun,
		root:      root,
		now:       time.Now,
		out:       os.Stdout,
		ask:       func() (string, error) { return remoteAgentVersion(client, base, tok) },
		fetch:     func(path string) error { return downloadAgent(client, base, tok, path) },
		versionOf: binaryVersion,
		install:   func(tmp string) error { return swapAndRestart(tmp, exe) },
	}
	return u.run()
}

// refreshUpdateUnits rewrites the updater's own units when this install has
// them, so a vantage installed before a unit existed gains it through an
// ordinary update run — the run AFTER the one that installed this build,
// since that one was still the old binary. "Has them" is the timer file being
// there: a --no-auto-update or pull install has none and gets none. Failures
// are warnings; the update itself does not depend on them.
func refreshUpdateUnits(f *serveFlags) {
	if !fileExists(updateTimerPath) {
		return
	}
	changed, err := writeUpdateUnits(updateUnits(f, execPath(), false))
	if err != nil {
		fmt.Printf("Warning: could not refresh the update units: %v\n", err)
		return
	}
	if !changed {
		return
	}
	if err := run("systemctl", "daemon-reload"); err != nil {
		fmt.Printf("Warning: %v\n", err)
		return
	}
	if err := run("systemctl", "enable", "--now", "hz-probe-update.path"); err != nil {
		fmt.Printf("Warning: could not enable update on report: %v\n", err)
		return
	}
	fmt.Println("Refreshed the update units; update on report enabled (hz-probe-update.path).")
}

// updater is one update run, its effects injected so the decisions — rate
// limit, flag removal, refusing a build that is not what hz named — are
// tested without a network, root or systemd.
type updater struct {
	have      string // the installed version
	flagPath  string // the agent's update-wanted; "" = none
	stampPath string // the last attempt, for the rate limit
	tmpPath   string // where the download lands
	dryRun    bool
	root      bool
	now       func() time.Time
	out       io.Writer

	ask       func() (string, error)            // the version hz holds
	fetch     func(path string) error           // download it to path
	versionOf func(path string) (string, error) // run it, read its version
	install   func(tmp string) error            // swap it in and restart
}

func (u *updater) printf(format string, a ...any) { _, _ = fmt.Fprintf(u.out, format, a...) }

func (u *updater) run() error {
	if !u.dryRun {
		// Whatever happens below. A flag left behind would retrigger the
		// path unit as soon as this run ends.
		defer u.clearFlag()
	}

	now := u.now()
	if !u.dryRun {
		last, err := readStamp(u.stampPath)
		if err != nil {
			// Unreadable is not missing (CLAUDE.md #2): reading it as "no
			// attempt yet" would let a broken stamp lift the limit for good.
			return fmt.Errorf("LOUD: cannot read the update rate-limit stamp %s, doing nothing: %w", u.stampPath, err)
		}
		switch {
		case last.IsZero():
		case last.After(now):
			// A stamp from the future is a clock that was wrong once.
			// Obeying it would block updates for as long as the error was.
			u.printf("The last update attempt is stamped %s, after now (%s); ignoring it.\n",
				last.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
		case now.Sub(last) < updateRateLimit:
			u.printf("Last update attempt at %s, within the hourly limit; nothing done.\n",
				last.UTC().Format(time.RFC3339))
			return nil
		}
	}

	want, err := u.ask()
	if err != nil {
		return fmt.Errorf("could not ask hz what build it holds: %w", err)
	}
	if want == "" {
		return fmt.Errorf("hz did not report an agent version; nothing to compare against")
	}
	if want == u.have {
		u.printf("hz-probe %s is current.\n", u.have)
		return nil
	}
	u.printf("hz-probe %s installed, hz holds %s.\n", u.have, want)
	if u.dryRun {
		u.printf("DRY RUN: would download, verify and restart.\n")
		return nil
	}

	if !u.root {
		return fmt.Errorf("must run as root to replace the binary and restart the service")
	}

	// Recorded before the download: an attempt that fails still counts, so
	// a failing one is retried hourly at most, not on every flag.
	if err := writeStamp(u.stampPath, now); err != nil {
		return fmt.Errorf("could not record the attempt for the rate limit, doing nothing: %w", err)
	}

	if err := u.fetch(u.tmpPath); err != nil {
		return err
	}
	defer func() { _ = os.Remove(u.tmpPath) }()

	// Never swap in something that will not run. A binary that fails here
	// leaves the working one in place; the alternative is a vantage that
	// stops reporting and looks like a network problem.
	got, err := u.versionOf(u.tmpPath)
	if err != nil {
		return fmt.Errorf("the downloaded binary does not run, keeping the current one: %w", err)
	}
	u.printf("downloaded: hz-probe %s\n", got)

	// hz named one build and served another. Installing it would trade a
	// known build for an unknown one, and trying again at once would fetch
	// the same thing; the stamp holds the next attempt off for the hour.
	if got != want {
		return fmt.Errorf("LOUD: hz said it holds %s but served %s; keeping %s, next attempt after %s",
			want, got, u.have, now.Add(updateRateLimit).UTC().Format(time.RFC3339))
	}

	if err := u.install(u.tmpPath); err != nil {
		return err
	}
	u.printf("hz-probe updated to %s, restarted and active.\n", got)
	return nil
}

// clearFlag removes update-wanted. os.Remove unlinks; it does not follow a
// symlink the agent might have put there.
func (u *updater) clearFlag() {
	if u.flagPath == "" {
		return
	}
	if err := os.Remove(u.flagPath); err != nil && !os.IsNotExist(err) {
		u.printf("Warning: could not remove %s: %v\n", u.flagPath, err)
	}
}

// readStamp reads the last attempt. Missing is the zero time and no error;
// unreadable or unparseable is an error.
func readStamp(path string) (time.Time, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
}

// writeStamp records an attempt, atomically, in a root-only directory.
func writeStamp(path string, t time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(t.UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// binaryVersion runs a binary's "version" command and returns the version
// it reports.
func binaryVersion(path string) (string, error) {
	out, err := exec.Command(path, "version").Output()
	if err != nil {
		return "", err
	}
	return parseVersionLine(string(out))
}

// parseVersionLine reads "hz-probe <version> (built <time>)".
func parseVersionLine(s string) (string, error) {
	fields := strings.Fields(s)
	if len(fields) < 2 || fields[0] != "hz-probe" {
		return "", fmt.Errorf("unexpected version output %q", strings.TrimSpace(s))
	}
	return fields[1], nil
}

// swapAndRestart moves the checked binary over exe and restarts the agent.
func swapAndRestart(tmp, exe string) error {
	if err := os.Rename(tmp, exe); err != nil {
		return fmt.Errorf("replacing %s: %w", exe, err)
	}
	fmt.Printf("replaced %s\n", exe)

	if err := run("systemctl", "restart", "hz-probe"); err != nil {
		return fmt.Errorf("restart failed after replacing the binary: %w", err)
	}
	if state := serviceState(); state != "active" && !strings.HasPrefix(state, "unknown") {
		return fmt.Errorf("hz-probe is %s after the update; check journalctl -u hz-probe", state)
	}
	return nil
}

// remoteAgentVersion asks hz which build it holds, via a report carrying no
// results. Reusing the report endpoint keeps this on one authenticated path
// rather than adding a second.
func remoteAgentVersion(c *http.Client, base, token string) (string, error) {
	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/probe/report",
		strings.NewReader(`{"vantage":"","results":[]}`))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return "", fmt.Errorf("hz returned %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out struct {
		AgentVersion string `json:"agent_version"`
	}
	if err := readJSON(resp.Body, &out); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.AgentVersion), nil
}

// downloadAgent fetches the build for this platform to path.
func downloadAgent(c *http.Client, base, token, path string) error {
	key, err := platformKey()
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet, base+"/admin/hz-probe/bin/"+key, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("download returned %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
