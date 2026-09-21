package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The directory-claim tests.
//
// The concept under test is "hz owns this directory and its contents are
// exactly what this payload lists" (Directory), and the property that matters
// is not that pruning WORKS — it is that pruning cannot happen anywhere else.
// So these tests come in pairs: one that the stale file goes, one that
// everything else stays, and one that says so even when the plan is a lie.

// errorsDirPayload is the real case the concept was built for: HAProxy's
// errors directory, where hz owns 503.http and the per-service maintenance
// pages and the distribution owns everything else.
func errorsDirPayload(dir string) *Desired {
	return &Desired{
		Machine: "gateway",
		HAProxy: &HAProxySection{
			ConfigPath: filepath.Join(dir, "haproxy.cfg"),
			Files: []File{
				{Path: filepath.Join(dir, "haproxy.cfg"), Mode: 0o644, Contents: "global\n  daemon\n"},
				{Path: filepath.Join(dir, "errors", "503.http"), Mode: 0o644, Contents: "HTTP/1.0 503\r\n\r\ndown\n"},
				{Path: filepath.Join(dir, "errors", "alpha_503.http"), Mode: 0o644, Contents: "HTTP/1.0 503\r\n\r\nalpha\n"},
			},
			Dirs: []Directory{{
				Path:  filepath.Join(dir, "errors"),
				Match: []string{"503.http", "*_503.http"},
			}},
		},
	}
}

