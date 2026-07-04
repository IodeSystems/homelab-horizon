// Command hz is an operator CLI for a homelab-horizon instance. Unlike the
// per-service hz-client (bash, service-token scoped, served from the admin UI),
// hz authenticates with the instance ADMIN token and drives whole-instance
// service management: list/show/create/edit/delete, global sync, and an
// interactive setup questionnaire.
//
// Config: ~/.hz_config (JSON {"host","token"}), overridable by HZ_HOST/HZ_TOKEN
// env or --host/--token flags. Request schemas are dumped straight from the
// shared internal/apitypes structs (`hz schema service`), so they never drift.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

var (
	Version   = "dev"
	BuildTime = "unknown"
)

const usage = `hz - operator CLI for homelab-horizon (admin-token scoped)

USAGE
  hz [--host URL] [--token TOK] <command> [args]

CONFIG
  Resolved in order (later wins): ~/.hz_config -> env -> flags.
  ~/.hz_config   JSON: {"host": "http://192.168.1.89:8080", "token": "<admin-token>"}
  HZ_HOST HZ_TOKEN   env overrides
  --host --token     flag overrides
  HZ_CONFIG          alternate config path

COMMANDS
  service list                       List services (table)
  service show <name> [--json]       Show one service
  service create [flags]             Create a service (see 'hz schema service')
  service edit <name> [flags]        Edit a service; only flags you pass change
  service delete <name>              Delete a service
  setup                              Interactive questionnaire -> create + sync
  sync [--wait]                      Trigger a global sync (--wait: block til done)
  pending                            Show unsynced config changes
  ports next --host IP [--count N] [--from PORT]
                                     Find the next free contiguous port range on a host
  schema [service]                   Dump the JSON request schema
  version                            Print version

SERVICE FLAGS (create/edit)
  --name NAME               service name (create: required; edit: rename)
  --domain D                domain (repeatable) or --domains a,b,c
  --backend HOST:PORT       proxy backend
  --static-root DIR         serve a static folder instead of a backend
  --self                    route to this hz instance's own admin UI
  --spa                     static: serve index.html for unknown paths
  --internal-only           not publicly reachable (default public)
  --public                  publicly reachable (clears --internal-only on edit)
  --health-check PATH       backend health-check path
  --deploy-next-backend HP  enable blue-green: standby backend host:port
  --balance MODE            first | roundrobin
  --internal-dns-ip IP      publish an internal (dnsmasq) A record
  --external-dns-ip IP      external A record (repeatable); --ttl SEC
  --timeout-connect/-server/-tunnel SEC   HAProxy timeout overrides
  --sync                    trigger a global sync after the mutation

EXAMPLES
  hz service list
  hz setup
  hz service create --name ebb --domain ebb.iodesystems.com \
    --backend 192.168.1.76:8300 --internal-only --health-check /healthz --sync
  hz sync --wait
  hz schema service
`

func main() {
	args := os.Args[1:]

	// Split leading global flags (--host/--token/--help/--version) from the command.
	cfgHost, cfgToken := "", ""
	var cmd string
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--help" || a == "-h":
			fmt.Print(usage)
			return
		case a == "--version":
			cmd = "version"
			i = len(args)
		case a == "--host":
			i++
			if i < len(args) {
				cfgHost = args[i]
			}
		case a == "--token":
			i++
			if i < len(args) {
				cfgToken = args[i]
			}
		case strings.HasPrefix(a, "--host="):
			cfgHost = strings.TrimPrefix(a, "--host=")
		case strings.HasPrefix(a, "--token="):
			cfgToken = strings.TrimPrefix(a, "--token=")
		default:
			cmd = a
			rest = args[i+1:]
			i = len(args)
		}
	}

	if cmd == "" {
		fmt.Print(usage)
		return
	}
	if cmd == "version" {
		fmt.Printf("hz %s (built %s)\n", Version, BuildTime)
		return
	}
	if cmd == "schema" {
		if err := runSchema(rest); err != nil {
			fatal(err)
		}
		return
	}

	// Config is resolved lazily on first request, so flag parsing and --help on
	// any subcommand work without a config present.
	c := newClient(cfgHost, cfgToken)

	var err error
	switch cmd {
	case "service":
		err = runService(c, rest)
	case "setup":
		err = runSetup(c, rest)
	case "sync":
		err = runSync(c, rest)
	case "pending":
		err = runPending(c, rest)
	case "ports":
		err = runPorts(c, rest)
	default:
		err = fmt.Errorf("unknown command: %s\nRun 'hz --help'", cmd)
	}
	if errors.Is(err, flag.ErrHelp) {
		return // flag already printed usage to stderr
	}
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error: "+err.Error())
	os.Exit(1)
}

// --- config ---

type config struct {
	Host  string `json:"host"`
	Token string `json:"token"`
}

func loadConfig(flagHost, flagToken string) (config, error) {
	var cfg config

	path := os.Getenv("HZ_CONFIG")
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".hz_config")
	}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return cfg, fmt.Errorf("parsing %s: %w", path, err)
		}
	}

	if v := os.Getenv("HZ_HOST"); v != "" {
		cfg.Host = v
	}
	if v := os.Getenv("HZ_TOKEN"); v != "" {
		cfg.Token = v
	}
	if flagHost != "" {
		cfg.Host = flagHost
	}
	if flagToken != "" {
		cfg.Token = flagToken
	}

	cfg.Host = strings.TrimRight(cfg.Host, "/")
	if cfg.Host == "" {
		return cfg, fmt.Errorf("no host: set \"host\" in ~/.hz_config, HZ_HOST, or --host")
	}
	if cfg.Token == "" {
		return cfg, fmt.Errorf("no token: set \"token\" in ~/.hz_config, HZ_TOKEN, or --token")
	}
	return cfg, nil
}

// --- http client with admin-token login ---

type client struct {
	flagHost  string
	flagToken string
	host      string
	token     string
	http      *http.Client
	loggedn   bool
}

func newClient(flagHost, flagToken string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{
		flagHost:  flagHost,
		flagToken: flagToken,
		http:      &http.Client{Timeout: 30 * time.Second, Jar: jar},
	}
}

// login resolves config (lazily, so offline flag parsing/--help works) and
// exchanges the admin token for a session cookie (stored in the jar).
func (c *client) login() error {
	if c.loggedn {
		return nil
	}
	if c.host == "" {
		cfg, err := loadConfig(c.flagHost, c.flagToken)
		if err != nil {
			return err
		}
		c.host, c.token = cfg.Host, cfg.Token
	}
	body, _ := json.Marshal(apitypes.LoginRequest{Token: c.token})
	resp, err := c.http.Post(c.host+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("login failed (%d): %s", resp.StatusCode, apiError(raw))
	}
	var lr apitypes.LoginResponse
	if err := json.Unmarshal(raw, &lr); err != nil || !lr.OK {
		return fmt.Errorf("login rejected: token is not the admin token")
	}
	if lr.Invite {
		return fmt.Errorf("token is an invite, not the admin token")
	}
	c.loggedn = true
	return nil
}

// do issues an authenticated request. body may be nil. If out is non-nil the
// JSON response is decoded into it.
func (c *client) do(method, path string, body, out interface{}) error {
	if err := c.login(); err != nil {
		return err
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.host+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s -> %d: %s", method, path, resp.StatusCode, apiError(raw))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decoding %s response: %w", path, err)
		}
	}
	return nil
}

// apiError extracts the {"error":"..."} message servers return, falling back to
// the raw body.
func apiError(raw []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		return e.Error
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return "(empty response)"
	}
	return s
}
