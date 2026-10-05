package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// The client's private key, as the fake hz returns it inside the config.
const vpnSecret = "PRIVATE-KEY-SENTINEL-do-not-print-anywhere-but-the-terminal="

// fakeVPNHZ is the running hz `vpn add` talks to over loopback: it records each
// request and answers the add with a config that holds vpnSecret.
type fakeVPNHZ struct {
	mu    sync.Mutex
	calls []string
	auth  []string
	added apitypes.PeerAddReq
}

func newVPNCLI(t *testing.T) (*tokenCLI, *fakeVPNHZ, string) {
	t.Helper()
	f := &fakeVPNHZ{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.URL.Path)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/api/v1/vpn/peers/add":
			_ = json.NewDecoder(r.Body).Decode(&f.added)
			_ = json.NewEncoder(w).Encode(apitypes.AddPeerResponse{OK: true, PublicKey: "pub-of-carl-1",
				Config: "[Interface]\nPrivateKey = " + vpnSecret + "\nAddress = 10.101.0.2/24\n"})
		case "/api/v1/vpn/peers/toggle-admin":
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c := newTokenCLI(t)
	cfg, err := config.Load(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ListenAddr = strings.TrimPrefix(srv.URL, "http://")
	if err := config.Save(c.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	tok := filepath.Join(filepath.Dir(c.configPath), "admin.token")
	if err := os.WriteFile(tok, []byte("hzu_local-admin-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return c, f, tok
}

func asTerminal(t *testing.T, yes bool) {
	t.Helper()
	prev, prevQR := stdoutIsTerminal, renderQR
	stdoutIsTerminal = func() bool { return yes }
	renderQR = func(s string) (string, error) { return "[QR of " + itoa(len(s)) + " bytes]\n", nil }
	t.Cleanup(func() { stdoutIsTerminal, renderQR = prev, prevQR })
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// The first client: created by the RUNNING hz (one writer), with the local
// admin's token; printed once, to the terminal only; attributed to --user.
func TestVPNAddCreatesPrintsOnceAndAttributes(t *testing.T) {
	asRoot(t, 0)
	asTerminal(t, true)
	c, f, _ := newVPNCLI(t)
	carl := c.user("carl")

	stdout, stderr, err := c.run("", "vpn", "add", "carl-1", "--profile", "vpn-only", "--vpn-admin", "--user", "carl")
	if err != nil {
		t.Fatalf("vpn add: %v\n%s", err, stderr)
	}
	if strings.Join(f.calls, " ") != "/api/v1/vpn/peers/add /api/v1/vpn/peers/toggle-admin" {
		t.Fatalf("requests to hz: %v", f.calls)
	}
	for _, a := range f.auth {
		if a != "Bearer hzu_local-admin-token" {
			t.Fatalf("authorization %q", a)
		}
	}
	if f.added.Name != "carl-1" || f.added.Profile != "vpn-only" {
		t.Fatalf("the add asked for %+v", f.added)
	}
	if !strings.Contains(stdout, vpnSecret) || !strings.Contains(stdout, "[QR of ") {
		t.Fatalf("the config and QR are not on the terminal:\n%s", stdout)
	}
	if strings.Contains(stderr, vpnSecret) {
		t.Fatal("the private key is on stderr")
	}
	if !strings.Contains(stderr, "attributed to carl") || !strings.Contains(stderr, "shown once") {
		t.Fatalf("stderr: %s", stderr)
	}
	owner, err := c.store.PeerOwner(context.Background(), "carl-1")
	if err != nil || owner != carl.ID {
		t.Fatalf("owner of carl-1: %q %v, want carl (%s)", owner, err, carl.ID)
	}
}

// Every refusal comes before hz is asked for anything, and prints no secret.
func TestVPNAddRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		uid      int
		terminal bool
		args     []string
		want     string
	}{
		"not root":       {1000, true, []string{"--profile", "vpn-only", "--user", "carl"}, "must run as root"},
		"not a terminal": {0, false, []string{"--profile", "vpn-only", "--user", "carl"}, "not a terminal"},
		"an upstream":    {0, true, []string{"--profile", "upstream", "--user", "carl"}, "made by the bootstrap"},
		"a bad profile":  {0, true, []string{"--profile", "admin", "--user", "carl"}, "is not one of"},
		"no such user":   {0, true, []string{"--profile", "vpn-only", "--user", "dave"}, "user create dave"},
		"no profile":     {0, true, []string{"--user", "carl"}, `"profile" not set`},
		"no user":        {0, true, []string{"--profile", "vpn-only"}, `"user" not set`},
	} {
		t.Run(name, func(t *testing.T) {
			asRoot(t, tc.uid)
			asTerminal(t, tc.terminal)
			c, f, _ := newVPNCLI(t)
			c.user("carl")
			stdout, stderr, err := c.run("", append([]string{"vpn", "add", "carl-1"}, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%v, want %q", err, tc.want)
			}
			if len(f.calls) != 0 {
				t.Fatalf("hz was asked %v before the refusal", f.calls)
			}
			if strings.Contains(stdout+stderr, vpnSecret) || stdout != "" {
				t.Fatalf("output on a refusal: stdout %q stderr %q", stdout, stderr)
			}
		})
	}
}

// Instrument: the terminal check is the real one — a pipe is not a terminal.
func TestStdoutIsTerminalIsFalseForAPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	prev := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = prev }()
	if stdoutIsTerminal() {
		t.Fatal("a pipe reads as a terminal")
	}
}
