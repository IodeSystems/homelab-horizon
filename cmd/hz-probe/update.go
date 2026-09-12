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
// So the privileged half is a separate root timer that pulls and re-hoists:
// it asks hz what build it holds, downloads it if that differs from what is
// installed, checks the new binary actually runs, swaps it in and restarts
// the service. hz is only ever a source of bytes over a verified TLS
// connection, never an instruction.

// updateTimeout bounds the whole operation, download included.
const updateTimeout = 3 * time.Minute

func runUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	var f serveFlags
	f.register(fs)
	dryRun := fs.Bool("dry-run", false, "report what would change, do nothing")
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

	// The agent's own token authorises the download. An install grant would
	// have expired long before a nightly timer ran.
	client := &http.Client{Timeout: updateTimeout}
	base := strings.TrimSuffix(f.pushTo, "/")

	want, err := remoteAgentVersion(client, base, tok)
	if err != nil {
		return fmt.Errorf("could not ask hz what build it holds: %w", err)
	}
	if want == "" {
		return fmt.Errorf("hz did not report an agent version; nothing to compare against")
	}

	have := strings.TrimSpace(Version)
	if want == have {
		fmt.Printf("hz-probe %s is current.\n", have)
		return nil
	}
	fmt.Printf("hz-probe %s installed, hz holds %s.\n", have, want)
	if *dryRun {
		fmt.Println("DRY RUN: would download, verify and restart.")
		return nil
	}

	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root to replace the binary and restart the service")
	}

	exe := execPath()
	tmp := exe + ".new"
	if err := downloadAgent(client, base, tok, tmp); err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()

	// Never swap in something that will not run. A binary that fails here
	// leaves the working one in place; the alternative is a vantage that
	// stops reporting and looks like a network problem.
	got, err := exec.Command(tmp, "version").Output()
	if err != nil {
		return fmt.Errorf("the downloaded binary does not run, keeping the current one: %w", err)
	}
	fmt.Printf("downloaded: %s", got)

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
	fmt.Println("hz-probe restarted and active.")
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
