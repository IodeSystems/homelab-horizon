package haproxy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// jailedManager is an HAProxy manager whose rendered config names the MFA jail
// list, over a fresh directory where the list does not exist yet — the state a
// freshly bootstrapped nested hz was in on 2026-10-07.
func jailedManager(t *testing.T) (*HAProxy, string, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "haproxy.cfg")
	list := filepath.Join(dir, "mfa-jailed.lst")
	h := New(cfgPath, filepath.Join(dir, "admin.sock"))
	h.SetBackends([]Backend{
		{Name: "kiosk", DomainMatches: []string{"kiosk.example.com"}, Server: "127.0.0.1:8080", MFAPortal: true},
		{Name: "wiki", DomainMatches: []string{"wiki.example.com"}, Server: "192.0.2.10:80"},
	})
	h.SetMFAJail(MFAJail{Enabled: true, ACLPath: list, PortalURL: "https://kiosk.example.com/app/mfa"})
	return h, cfgPath, list
}

// TestWriteConfigWritesTheJailListFirst is the 2026-10-07 bug: the config names
// mfa-jailed.lst and nothing had written it. With the list absent at the
// start, WriteConfig must leave BOTH on disk, and the list must already be
// there when the config is written — asserted from inside the provider, which
// WriteConfig calls before anything else is written.
func TestWriteConfigWritesTheJailListFirst(t *testing.T) {
	h, cfgPath, list := jailedManager(t)
	if _, err := os.Stat(list); !os.IsNotExist(err) {
		t.Fatalf("precondition: the list must be absent, stat err = %v", err)
	}
	h.SetSideFiles(func() []SideFile {
		if _, err := os.Stat(cfgPath); err == nil {
			t.Errorf("haproxy.cfg was on disk before its side files were written")
		}
		// No one jailed: an EMPTY list, which is still a file (CLAUDE.md #2).
		return []SideFile{{Path: list, Contents: RenderJailACL(nil)}}
	})

	if err := h.WriteConfig(80, 443, nil); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	rendered, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("haproxy.cfg not written: %v", err)
	}
	if !strings.Contains(string(rendered), "src -f "+list) {
		t.Fatalf("instrument check: the rendered config does not name the list, so this test proves nothing:\n%s", rendered)
	}
	if _, err := os.Stat(list); err != nil {
		t.Fatalf("the list the config names is not on disk: %v", err)
	}
	// Every file the config names under its own directory exists.
	if err := checkReferencedFiles(string(rendered), filepath.Dir(cfgPath)); err != nil {
		t.Fatal(err)
	}

	// The real validator, when this box has it.
	bin, err := exec.LookPath("haproxy")
	if err != nil {
		t.Log("haproxy is not installed here; `haproxy -c` validation skipped (the reference check above stands in for it)")
		return
	}
	if out, err := exec.Command(bin, "-c", "-f", cfgPath).CombinedOutput(); err != nil {
		t.Fatalf("haproxy -c refused the rendered config: %v\n%s", err, out)
	}
}

// TestWriteConfigRefusesAMissingReference: with nobody to write the list,
// WriteConfig must fail and must NOT leave a haproxy.cfg on disk — a config a
// restart would refuse is worse than the previous one.
func TestWriteConfigRefusesAMissingReference(t *testing.T) {
	h, cfgPath, list := jailedManager(t)
	err := h.WriteConfig(80, 443, nil)
	if err == nil || !strings.Contains(err.Error(), list) {
		t.Fatalf("WriteConfig with no jail list = %v, want an error naming %s", err, list)
	}
	if _, statErr := os.Stat(cfgPath); !os.IsNotExist(statErr) {
		t.Fatalf("haproxy.cfg was written although it names a missing file (stat err %v)", statErr)
	}
}

func TestReferencedFiles(t *testing.T) {
	cfg := RenderConfig(ConfigInput{
		HTTPPort: 80, HTTPSPort: 443,
		Backends: []Backend{
			{Name: "kiosk", DomainMatches: []string{"kiosk.example.com"}, Server: "127.0.0.1:8080", MFAPortal: true},
			{Name: "wiki", DomainMatches: []string{"wiki.example.com"}, Server: "192.0.2.10:80", ErrorFile503: "/x/errors/wiki_503.http"},
		},
		MFAJail: MFAJail{Enabled: true, ACLPath: "/x/mfa-jailed.lst"},
	})
	got := strings.Join(ReferencedFiles(cfg), " ")
	for _, want := range []string{"/x/mfa-jailed.lst", "/x/errors/wiki_503.http", "/etc/haproxy/errors/503.http"} {
		if !strings.Contains(got, want) {
			t.Errorf("ReferencedFiles missed %s; got %s", want, got)
		}
	}
}
