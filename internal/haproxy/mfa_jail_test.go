package haproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func jailBackends() []Backend {
	return []Backend{
		{Name: "portal", DomainMatches: []string{"vpn.example.com"}, Server: "127.0.0.1:8080", MFAPortal: true},
		{Name: "wiki", DomainMatches: []string{"wiki.example.com"}, Server: "192.168.1.20:80"},
	}
}

// TestMFAJailOffChangesNothing pins that a host not using MFA gets exactly the
// config it got before the jail existed — no stray ACL, no reference to a file
// that won't be there.
func TestMFAJailOffChangesNothing(t *testing.T) {
	h := New("/tmp/haproxy.cfg", "/tmp/admin.sock")
	h.SetBackends(jailBackends())
	got := h.GenerateConfig(80, 443, nil)
	for _, forbidden := range []string{"mfa_jailed", "mfa-jailed.lst"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("MFA off should emit no %q, config:\n%s", forbidden, got)
		}
	}
}

// TestMFAJailEmitsRedirectInEveryFrontend covers the property that matters:
// a jailed peer is bounced no matter which listener it arrives on. Missing the
// rule in one frontend leaves that port a hole straight through the jail.
func TestMFAJailEmitsRedirectInEveryFrontend(t *testing.T) {
	certDir := t.TempDir()
	writeTestCert(t, filepath.Join(certDir, "vpn.pem"), []string{"vpn.example.com"})

	h := New("/tmp/haproxy.cfg", "/tmp/admin.sock")
	h.SetBackends(jailBackends())
	h.SetMFAJail(MFAJail{
		Enabled:   true,
		ACLPath:   "/etc/haproxy/mfa-jailed.lst",
		PortalURL: "https://vpn.example.com/mfa",
	})

	for _, tc := range []struct {
		name string
		ssl  *SSLConfig
		want int // how many frontends should carry the rule
	}{
		{"ssl enabled — http_front + https_front", &SSLConfig{Enabled: true, CertDir: certDir}, 2},
		{"no ssl — single http_front", nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := h.GenerateConfig(80, 443, tc.ssl)
			if n := strings.Count(got, "acl mfa_jailed src -f /etc/haproxy/mfa-jailed.lst"); n != tc.want {
				t.Errorf("want %d mfa_jailed ACLs, got %d:\n%s", tc.want, n, got)
			}
			redirect := "http-request redirect location https://vpn.example.com/mfa code 302 if mfa_jailed !host_portal"
			if n := strings.Count(got, redirect); n != tc.want {
				t.Errorf("want %d jail redirects, got %d:\n%s", tc.want, n, got)
			}
		})
	}
}

// TestMFAJailRuleOrdering pins that the jail rule lands after the host ACLs it
// references and before the use_backend lines it must pre-empt. Emitted after
// use_backend, the peer is already routed and the jail never fires.
func TestMFAJailRuleOrdering(t *testing.T) {
	h := New("/tmp/haproxy.cfg", "/tmp/admin.sock")
	h.SetBackends(jailBackends())
	h.SetMFAJail(MFAJail{Enabled: true, ACLPath: "/etc/haproxy/mfa-jailed.lst"})
	got := h.GenerateConfig(80, 443, nil)

	hostACL := strings.Index(got, "acl host_portal")
	jail := strings.Index(got, "acl mfa_jailed")
	useBackend := strings.Index(got, "use_backend")
	if hostACL < 0 || jail < 0 || useBackend < 0 {
		t.Fatalf("missing expected lines (host=%d jail=%d use=%d):\n%s", hostACL, jail, useBackend, got)
	}
	if hostACL >= jail || jail >= useBackend {
		t.Errorf("want host ACL < jail < use_backend, got %d < %d < %d:\n%s", hostACL, jail, useBackend, got)
	}
}

// TestMFAJailWithoutPortalEmitsNothing: a jail with no portal exception would
// deny a jailed peer access to the page that clears the jail. Fail open rather
// than ship an unrecoverable lockout.
func TestMFAJailWithoutPortalEmitsNothing(t *testing.T) {
	h := New("/tmp/haproxy.cfg", "/tmp/admin.sock")
	h.SetBackends([]Backend{{Name: "wiki", DomainMatches: []string{"wiki.example.com"}, Server: "192.168.1.20:80"}})
	h.SetMFAJail(MFAJail{Enabled: true, ACLPath: "/etc/haproxy/mfa-jailed.lst"})
	if got := h.GenerateConfig(80, 443, nil); strings.Contains(got, "mfa_jailed") {
		t.Errorf("no portal backend should suppress the jail entirely, got:\n%s", got)
	}
}

func TestMFAJailDeniesWhenNoPortalURL(t *testing.T) {
	h := New("/tmp/haproxy.cfg", "/tmp/admin.sock")
	h.SetBackends(jailBackends())
	h.SetMFAJail(MFAJail{Enabled: true, ACLPath: "/etc/haproxy/mfa-jailed.lst"})
	got := h.GenerateConfig(80, 443, nil)
	if !strings.Contains(got, "http-request deny deny_status 403 if mfa_jailed !host_portal") {
		t.Errorf("expected 403 fallback with no portal URL, got:\n%s", got)
	}
	if strings.Contains(got, "redirect location") {
		t.Errorf("should not redirect without a portal URL, got:\n%s", got)
	}
}

// TestMFAJailMultiplePortalsAllExempt — several proxy.self services means
// several legitimate portal hosts; every one must be excluded or the redirect
// loops on whichever was missed.
func TestMFAJailMultiplePortalsAllExempt(t *testing.T) {
	h := New("/tmp/haproxy.cfg", "/tmp/admin.sock")
	h.SetBackends([]Backend{
		{Name: "portal", DomainMatches: []string{"vpn.example.com"}, Server: "127.0.0.1:8080", MFAPortal: true},
		{Name: "admin", DomainMatches: []string{"admin.example.com"}, Server: "127.0.0.1:8080", MFAPortal: true},
	})
	h.SetMFAJail(MFAJail{Enabled: true, ACLPath: "/etc/haproxy/mfa-jailed.lst"})
	got := h.GenerateConfig(80, 443, nil)
	if !strings.Contains(got, "if mfa_jailed !host_admin !host_portal") &&
		!strings.Contains(got, "if mfa_jailed !host_portal !host_admin") {
		t.Errorf("both portal hosts must be exempt, got:\n%s", got)
	}
}

func TestWriteJailACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "mfa-jailed.lst")

	changed, err := WriteJailACL(path, []string{"10.100.0.9", "10.100.0.2"})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !changed {
		t.Error("first write should report changed")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	// Sorted, so an unordered jail set doesn't cause a spurious reload.
	if !strings.Contains(string(body), "10.100.0.2\n10.100.0.9\n") {
		t.Errorf("want sorted IPs, got:\n%s", body)
	}

	changed, err = WriteJailACL(path, []string{"10.100.0.9", "10.100.0.2"})
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if changed {
		t.Error("identical contents must not report changed — that reloads HAProxy for nothing")
	}

	changed, err = WriteJailACL(path, nil)
	if err != nil {
		t.Fatalf("empty write: %v", err)
	}
	if !changed {
		t.Error("clearing the list is a change")
	}
	body, _ = os.ReadFile(path)
	if strings.Contains(string(body), "10.100.0.2") {
		t.Errorf("cleared list must not retain IPs, got:\n%s", body)
	}
	// The file must still exist — HAProxy won't start without it.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("ACL file must survive an empty jail set: %v", err)
	}
}
