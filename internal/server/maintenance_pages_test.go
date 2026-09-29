package server

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/haproxy"
	"github.com/iodesystems/homelab-horizon/internal/iptables"
)

// THE BYTE-IDENTICAL PROOF FOR THE HAPROXY ERRORS DIRECTORY.
//
// Two writers fill that directory today and will until hz stops writing files
// at all (item 12 step 5, plan/design/privilege-audit.md §7.B):
//
//	hz     haproxy.WriteConfig' 503 stanza + Config.WriteMaintenancePageFiles
//	agent  the HAProxySection's Files, plus the prune its Dirs claim allows
//
// Every other test in this tree checks ONE of those — that the payload carries
// the pages, or that the plan would remove the stale one. A test that only
// checks the new path proves the new path exists, not that the move is safe.
// What makes the move safe is that the two writers cannot disagree, and the
// only way to see that is to run both and compare the directory they produce:
// names, bytes and modes.
//
// It is scoped to the errors directory deliberately. That is what the claim is
// about, it is the only directory both writers touch, and widening the compare
// to the whole HAProxy directory would only add files hz's maintenance-page
// writer never had an opinion on.

// dirSnapshot is one directory, flattened to something comparable: name →
// mode + contents. Read rather than derived, so what is asserted is the state
// on disk and not a second rendering of the intent.
//
// Named for what it does rather than for this file, because the second
// hand-over reuses it verbatim (ipforward_test.go): the compare is the same
// compare wherever two writers share a directory.
type dirSnapshot map[string]string

func snapshotDir(t *testing.T, dir string) dirSnapshot {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	out := dirSnapshot{}
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			t.Fatalf("stat %s/%s: %v", dir, e.Name(), err)
		}
		if !fi.Mode().IsRegular() {
			out[e.Name()] = fmt.Sprintf("mode=%v (not a regular file)", fi.Mode())
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s/%s: %v", dir, e.Name(), err)
		}
		out[e.Name()] = fmt.Sprintf("mode=%04o bytes=%q", fi.Mode().Perm(), string(b))
	}
	return out
}

