package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// `homelab-horizon sync` — the console's Sync button.
//
// It asks the RUNNING hz on this box, over loopback, to run its full sync —
// POST /api/v1/services/sync, the same request `hz sync` and the UI make, so
// the same code path (runSyncInternal) and the same single writer. Running the
// sync in this process instead would mean a second hz writing haproxy.cfg and
// the synced baseline beside the live one.
//
// It authenticates with a personal API token read from a file, by default the
// one `user create` writes beside the config (the bootstrap's hz-local-admin,
// /etc/homelab-horizon/admin.token). Root only, like the other console paths.

func newSyncCmd(opts *serveOpts) *cobra.Command {
	var tokenFile string
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Run a full sync on this hz, over loopback (console, root only)",
		Long: "Asks the hz running on this box to run its full sync (dnsmasq, DNS records,\n" +
			"certificates, HAProxy) — the same sync as the UI's Sync button and `hz sync` —\n" +
			"and prints what it does. It talks to hz over loopback with a personal API token\n" +
			"read from --token-file (default: admin.token beside the config, which\n" +
			"`homelab-horizon user create ... --token-file` writes). The token is never\n" +
			"printed. Exits non-zero when the sync ends with errors.",
		Example: "  homelab-horizon sync\n" +
			"  homelab-horizon sync --token-file /etc/homelab-horizon/admin.token",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireRoot("sync"); err != nil {
				return err
			}
			cfg, cfgPath, err := loadConfig(opts.configPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if tokenFile == "" {
				tokenFile = filepath.Join(filepath.Dir(cfgPath), "admin.token")
			}
			raw, err := os.ReadFile(tokenFile)
			if err != nil {
				return fmt.Errorf("read the API token from %s: %w (create one with: homelab-horizon user create <name> --admin --token-file %s)", tokenFile, err, tokenFile)
			}
			token := strings.TrimSpace(string(raw))
			base, err := loopbackBase(cfg.EffectiveListenAddr())
			if err != nil {
				return err
			}
			return runConsoleSync(cmd.OutOrStdout(), &http.Client{Timeout: 30 * time.Second}, base, token, tokenFile, timeout)
		},
	}
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "file holding a personal API token (default: admin.token beside the config)")
	cmd.Flags().DurationVar(&timeout, "timeout", 15*time.Minute, "give up waiting after this long (the sync itself keeps running)")
	return cmd
}

// loopbackBase is the URL this box's hz answers on locally: its listen
// address, with a wildcard host made loopback.
func loopbackBase(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("listen_addr %q is not host:port: %w", listen, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

// syncLine is one message from the sync status history.
type syncLine struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	Done    bool   `json:"done"`
	Status  string `json:"status"`
}

func runConsoleSync(out io.Writer, c *http.Client, base, token, tokenFile string, timeout time.Duration) error {
	do := func(method, path string, v any) error {
		req, err := http.NewRequest(method, base+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := c.Do(req)
		if err != nil {
			return fmt.Errorf("hz is not answering at %s (%w) — is homelab-horizon running? systemctl status homelab-horizon", base, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("hz at %s refused the token in %s (401): revoked, expired, or not a personal API token", base, tokenFile)
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return json.NewDecoder(resp.Body).Decode(v)
	}

	var started apitypes.TriggerSyncResponse
	if err := do(http.MethodPost, "/api/v1/services/sync", &started); err != nil {
		return err
	}
	if started.Started {
		say(out, "Sync started on %s.\n", base)
	} else {
		say(out, "A sync was already running on %s; following it.\n", base)
	}

	printed := 0
	status := ""
	deadline := time.Now().Add(timeout)
	for {
		var st struct {
			Running bool     `json:"running"`
			History []string `json:"history"`
		}
		if err := do(http.MethodGet, "/api/v1/services/sync/status", &st); err != nil {
			return err
		}
		for ; printed < len(st.History); printed++ {
			var l syncLine
			if json.Unmarshal([]byte(st.History[printed]), &l) != nil {
				continue
			}
			switch {
			case l.Done:
				status = l.Status
			case l.Level == "section_end":
			default:
				say(out, "%-8s %s\n", l.Level, l.Message)
			}
		}
		if !st.Running {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the sync is still running after %s; it carries on in hz — see journalctl -u homelab-horizon", timeout)
		}
		time.Sleep(time.Second)
	}
	switch status {
	case "success":
		say(out, "Sync finished: success.\n")
		return nil
	case "":
		return fmt.Errorf("the sync ended without a result line; see journalctl -u homelab-horizon")
	default:
		return fmt.Errorf("the sync finished with errors (status %s); the lines above say which", status)
	}
}