// seedErrorsDir writes what a real gateway has in that directory: the
// distribution's own error pages, hz's default page, one live maintenance page
// and one left over from a service that no longer wants one.
func seedErrorsDir(t *testing.T, dir string) string {
	t.Helper()
	errs := filepath.Join(dir, "errors")
	if err := os.MkdirAll(errs, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"400.http":       "HTTP/1.0 400\r\n\r\nvanilla\n",
		"503.http":       "HTTP/1.0 503\r\n\r\ndown\n",
		"alpha_503.http": "HTTP/1.0 503\r\n\r\nalpha\n",
		"beta_503.http":  "HTTP/1.0 503\r\n\r\nSTALE — beta stopped wanting one\n",
	} {
		if err := os.WriteFile(filepath.Join(errs, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return errs
}

// The case the concept exists for: a page hz has stopped listing is reported
// as a removal, and nothing else in the directory is.
func TestClaimedDirectoryPrunesOnlyWhatItStoppedListing(t *testing.T) {
	dir := t.TempDir()
	errs := seedErrorsDir(t, dir)
	d := errorsDirPayload(dir)

	plan := Compute(d, NewSystemObserver().Observe(d))

	var removed []string
	for _, c := range plan.Changes {
		if c.Kind == KindRemove {
			removed = append(removed, c.Target)
		}
	}
	want := []string{filepath.Join(errs, "beta_503.http")}
	if strings.Join(removed, ",") != strings.Join(want, ",") {
		t.Fatalf("plan would remove %v, want exactly %v", removed, want)
	}
	if !plan.Changed() {
		t.Fatal("a removal is a pending change; Pending() does not count it")
	}

	// And a removal is what Apply does with it, once.
	r := &recordingReloader{}
	res, err := Apply(d, plan, NewSystemObserver().Observe(d), r)
	if err != nil {
		t.Fatalf("apply: %v (%v)", err, res.Errors)
	}
	if len(res.Removed) != 1 || res.Removed[0] != want[0] {
		t.Fatalf("apply removed %v, want %v", res.Removed, want)
	}
	if _, err := os.Stat(want[0]); !os.IsNotExist(err) {
		t.Fatalf("the stale page is still there: %v", err)
	}
	for _, keep := range []string{"400.http", "503.http", "alpha_503.http"} {
		if _, err := os.Stat(filepath.Join(errs, keep)); err != nil {
			t.Fatalf("%s was removed from a directory hz shares with the distribution: %v", keep, err)
		}
	}
	// A prune is a change to the subsystem that owns the directory, so it
	// reloads — and only it.
	if len(r.calls) != 1 || r.calls[0] != SubsystemHAProxy {
		t.Fatalf("a pruned maintenance page reloaded %v", r.calls)
	}

	// Second pass: nothing left to do, nothing reloaded.
	again := &recordingReloader{}
	plan2 := Compute(d, NewSystemObserver().Observe(d))
	if plan2.Changed() {
		t.Fatalf("the prune is not idempotent, still pending: %+v", plan2.Pending())
	}
	res2, err := Apply(d, plan2, NewSystemObserver().Observe(d), again)
	if err != nil || len(res2.Removed) != 0 || len(again.calls) != 0 {
		t.Fatalf("second pass removed %v and reloaded %v (%v)", res2.Removed, again.calls, err)
	}
}

// THE BOUND. This is the test that must fail if anybody widens the prune.
//
// Every target below is one a payload must not be able to remove, and the plan
// handed to Apply SAYS to remove each of them — hand-built, as a plan that
// arrived from anywhere other than Compute would be. So the test fails in
// exactly two ways, which are the two ways the bound can be lost:
//
//	widen Desired.prunable          — the file gets deleted
//	drop the re-check in Result.prune — the file gets deleted
//
// A Change is a plain struct. If the applier believed it, "what can hz-agent
// delete" would be answered by whatever produced the plan instead of by the
// payload hz signed for.
func TestRemovalIsImpossibleOutsideAClaimedDirectory(t *testing.T) {
	dir := t.TempDir()
	errs := seedErrorsDir(t, dir)
	d := errorsDirPayload(dir)

	// A sibling directory nobody claimed, holding a file with a name the
	// claim WOULD cover if the claim reached it.
	elsewhere := filepath.Join(dir, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(elsewhere, "gamma_503.http")
	if err := os.WriteFile(sibling, []byte("not hz's\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A subdirectory of the claimed directory. A claim is one directory deep.
	sub := filepath.Join(errs, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(sub, "delta_503.http")
	if err := os.WriteFile(nested, []byte("nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A file in the claimed directory the claim does not cover.
	vanilla := filepath.Join(errs, "400.http")
	// A file the payload itself writes.
	listed := filepath.Join(errs, "alpha_503.http")
	// The config, which is not even in the claimed directory.
	config := filepath.Join(dir, "haproxy.cfg")
	if err := os.WriteFile(config, []byte("global\n  daemon\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Traversal out of the claim, spelled as if it were inside it.
	outside := filepath.Join(dir, "passwd")
	if err := os.WriteFile(outside, []byte("root:x:0:0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	traversal := filepath.Join(errs, "..", "passwd")

	mustSurvive := map[string]string{
		"a sibling directory nobody claimed":       sibling,
		"a subdirectory of the claimed one":        nested,
		"a name the claim does not cover":          vanilla,
		"a file the payload itself writes":         listed,
		"a file outside the claimed directory":     config,
		"a traversal out of the claimed directory": traversal,
	}

	// The lie: a plan that says to remove all of them.
	lying := Plan{Machine: d.Machine, Generation: d.Fingerprint()}
	for _, target := range mustSurvive {
		lying.Changes = append(lying.Changes, Change{
			Subsystem: SubsystemHAProxy,
			Target:    target,
			Kind:      KindRemove,
			Detail:    "a plan that was not computed from this payload",
		})
	}

	r := &recordingReloader{}
	res, err := Apply(d, lying, NewSystemObserver().Observe(d), r)
	if err == nil {
		t.Fatal("Apply accepted a plan full of removals it must refuse")
	}
	if len(res.Removed) != 0 {
		t.Fatalf("Apply removed %v on a plan it did not compute", res.Removed)
	}
	for why, target := range mustSurvive {
		if _, statErr := os.Stat(target); statErr != nil {
			t.Fatalf("%s was removed (%s): %v", why, target, statErr)
		}
	}
	if len(res.Errors) != len(mustSurvive) {
		t.Fatalf("want one refusal per target, got %d: %v", len(res.Errors), res.Errors)
	}
	// The refusals are not a reload either: nothing moved.
	if len(r.calls) != 0 {
		t.Fatalf("refused removals still reloaded %v", r.calls)
	}
}

// The bound, asked directly, on the shapes a path can take. Compute never
// builds these — the applier is handed them.
func TestPrunableRefusesEveryPathOutsideTheClaim(t *testing.T) {
	d := errorsDirPayload("/etc/haproxy")

	for _, target := range []string{
		"/etc/haproxy/errors/400.http",         // claimed dir, uncovered name
		"/etc/haproxy/errors/503.http",         // covered, but the payload writes it
		"/etc/haproxy/errors/sub/x_503.http",   // one level down
		"/etc/haproxy/x_503.http",              // the parent of the claim
		"/etc/x_503.http",                      // somewhere else entirely
		"/etc/haproxy/errors/../x_503.http",    // traversal up
		"/etc/haproxy/errors/../../x_503.http", // and further
		"etc/haproxy/errors/x_503.http",        // relative
		"/etc/haproxy/errors",                  // the directory itself
		"/etc/haproxy/errors/",                 // and with a separator
		"",                                     // nothing at all
		"/",                                    // the root
	} {
		if sub, ok := d.prunable(target); ok {
			t.Errorf("prunable(%q) said yes for %s", target, sub)
		}
	}

	// And the one that must be yes, or the test above proves only that the
	// function always says no.
	if sub, ok := d.prunable("/etc/haproxy/errors/beta_503.http"); !ok || sub != SubsystemHAProxy {
		t.Fatalf("a stale page in the claimed directory was refused (%s, %v)", sub, ok)
	}
	// Spelled awkwardly, it is still the same file.
	if _, ok := d.prunable("/etc/haproxy//errors/./beta_503.http"); !ok {
		t.Fatal("a redundantly-spelled path in the claimed directory was refused")
	}
}

// A claim that names no pattern claims NOTHING.
//
// Fail-closed is not a preference here. /etc/haproxy/errors on a real gateway
// holds Debian's 400/403/408/500/502/504 pages next to hz's two names, so a
// claim that defaulted to "everything in the directory" would have deleted six
// files the first time it ran.
func TestAClaimWithNoPatternsPrunesNothing(t *testing.T) {
	dir := t.TempDir()
	errs := seedErrorsDir(t, dir)

	d := errorsDirPayload(dir)
	d.HAProxy.Dirs = []Directory{{Path: errs}} // claimed, covering nothing

	plan := Compute(d, NewSystemObserver().Observe(d))
	for _, c := range plan.Changes {
		if c.Kind == KindRemove {
			t.Fatalf("an empty claim planned a removal: %s", c.Target)
		}
	}

	// Nor does a pattern that tries to reach through a separator: a claim is
	// about one directory's names, so a pattern with a separator in it matches
	// nothing rather than matching something one level down.
	d.HAProxy.Dirs = []Directory{{Path: errs, Match: []string{"*/*", "sub/*.http", "errors/*_503.http"}}}
	for _, c := range Compute(d, NewSystemObserver().Observe(d)).Changes {
		if c.Kind == KindRemove {
			t.Fatalf("a separator pattern claimed %s", c.Target)
		}
	}
}

// Non-regular entries are never removed, and a symlink is the case that
// matters: following one would make a claimed directory a way to unlink
// anything the agent can reach.
func TestASymlinkInAClaimedDirectoryIsNeverRemoved(t *testing.T) {
	dir := t.TempDir()
	errs := seedErrorsDir(t, dir)
	d := errorsDirPayload(dir)

	victim := filepath.Join(dir, "important.conf")
	if err := os.WriteFile(victim, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(errs, "gamma_503.http") // a name the claim covers
	if err := os.Symlink(victim, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	plan := Compute(d, NewSystemObserver().Observe(d))
	for _, c := range plan.Changes {
		if c.Kind == KindRemove && c.Target == link {
			t.Fatal("the plan would remove a symlink")
		}
	}

	// And if a plan says so anyway, the applier still will not.
	lying := Plan{Changes: []Change{{Subsystem: SubsystemHAProxy, Target: link, Kind: KindRemove}}}
	res, err := Apply(d, lying, NewSystemObserver().Observe(d), &recordingReloader{})
	if err == nil {
		t.Fatal("Apply removed a symlink without complaint")
	}
	if len(res.Removed) != 0 {
		t.Fatalf("Apply removed %v", res.Removed)
	}
	if _, statErr := os.Lstat(link); statErr != nil {
		t.Fatalf("the symlink is gone: %v", statErr)
	}
	if _, statErr := os.Stat(victim); statErr != nil {
		t.Fatalf("the symlink's target is gone: %v", statErr)
	}
}

// A directory the agent could not list is UNKNOWN, not empty. The same rule
// observe.go already applies to a file it cannot read: an unlistable directory
// reported as clean is a claim nobody checked.
func TestAnUnlistableClaimedDirectoryIsUnknown(t *testing.T) {
	dir := t.TempDir()
	d := errorsDirPayload(dir)
	errs := filepath.Join(dir, "errors")

	obs := Observed{
		Files: map[string]FileState{},
		Dirs:  map[string]DirState{errs: {Exists: true, ReadErr: "permission denied"}},
	}
	plan := Compute(d, obs)

	var unknown int
	for _, c := range plan.Changes {
		switch {
		case c.Kind == KindRemove:
			t.Fatalf("a removal was planned against a directory that could not be listed: %s", c.Target)
		case c.Kind == KindUnknown && c.Target == errs:
			unknown++
		}
	}
	if unknown != 1 {
		t.Fatalf("an unlistable claimed directory did not report unknown: %+v", plan.Changes)
	}
}

// The generic section: files land, the units it names are poked, and an
// unchanged pass pokes nothing.
func TestGenericSectionWritesFilesAndPokesOnlyItsUnits(t *testing.T) {
	dir := t.TempDir()
	d := &Desired{
		Machine: "gateway",
		Files: &FilesSection{
			Files: []File{{
				Path: filepath.Join(dir, "journald.conf.d", "hz.conf"), Mode: 0o644,
				Contents: "[Journal]\nSystemMaxUse=200M\n",
			}},
			Units: []Unit{{Name: "systemd-journald", Action: UnitRestart}},
		},
	}

	obs := NewSystemObserver()
	r := &recordingReloader{}
	res, err := Apply(d, Compute(d, obs.Observe(d)), obs.Observe(d), r)
	if err != nil {
		t.Fatalf("apply: %v (%v)", err, res.Errors)
	}
	if len(res.Wrote) != 1 {
		t.Fatalf("wrote %v", res.Wrote)
	}
	if len(r.calls) != 1 || r.calls[0] != SubsystemFiles {
		t.Fatalf("poked %v", r.calls)
	}
	if len(r.units) != 1 || r.units[0].Name != "systemd-journald" {
		t.Fatalf("the reloader was handed %+v", r.units)
	}

	second := &recordingReloader{}
	plan := Compute(d, obs.Observe(d))
	if plan.Changed() {
		t.Fatalf("still pending: %+v", plan.Pending())
	}
	if _, err := Apply(d, plan, obs.Observe(d), second); err != nil {
		t.Fatal(err)
	}
	if len(second.calls) != 0 {
		t.Fatalf("an unchanged generic section poked %v — that is a restart for nothing", second.calls)
	}
}

// An action the agent does not know is an error, not a shrug. A payload asking
// for something this binary cannot do is version skew, and a file that lands
// with nothing poked looks applied and is not.
func TestAnUnknownUnitActionIsRefused(t *testing.T) {
	err := SystemReloader{}.Units(&FilesSection{Units: []Unit{{Name: "hz", Action: "sing"}}})
	if err == nil || !strings.Contains(err.Error(), "sing") {
		t.Fatalf("want a refusal naming the action, got %v", err)
	}
	// An empty action pokes nothing and is not an error: some files are read
	// by something on its own schedule.
	if err := (SystemReloader{}).Units(&FilesSection{Units: []Unit{{Name: "hz"}}}); err != nil {
		t.Fatalf("an action-less unit was refused: %v", err)
	}
}

// Certificate material: the payload FORCES Secret, exactly as it does for
// WireGuard, so a diff cannot widen into printing a private key because a
// producer left the flag off.
func TestCertFilesAreForcedSecret(t *testing.T) {
	d := &Desired{
		Machine: "gateway",
		Certs: &CertSection{
			Dir: "/etc/haproxy/certs",
			Files: []File{{
				Path: "/etc/haproxy/certs/example.pem", Mode: 0o600,
				Contents: fakeBundle, // the producer "forgot" Secret
			}},
		},
	}

	for _, of := range d.allFiles() {
		if of.Subsystem == SubsystemCerts && !of.File.Secret {
			t.Fatal("a cert file came out of the payload without Secret forced")
		}
	}

	obs := Observed{Files: map[string]FileState{
		"/etc/haproxy/certs/example.pem": {Exists: true, Contents: "-----BEGIN CERTIFICATE-----\nb2xk\n-----END CERTIFICATE-----\n"},
	}}
	report := Report(Compute(d, obs))
	if strings.Contains(report, fakeKeyBody) {
		t.Fatalf("the key body reached the report:\n%s", report)
	}
	if !strings.Contains(report, "key material") {
		t.Fatalf("a cert change was not described as secret:\n%s", report)
	}
}

// LAYER TWO, on the shape a certificate has — proven with layer one out of the
// way, on a section that gets no forcing at all.
//
// This is the cert half of what the WireGuard tests already pin for a
// key = value config. A PEM has no assignment in it, so secretAssignment sees
// nothing; without the banner and blob rules the whole private key would print
// the moment a producer marked a bundle public.
func TestRedactionCatchesPEMKeyMaterialWithNoSecretFlag(t *testing.T) {
	unforced := &Desired{
		Machine: "gateway",
		HAProxy: &HAProxySection{
			ConfigPath: "/etc/haproxy/haproxy.cfg",
			Files:      []File{{Path: "/etc/haproxy/haproxy.cfg", Contents: fakeBundle}},
		},
	}
	obs := Observed{Files: map[string]FileState{
		"/etc/haproxy/haproxy.cfg": {Exists: true, Contents: "global\n"},
	}}
	report := Report(Compute(unforced, obs))

	if strings.Contains(report, fakeKeyBody) {
		t.Fatalf("a PEM key body survived into a report with no Secret flag anywhere:\n%s", report)
	}
	if strings.Contains(report, "BEGIN PRIVATE KEY") {
		t.Fatalf("the PEM banner survived:\n%s", report)
	}
	if !strings.Contains(report, "[redacted") {
		t.Fatalf("nothing was redacted, so the lines never reached the redactor:\n%s", report)
	}

	// The redactor is idempotent — Sanitized runs it again at both ends of the
	// wire, and a second pass must not walk a redacted line back open.
	for _, line := range strings.Split(report, "\n") {
		if redactLine(line) != line {
			t.Fatalf("a second pass changed %q", line)
		}
	}
}

// Shape-correct and not a key: a 64-character base64 line is what a PEM body
// looks like, and this one is the alphabet in order.
const (
	fakeKeyBody = "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVphYmNkZWZnaGlqa2xtbm9wcXJzdHV2"
	fakeBundle  = "-----BEGIN CERTIFICATE-----\n" + fakeKeyBody + "\n-----END CERTIFICATE-----\n" +
		"-----BEGIN PRIVATE KEY-----\n" + fakeKeyBody + "\n-----END PRIVATE KEY-----\n"
)