func (s dirSnapshot) names() []string {
	out := make([]string, 0, len(s))
	for n := range s {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// seedErrorsDir puts in a directory what a real gateway has in one: the
// distribution's own error pages, which hz does not own and must never touch,
// and some adversarial names around the maintenance-page shape.
//
// THE ADVERSARIAL NAMES ARE THE POINT OF THE SEED. hz's writer prunes by
// `strings.HasSuffix(name, MaintenancePageSuffix)`; the agent prunes by
// `path.Match` against the `*_503.http` claim. Those are two different
// predicates spelled from one constant, and a table of names run through both
// writers is what shows they decide the same way — including the empty-prefix
// case (`_503.http`, where the glob's `*` matches nothing) and the near
// misses that neither may take.
func seedErrorsDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		// The distribution's. haproxy ships these; hz owns none of them.
		"400.http": "HTTP/1.0 400\r\n\r\nvanilla 400\n",
		"403.http": "HTTP/1.0 403\r\n\r\nvanilla 403\n",
		"500.http": "HTTP/1.0 500\r\n\r\nvanilla 500\n",
		"502.http": "HTTP/1.0 502\r\n\r\nvanilla 502\n",
		// A maintenance page for a service that no longer wants one. Both
		// writers must REMOVE it: HAProxy keeps serving a file it can open.
		"gone_503.http": "HTTP/1.0 503\r\n\r\nstale\n",
		// The empty-prefix edge of both predicates.
		"_503.http": "HTTP/1.0 503\r\n\r\nstale, no service name\n",
		// A dotfile that still carries the suffix.
		".hidden_503.http": "HTTP/1.0 503\r\n\r\nstale and hidden\n",
		// Near misses. Neither writer may take these.
		"x_503.http.bak": "backup, not a page\n",
		"y_503.httpx":    "not the suffix\n",
		"503.http.old":   "not the suffix either\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// maintenanceServices is the config half of the scenario: two services that
// want a page, one that does not.
func maintenanceServices() []config.Service {
	return []config.Service{
		{
			Name:    "alpha",
			Domains: []string{"alpha.example.test"},
			Proxy:   &config.ProxyConfig{Backend: "127.0.0.1:9001", MaintenancePage: "<h1>alpha is back soon</h1>"},
		},
		{
			Name:    "beta",
			Domains: []string{"beta.example.test"},
			Proxy:   &config.ProxyConfig{Backend: "127.0.0.1:9002"},
		},
		{
			// A name that has to be sanitised before it is a file name, so the
			// two paths are compared on the sanitiser as well.
			Name:    "Gamma Svc/2",
			Domains: []string{"gamma.example.test"},
			Proxy:   &config.ProxyConfig{Backend: "127.0.0.1:9003", MaintenancePage: "<h1>gamma window</h1>"},
		},
	}
}

// noopReloader lets Apply run its file and prune halves without shelling out
// to haproxy, dnsmasq or iptables. It records what it was asked to reload, so
// a test can still say whether the write half thought anything moved.
type noopReloader struct{ reloaded []agent.Subsystem }

func (r *noopReloader) HAProxy(*agent.HAProxySection) error {
	r.reloaded = append(r.reloaded, agent.SubsystemHAProxy)
	return nil
}
func (r *noopReloader) DNSMasq(*agent.DNSMasqSection) error {
	r.reloaded = append(r.reloaded, agent.SubsystemDNSMasq)
	return nil
}
func (r *noopReloader) WireGuard(*agent.WireGuardSection) error {
	r.reloaded = append(r.reloaded, agent.SubsystemWireGuard)
	return nil
}
func (r *noopReloader) IPTables(*agent.IPTablesSection, []iptables.Rule) (iptables.Report, error) {
	r.reloaded = append(r.reloaded, agent.SubsystemIPTables)
	return iptables.Report{}, nil
}
func (r *noopReloader) Units(*agent.FilesSection) error {
	r.reloaded = append(r.reloaded, agent.SubsystemFiles)
	return nil
}
func (r *noopReloader) RestartUnit(string) error { return nil }
func (r *noopReloader) SegmentTunnel(agent.TunnelDecision) error {
	r.reloaded = append(r.reloaded, agent.SubsystemSegments)
	return nil
}

// hzFillsTheErrorsDirectory runs hz's OWN writers — the two that exist today —
// over a fresh HAProxy directory and returns the errors directory they left.
func hzFillsTheErrorsDirectory(t *testing.T, cfg config.Config) string {
	t.Helper()
	dir := t.TempDir()
	cfg.HAProxyConfigPath = filepath.Join(dir, "haproxy.cfg")
	seedErrorsDir(t, cfg.HAProxyErrorsDir())

	// The static 503: written by WriteConfig, beside the config that names it.
	if err := haproxy.New(cfg.HAProxyConfigPath, "/run/haproxy/admin.sock").
		WriteConfig(cfg.HAProxyHTTPPort, cfg.HAProxyHTTPSPort, nil); err != nil {
		t.Fatalf("hz WriteConfig: %v", err)
	}
	// The per-service pages, and the prune.
	if err := cfg.WriteMaintenancePageFiles(); err != nil {
		t.Fatalf("hz WriteMaintenancePageFiles: %v", err)
	}
	return cfg.HAProxyErrorsDir()
}

// agentFillsTheErrorsDirectory takes the payload hz SERVES for this machine and
// applies it the way hz-agent would — real observer, real plan, real Apply —
// over an identically seeded directory, and returns the one it left.
//
// It goes through the served payload rather than desiredFor so the proof
// covers the wire: a field that did not survive JSON would show up here as a
// difference, which is exactly what it would be on a box.
func agentFillsTheErrorsDirectory(t *testing.T, s *Server) (string, agent.Result) {
	t.Helper()
	errorsDir := s.cfg().HAProxyErrorsDir()
	seedErrorsDir(t, errorsDir)
	return errorsDir, agentPass(t, s)
}

// agentPass is one hz-agent cycle against the payload hz is serving right now:
// poll, observe, plan, apply.
func agentPass(t *testing.T, s *Server) agent.Result {
	t.Helper()
	d, _ := servedDesired(t, s)
	gens := agentGenerations(t, s)
	obs := agent.NewSystemObserver().WithGenerations(gens).Observe(&d)
	res, err := agent.Apply(&d, agent.Compute(&d, obs), obs, &noopReloader{}, gens)
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("agent Apply: %v %v", err, res.Errors)
	}
	return res
}

// agentGenerations is the applied-generation record the agent keeps, one per
// test, so repeated passes in a test see the same record a real agent would.
func agentGenerations(t *testing.T, s *Server) agent.GenerationStore {
	t.Helper()
	dir := filepath.Dir(s.cfg().HAProxyConfigPath)
	return agent.FileGenerationStore{Path: filepath.Join(dir, "applied-generations.json")}
}

// The proof: hz's writers and the agent's payload leave the SAME directory.
func TestBothWritersLeaveTheSameErrorsDirectory(t *testing.T) {
	s, _ := agentTestServer(t)
	cfg := *s.cfg()
	cfg.Services = maintenanceServices()
	s.config.Store(&cfg)

	hzDir := hzFillsTheErrorsDirectory(t, cfg)
	agentDir, res := agentFillsTheErrorsDirectory(t, s)
	if hzDir == agentDir {
		t.Fatal("both writers ran over the same directory; the comparison would be a tautology")
	}

	hzState := snapshotDir(t, hzDir)
	agentState := snapshotDir(t, agentDir)

	// THE COMPARISON IS ONLY WORTH ANYTHING IF THE DIRECTORY IS INTERESTING.
	// Two empty directories are byte-identical, and so are two directories
	// where neither writer did anything. Pin what has to be in there first,
	// so a scenario that quietly stopped exercising the writers fails here
	// rather than passing as a match.
	alpha := config.MaintenancePageName("alpha")
	gamma := config.MaintenancePageName("Gamma Svc/2")
	for _, name := range []string{
		alpha, gamma, // written
		filepath.Base(haproxy.Error503Path),            // the static page, written
		"400.http", "403.http", "500.http", "502.http", // the distribution's, KEPT
		"x_503.http.bak", "y_503.httpx", "503.http.old", // near misses, KEPT
	} {
		if _, ok := hzState[name]; !ok {
			t.Fatalf("hz's directory has no %s: %v", name, hzState.names())
		}
	}
	for _, name := range []string{"gone_503.http", "_503.http", ".hidden_503.http"} {
		if _, ok := hzState[name]; ok {
			t.Fatalf("hz did not prune %s, so the scenario is not exercising the prune", name)
		}
	}
	if strings.Contains(gamma, "/") {
		t.Fatalf("MaintenancePageName left a separator in %q; it is not a bare file name", gamma)
	}

	// And now the whole directory, name by name.
	if got, want := agentState.names(), hzState.names(); !slicesEqualStr(got, want) {
		t.Fatalf("the two writers leave different files:\n agent %v\n hz    %v", got, want)
	}
	for _, name := range hzState.names() {
		if agentState[name] != hzState[name] {
			t.Fatalf("%s differs between the writers:\n agent %s\n hz    %s",
				name, agentState[name], hzState[name])
		}
	}

	// Cross-check the agent's own account of what it did against the
	// directory. A prune that silently did nothing and a comparison that was
	// never reached look identical from the filesystem alone.
	var removed []string
	for _, p := range res.Removed {
		if filepath.Dir(p) == agentDir {
			removed = append(removed, filepath.Base(p))
		}
	}
	sort.Strings(removed)
	want := []string{"_503.http", ".hidden_503.http", "gone_503.http"}
	sort.Strings(want)
	if !slicesEqualStr(removed, want) {
		t.Fatalf("the agent removed %v, want exactly %v", removed, want)
	}
}

// Rerunning either writer over the directory the OTHER one left changes
// nothing. Both are on the box until step 5, in either order and on their own
// triggers, so convergence is the property that matters — not just that one
// pass agrees.
func TestNeitherWriterUndoesTheOther(t *testing.T) {
	s, _ := agentTestServer(t)
	cfg := *s.cfg()
	cfg.Services = maintenanceServices()
	s.config.Store(&cfg)

	dir, _ := agentFillsTheErrorsDirectory(t, s)
	afterAgent := snapshotDir(t, dir)

	// hz's writers, second, over the agent's result.
	if err := haproxy.New(cfg.HAProxyConfigPath, "/run/haproxy/admin.sock").
		WriteConfig(cfg.HAProxyHTTPPort, cfg.HAProxyHTTPSPort, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.cfg().WriteMaintenancePageFiles(); err != nil {
		t.Fatal(err)
	}
	if after := snapshotDir(t, dir); !stateEqual(after, afterAgent) {
		t.Fatalf("hz's writers changed what the agent left:\n after %v\n was   %v", after, afterAgent)
	}

	// And the agent, third, over hz's result: nothing more to write, nothing
	// more to remove.
	res := agentPass(t, s)
	for _, p := range res.Wrote {
		if filepath.Dir(p) == dir {
			t.Fatalf("the agent rewrote %s after hz had written it; a rewrite reloads HAProxy for nothing", p)
		}
	}
	if len(res.Removed) > 0 {
		t.Fatalf("the agent removed %v on a converged directory", res.Removed)
	}
	if after := snapshotDir(t, dir); !stateEqual(after, afterAgent) {
		t.Fatalf("a second agent pass changed the directory:\n after %v\n was   %v", after, afterAgent)
	}
}

// Clearing a maintenance page removes the file by BOTH paths. The write half
// agreeing is the easy half; the delete half is the one that needed
// agent.Directory to exist at all.
func TestClearingAPageRemovesTheFileByEitherPath(t *testing.T) {
	s, _ := agentTestServer(t)
	cfg := *s.cfg()
	cfg.Services = maintenanceServices()
	s.config.Store(&cfg)

	// Converge both directories with the pages present.
	agentDir, _ := agentFillsTheErrorsDirectory(t, s)
	hzDir := hzFillsTheErrorsDirectory(t, cfg)
	alpha := config.MaintenancePageName("alpha")
	for _, dir := range []string{agentDir, hzDir} {
		if _, err := os.Stat(filepath.Join(dir, alpha)); err != nil {
			t.Fatalf("alpha's page is not in %s to begin with: %v", dir, err)
		}
	}

	// The admin clears it.
	cleared := *s.cfg()
	cleared.Services = maintenanceServices()
	cleared.Services[0].Proxy = &config.ProxyConfig{Backend: "127.0.0.1:9001"}
	s.config.Store(&cleared)

	clearedHZ := cleared
	clearedHZ.HAProxyConfigPath = filepath.Join(hzDir, "..", "haproxy.cfg")
	if err := clearedHZ.WriteMaintenancePageFiles(); err != nil {
		t.Fatal(err)
	}
	agentPass(t, s)

	for _, dir := range []string{agentDir, hzDir} {
		if _, err := os.Stat(filepath.Join(dir, alpha)); !os.IsNotExist(err) {
			t.Fatalf("%s still holds a cleared page; HAProxy would keep serving it (err=%v)",
				filepath.Join(dir, alpha), err)
		}
	}
	// Gamma's is untouched by the clear, both ways.
	gamma := config.MaintenancePageName("Gamma Svc/2")
	for _, dir := range []string{agentDir, hzDir} {
		if _, err := os.Stat(filepath.Join(dir, gamma)); err != nil {
			t.Fatalf("clearing alpha took gamma's page in %s too: %v", dir, err)
		}
	}
	if !stateEqual(snapshotDir(t, agentDir), snapshotDir(t, hzDir)) {
		t.Fatalf("the two writers diverge after a clear:\n agent %v\n hz    %v",
			snapshotDir(t, agentDir), snapshotDir(t, hzDir))
	}
}

func slicesEqualStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stateEqual(a, b dirSnapshot) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
