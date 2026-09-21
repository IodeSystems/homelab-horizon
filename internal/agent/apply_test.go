package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/iptables"
)

// recordingReloader stands in for the subsystem apply halves so a test can
// prove which of them were asked to reload — the property no test could reach
// if Apply called systemctl directly.
type recordingReloader struct {
	calls []Subsystem
	live  []iptables.Rule
}

func (r *recordingReloader) HAProxy(*HAProxySection) error {
	r.calls = append(r.calls, SubsystemHAProxy)
	return nil
}
func (r *recordingReloader) DNSMasq(*DNSMasqSection) error {
	r.calls = append(r.calls, SubsystemDNSMasq)
	return nil
}
func (r *recordingReloader) WireGuard(*WireGuardSection) error {
	r.calls = append(r.calls, SubsystemWireGuard)
	return nil
}
func (r *recordingReloader) IPTables(_ *IPTablesSection, live []iptables.Rule) (iptables.Report, error) {
	r.calls = append(r.calls, SubsystemIPTables)
	r.live = live
	return iptables.Report{}, nil
}

func sectionsFor(t *testing.T, dir string) *Desired {
	t.Helper()
	return &Desired{
		Machine: "gateway",
		HAProxy: &HAProxySection{
			ConfigPath: filepath.Join(dir, "haproxy.cfg"),
			Files: []File{{
				Path: filepath.Join(dir, "haproxy.cfg"), Mode: 0o644,
				Contents: "global\n  daemon\n",
			}},
		},
		DNSMasq: &DNSMasqSection{
			ConfigPath: filepath.Join(dir, "dnsmasq.conf"),
			HostsPath:  filepath.Join(dir, "records.conf"),
			Files: []File{
				{Path: filepath.Join(dir, "dnsmasq.conf"), Mode: 0o644, Contents: "interface=eth0\n"},
				{Path: filepath.Join(dir, "records.conf"), Mode: 0o644, Contents: "address=/x/10.0.0.1\n"},
			},
		},
	}
}

// The first pass creates everything and reloads each subsystem once. The
// SECOND pass, against the files the first one wrote, must write nothing and
// reload nothing at all.
func TestApplyIsIdempotentAndReloadsNothingSecondTime(t *testing.T) {
	dir := t.TempDir()
	d := sectionsFor(t, dir)
	obs := NewSystemObserver()

	first := &recordingReloader{}
	res, err := Apply(d, Compute(d, obs.Observe(d)), obs.Observe(d), first)
	if err != nil {
		t.Fatalf("first apply: %v (%v)", err, res.Errors)
	}
	if len(res.Wrote) != 3 {
		t.Fatalf("want three files written, got %v", res.Wrote)
	}
	if len(first.calls) != 2 {
		t.Fatalf("want haproxy and dnsmasq reloaded once each, got %v", first.calls)
	}

	second := &recordingReloader{}
	plan := Compute(d, obs.Observe(d))
	if plan.Changed() {
		t.Fatalf("the second plan still wants changes: %+v", plan.Pending())
	}
	res2, err := Apply(d, plan, obs.Observe(d), second)
	if err != nil {
		t.Fatalf("second apply: %v (%v)", err, res2.Errors)
	}
	if len(res2.Wrote) != 0 {
		t.Fatalf("the second apply rewrote %v — identical contents must not be written", res2.Wrote)
	}
	if len(second.calls) != 0 {
		t.Fatalf("the second apply reloaded %v — that is a reload for nothing", second.calls)
	}
}

// One changed file reloads its own subsystem and nobody else's.
func TestApplyReloadsOnlyTheSubsystemThatMoved(t *testing.T) {
	dir := t.TempDir()
	d := sectionsFor(t, dir)
	obs := NewSystemObserver()
	if _, err := Apply(d, Compute(d, obs.Observe(d)), obs.Observe(d), &recordingReloader{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	d.DNSMasq.Files[1].Contents = "address=/y/10.0.0.2\n"
	r := &recordingReloader{}
	res, err := Apply(d, Compute(d, obs.Observe(d)), obs.Observe(d), r)
	if err != nil {
		t.Fatalf("apply: %v (%v)", err, res.Errors)
	}
	if len(r.calls) != 1 || r.calls[0] != SubsystemDNSMasq {
		t.Fatalf("a dnsmasq record change reloaded %v", r.calls)
	}
}

// A target the agent could not read is a target it must not overwrite.
func TestApplyRefusesToWriteOverAnUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "haproxy.cfg")
	d := haproxyDesired(path, "global\n")

	obs := Observed{Files: map[string]FileState{
		path: {Exists: true, ReadErr: "permission denied"},
	}}
	r := &recordingReloader{}
	res, err := Apply(d, Compute(d, obs), obs, r)
	if err == nil {
		t.Fatal("apply should report the refusal, not succeed quietly")
	}
	if len(res.Wrote) != 0 {
		t.Fatalf("it wrote %v over a file it could not read", res.Wrote)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Fatal("the file was created despite the refusal")
	}
	if len(r.calls) != 0 {
		t.Fatalf("a refused write still reloaded %v", r.calls)
	}
}

