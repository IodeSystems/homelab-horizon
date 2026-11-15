package server

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests drive the static file server the way the agent-managed unit will:
// a real process entry point, a real listener, a real GET over TCP.
//
// The handler tests beside them (static_test.go) prove the confinement rules;
// these prove the roots are actually SERVED — which is the half a green suite
// would otherwise keep proving after somebody deleted the thing that binds.

// freeLoopbackAddr returns a loopback address nothing is listening on.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// writeSiteMap writes a host->site map where the unit's env var would point.
func writeSiteMap(t *testing.T, sites map[string]staticSite) string {
	t.Helper()
	b, err := json.Marshal(sites)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "static-sites.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write map: %v", err)
	}
	return path
}

// startChild runs the server in the background and waits for it to answer.
// It returns the base URL. Errors from the run are reported on t.
func startChild(t *testing.T, addr, sitesPath string) string {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- RunStaticServerChild(addr, sitesPath) }()
	t.Cleanup(func() {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("static server returned: %v", err)
			}
		default:
			// Still serving; the test process exits and takes it with it.
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return "http://" + addr
		}
		select {
		case err := <-done:
			t.Fatalf("static server exited before it listened: %v", err)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("static server never listened on %s", addr)
	return ""
}

func getSite(t *testing.T, url, host string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s (Host %s): %v", url, host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// THE EVIDENCE THIS WHOLE CHANGE RESTS ON: a document root declared in a map
// file is served over a real socket by the entry point the unit runs. Nothing
// forks; the map arrives as a file.
func TestTheUnitEntryPointServesTheRootsItIsGivenAMapFor(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<h1>home</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file OUTSIDE the root, for the traversal attempt to aim at.
	outside := filepath.Join(filepath.Dir(root), "outside-secret")
	if err := os.WriteFile(outside, []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}

	sites := writeSiteMap(t, map[string]staticSite{
		"site.example.com": {Root: root, SPA: true},
	})
	base := startChild(t, freeLoopbackAddr(t), sites)

	t.Run("a file is served", func(t *testing.T) {
		code, body := getSite(t, base+"/assets/app.js", "site.example.com")
		if code != http.StatusOK || body != "console.log(1)" {
			t.Fatalf("got %d %q, want 200 and the file's bytes", code, body)
		}
	})

	t.Run("the SPA fallback boots the app", func(t *testing.T) {
		code, body := getSite(t, base+"/some/client/route", "site.example.com")
		if code != http.StatusOK || !strings.Contains(body, "home") {
			t.Fatalf("got %d %q, want index.html for a client-side route", code, body)
		}
	})

	t.Run("a traversal attempt gets nothing", func(t *testing.T) {
		for _, p := range []string{
			"/../outside-secret",
			"/assets/../../outside-secret",
			"/%2e%2e/outside-secret",
		} {
			_, body := getSite(t, base+p, "site.example.com")
			if strings.Contains(body, "SECRET") {
				t.Fatalf("%s escaped the document root", p)
			}
		}
	})

	// The one that exercises os.Root rather than path cleaning: a symlink
	// inside the root pointing out of it. Cleaning the URL cannot catch this —
	// the path is legal, the target is not — so this is the confinement the
	// package doc calls the isolation boundary, checked over a real socket.
	t.Run("a symlink out of the root is not followed", func(t *testing.T) {
		if err := os.Symlink(outside, filepath.Join(root, "escape.txt")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		code, body := getSite(t, base+"/escape.txt", "site.example.com")
		if strings.Contains(body, "SECRET") {
			t.Fatalf("a symlink escaped the document root (status %d)", code)
		}
	})

	t.Run("an unclaimed host is not served", func(t *testing.T) {
		if code, _ := getSite(t, base+"/index.html", "other.example.com"); code != http.StatusNotFound {
			t.Fatalf("got %d for a host no site claims, want 404", code)
		}
	})
}

// EMPTY IS AN ANSWER. hz declaring that this machine serves no static sites is
// a map with no entries, and the server must come up and 404 — not refuse.
func TestAnEmptySiteMapServes404sRatherThanRefusingToStart(t *testing.T) {
	sites := writeSiteMap(t, map[string]staticSite{})
	base := startChild(t, freeLoopbackAddr(t), sites)
	if code, _ := getSite(t, base+"/", "site.example.com"); code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 from a server hz gave no sites", code)
	}
}

// UNREADABLE IS NOT EMPTY. A map that cannot be read is an outage dressed as
// an empty fleet if it is served; the unit must fail instead, so
// Restart=on-failure keeps trying and `systemctl status` says so.
func TestAnUnreadableSiteMapRefusesToServe(t *testing.T) {
	addr := freeLoopbackAddr(t)
	cases := map[string]func(t *testing.T) string{
		"missing": func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "never-written.json")
		},
		"not JSON": func(t *testing.T) string {
			p := filepath.Join(t.TempDir(), "sites.json")
			if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
				t.Fatal(err)
			}
			return p
		},
		"JSON null": func(t *testing.T) string {
			p := filepath.Join(t.TempDir(), "sites.json")
			if err := os.WriteFile(p, []byte("null"), 0o644); err != nil {
				t.Fatal(err)
			}
			return p
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			// In a goroutine with a deadline, because the failure mode being
			// guarded against is "it served anyway", which never returns. A
			// test that hangs is a bad red; this one says what happened.
			done := make(chan error, 1)
			go func() { done <- RunStaticServerChild(addr, mk(t)) }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("returned cleanly; an unreadable map must fail the unit, not 404 every host")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("still running: it bound and is serving an unreadable map as an empty one")
			}
			// And it must not have bound: a listener answering 404 is the
			// outage this test exists to rule out.
			c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
			if err == nil {
				_ = c.Close()
				t.Fatal("something is listening: the server bound before reading the map")
			}
		})
	}
}
