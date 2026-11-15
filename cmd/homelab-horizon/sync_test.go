package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// fakeHZ answers the two sync endpoints the way hz does, for one token, and
// records what was asked.
func fakeHZ(t *testing.T, token, status string) (*httptest.Server, *[]string) {
	t.Helper()
	var asked []string
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/services/sync":
			_, _ = w.Write([]byte(`{"ok":true,"started":true}`))
		case "GET /api/v1/services/sync/status":
			polls++
			hist := []string{`{"level":"step","message":"Syncing HAProxy..."}`, `{"level":"success","message":"  Wrote HAProxy config"}`}
			running := polls < 2
			if !running {
				hist = append(hist, `{"done":true,"status":"`+status+`"}`)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"running": running, "history": hist})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

// syncCLI is a config whose listen address is the fake hz, and the token
// file beside it where `user create` would have written it.
func syncCLI(t *testing.T, srv *httptest.Server, token string) *tokenCLI {
	t.Helper()
	c := newTokenCLI(t)
	cfg, err := config.Load(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ListenAddr = strings.TrimPrefix(srv.URL, "http://")
	if err := config.Save(c.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(c.configPath), "admin.token"), []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return c
}

// `homelab-horizon sync` is the running hz's own sync, asked over loopback
// with the token beside the config: it triggers, follows the lines to the end,
// and never prints the token.
func TestConsoleSyncTriggersAndFollowsTheRunningHZ(t *testing.T) {
	asRoot(t, 0)
	const token = "hz_pat_test-console-sync-token"
	srv, asked := fakeHZ(t, token, "success")
	c := syncCLI(t, srv, token)

	stdout, stderr, err := c.run("", "sync")
	if err != nil {
		t.Fatalf("sync: %v\n%s", err, stderr)
	}
	if len(*asked) < 3 || (*asked)[0] != "POST /api/v1/services/sync" {
		t.Fatalf("requests: %v", *asked)
	}
	for _, want := range []string{"Sync started", "Wrote HAProxy config", "success"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Count(stdout, "Wrote HAProxy config") != 1 {
		t.Errorf("a history line printed twice:\n%s", stdout)
	}
	if strings.Contains(stdout+stderr, token) {
		t.Fatal("the token was printed")
	}
}

// A sync that ends with errors is a non-zero exit; a refused token says which
// file; not root is refused before anything is asked.
func TestConsoleSyncFailures(t *testing.T) {
	const token = "hz_pat_test-console-sync-token"
	asRoot(t, 0)
	srv, _ := fakeHZ(t, token, "failed")
	if _, _, err := syncCLI(t, srv, token).run("", "sync"); err == nil || !strings.Contains(err.Error(), "errors") {
		t.Fatalf("a failed sync: %v", err)
	}

	srv2, _ := fakeHZ(t, "hz_pat_other", "success")
	if _, _, err := syncCLI(t, srv2, token).run("", "sync"); err == nil || !strings.Contains(err.Error(), "admin.token") {
		t.Fatalf("a refused token: %v", err)
	}

	asRoot(t, 1000)
	srv3, asked := fakeHZ(t, token, "success")
	if _, _, err := syncCLI(t, srv3, token).run("", "sync"); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("non-root: %v", err)
	}
	if len(*asked) != 0 {
		t.Fatalf("non-root still asked hz: %v", *asked)
	}
}

func TestLoopbackBase(t *testing.T) {
	for in, want := range map[string]string{
		":8080": "http://127.0.0.1:8080", "0.0.0.0:8080": "http://127.0.0.1:8080", "[::]:8080": "http://127.0.0.1:8080",
		"127.0.0.1:8080": "http://127.0.0.1:8080", "10.100.0.5:9000": "http://10.100.0.5:9000",
	} {
		if got, err := loopbackBase(in); err != nil || got != want {
			t.Errorf("%s: %s (%v), want %s", in, got, err, want)
		}
	}
}