// iptables has no file, so it reconciles every pass — but only when the live
// set was genuinely read.
func TestIPTablesReconcilesOnlyWhenTheLiveSetWasRead(t *testing.T) {
	d := &Desired{Machine: "gateway", IPTables: &IPTablesSection{
		Expected: []iptables.Rule{{Table: "filter", Chain: "WG-FORWARD", Args: []string{"-j", "DROP"}}},
	}}

	blind := &recordingReloader{}
	if _, err := Apply(d, Compute(d, Observed{Files: map[string]FileState{}}), Observed{Files: map[string]FileState{}}, blind); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(blind.calls) != 0 {
		t.Fatalf("reconciled %v against a firewall it never read", blind.calls)
	}

	seeing := &recordingReloader{}
	obs := Observed{Files: map[string]FileState{}, IPTablesReadable: true}
	if _, err := Apply(d, Compute(d, obs), obs, seeing); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(seeing.calls) != 1 || seeing.calls[0] != SubsystemIPTables {
		t.Fatalf("want one iptables reconcile, got %v", seeing.calls)
	}
}

func TestWriteIfChangedDoesNotRewriteIdenticalContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b.conf")
	changed, err := writeIfChanged(path, []byte("x\n"), 0o644)
	if err != nil || !changed {
		t.Fatalf("first write: changed=%v err=%v", changed, err)
	}
	changed, err = writeIfChanged(path, []byte("x\n"), 0o644)
	if err != nil || changed {
		t.Fatalf("identical contents reported changed=%v err=%v", changed, err)
	}
	changed, err = writeIfChanged(path, []byte("y\n"), 0o644)
	if err != nil || !changed {
		t.Fatalf("different contents reported changed=%v err=%v", changed, err)
	}
}

// The poll is conditional: hz answers 304 and the agent does not re-plan.
func TestHTTPSourceConditionalGet(t *testing.T) {
	d := haproxyDesired("/etc/haproxy/haproxy.cfg", "global\n")
	etag := d.Fingerprint()

	var conditional int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t0ken" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("If-None-Match") == etag {
			conditional++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		_ = json.NewEncoder(w).Encode(d)
	}))
	defer srv.Close()

	s := &HTTPSource{BaseURL: srv.URL, Token: "t0ken"}
	got, newETag, changed, err := s.Fetch(context.Background(), "")
	if err != nil || !changed || got == nil {
		t.Fatalf("first fetch: changed=%v err=%v", changed, err)
	}
	if newETag != etag {
		t.Fatalf("etag %q != payload fingerprint %q", newETag, etag)
	}

	_, _, changed, err = s.Fetch(context.Background(), newETag)
	if err != nil || changed {
		t.Fatalf("second fetch: changed=%v err=%v", changed, err)
	}
	if conditional != 1 {
		t.Fatalf("the second poll was not conditional (%d)", conditional)
	}
}

func TestHTTPSourceSurfacesAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, _, _, err := (&HTTPSource{BaseURL: srv.URL}).Fetch(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("want the status surfaced, got %v", err)
	}
}

func TestFileSourceReportsUnchangedOnTheSameHash(t *testing.T) {
	d := haproxyDesired("/etc/haproxy/haproxy.cfg", "global\n")
	b, _ := json.Marshal(d)
	path := filepath.Join(t.TempDir(), "desired.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	s := FileSource{Path: path}
	got, etag, changed, err := s.Fetch(context.Background(), "")
	if err != nil || !changed || got == nil {
		t.Fatalf("first read: %v %v", changed, err)
	}
	if _, _, changed, err = s.Fetch(context.Background(), etag); err != nil || changed {
		t.Fatalf("second read: changed=%v err=%v", changed, err)
	}
}
