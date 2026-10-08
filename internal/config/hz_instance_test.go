package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// An Hz Instance's fields are exactly these: it is not a machine (no
// segments, no agent enrolment), and its link and token are its own.
func TestAnHZInstanceIsItsOwnRecord(t *testing.T) {
	var got []string
	ty := reflect.TypeOf(HZInstance{})
	for i := 0; i < ty.NumField(); i++ {
		got = append(got, ty.Field(i).Name)
	}
	want := []string{"Name", "Project", "URL", "VPNClient", "TokenSHA256", "Note", "Domain", "Domains", "DNSGrant"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HZInstance fields = %v, want %v", got, want)
	}
	f, _ := reflect.TypeOf(Config{}).FieldByName("HZInstances")
	if tag := f.Tag.Get("json"); tag != "hz_instances,omitempty" {
		t.Fatalf("Config.HZInstances json tag = %q, want hz_instances,omitempty", tag)
	}
}

var liveDigest = strings.Repeat("b", 64)

// legacyNestedLiveShape is the live gateway's nested hz EXACTLY as N3/N4e
// wrote it (names from the brief; the digest and addresses are placeholders):
// machine `redline-loadtest` owned by redline with an hz marker naming its
// upstream VPN client and token digest, and rung redline/loadtest placed in it.
func legacyNestedLiveShape(machineExtra string) string {
	return `{
  "projects": [{"name": "redline"}],
  "environments": [
    {"project": "redline", "name": "staging", "posture": "staging"},
    {"project": "redline", "name": "loadtest", "posture": "prod", "from": "staging", "upstream": "redline-loadtest"}
  ],
  "machines": [
    {"name": "gw-1"},
    {"name": "redline-loadtest", "project": "redline"` + machineExtra + `,
     "hz": {"url": "http://10.100.0.9:8080", "vpn_client": "redline-loadtest-upstream", "token_sha256": "` + liveDigest + `"}}
  ],
  "wg_peers": [{"name": "redline-loadtest-upstream", "public_key": "k", "allowed_ips": "10.100.0.9/32"}],
  "vpn_profiles": {"redline-loadtest-upstream": "upstream"}
}`
}

var wantLiveInstance = HZInstance{
	Name: "redline-loadtest", Project: "redline", URL: "http://10.100.0.9:8080",
	VPNClient: "redline-loadtest-upstream", TokenSHA256: liveDigest,
}

// The old shape loads as an instance with the SAME name, owner, URL, VPN
// client and token digest; the carrier holds nothing else, so it is a removal
// candidate — and settling with no enrolment removes it. Save writes
// hz_instances and no `hz` key on a machine; the file reloads the same.
func TestTheLiveMachineHZShapeMigrates(t *testing.T) {
	cfg, err := LoadFromJSON([]byte(legacyNestedLiveShape("")))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.HZInstances, []HZInstance{wantLiveInstance}) || cfg.LegacyMachineHZ != nil {
		t.Fatalf("migrated = %+v legacy=%+v", cfg.HZInstances, cfg.LegacyMachineHZ)
	}
	if !reflect.DeepEqual(cfg.LegacyHZCarriers, []string{"redline-loadtest"}) {
		t.Fatalf("carriers = %v", cfg.LegacyHZCarriers)
	}
	if err := cfg.ValidateEnvironments(); err != nil {
		t.Fatalf("the rung's Upstream must resolve against the instance: %v", err)
	}
	removed, kept := cfg.SettleLegacyHZCarriers(func(string) bool { return false })
	if !reflect.DeepEqual(removed, []string{"redline-loadtest"}) || kept != nil {
		t.Fatalf("settle: removed=%v kept=%v", removed, kept)
	}
	if _, ok := cfg.FindMachine("redline-loadtest"); ok {
		t.Fatal("the empty carrier machine is still declared")
	}
	if _, ok := cfg.FindMachine("gw-1"); !ok {
		t.Fatal("settling removed an unrelated machine")
	}

	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"hz_instances"`) || strings.Contains(string(raw), `"hz":`) {
		t.Fatalf("Save did not write the new shape only:\n%s", raw)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.HZInstances, []HZInstance{wantLiveInstance}) || again.LegacyHZCarriers != nil {
		t.Fatalf("reloaded = %+v carriers=%v", again.HZInstances, again.LegacyHZCarriers)
	}
	if e, _ := again.LookupEnvironment("redline", "loadtest"); e.Upstream != "redline-loadtest" {
		t.Fatalf("the rung lost its upstream: %+v", e)
	}
}

// A carrier that is a box as well — segments or a note — is KEPT by the
// migration itself, and never offered for removal.
func TestACarrierThatHoldsMoreIsKept(t *testing.T) {
	for label, extra := range map[string]string{
		"note":     `, "note": "also the office build box"`,
		"segments": `, "segments": ["seg:lab"]`,
	} {
		cfg, err := LoadFromJSON([]byte(legacyNestedLiveShape(extra)))
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if len(cfg.LegacyHZCarriers) != 0 {
			t.Fatalf("%s: a carrier holding more was offered for removal: %v", label, cfg.LegacyHZCarriers)
		}
		cfg.SettleLegacyHZCarriers(func(string) bool { return false })
		if _, ok := cfg.FindMachine("redline-loadtest"); !ok {
			t.Fatalf("%s: the carrier was removed", label)
		}
		if !reflect.DeepEqual(cfg.HZInstances, []HZInstance{wantLiveInstance}) {
			t.Fatalf("%s: instance = %+v", label, cfg.HZInstances)
		}
	}
}

// An ENROLLED carrier is a real box: settling keeps it. nil enrolled is
// "nobody can tell", which keeps it too.
func TestAnEnrolledCarrierIsKept(t *testing.T) {
	for label, enrolled := range map[string]func(string) bool{
		"enrolled": func(n string) bool { return n == "redline-loadtest" },
		"unknown":  nil,
	} {
		cfg, err := LoadFromJSON([]byte(legacyNestedLiveShape("")))
		if err != nil {
			t.Fatal(err)
		}
		removed, kept := cfg.SettleLegacyHZCarriers(enrolled)
		if removed != nil || !reflect.DeepEqual(kept, []string{"redline-loadtest"}) {
			t.Fatalf("%s: removed=%v kept=%v", label, removed, kept)
		}
		if _, ok := cfg.FindMachine("redline-loadtest"); !ok {
			t.Fatalf("%s: an enrolled carrier was removed", label)
		}
		if cfg.LegacyHZCarriers != nil {
			t.Fatalf("%s: settling left candidates behind", label)
		}
	}
}

// A backup restore decodes with plain json.Unmarshal and then calls Save:
// the marker is captured on decode and migrated by Save, never dropped.
func TestSaveMigratesAMachineHZDecodedDirectly(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(legacyNestedLiveShape("")), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.LegacyMachineHZ) != 1 {
		t.Fatalf("decode did not capture the marker: %+v", cfg.LegacyMachineHZ)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, &cfg); err != nil {
		t.Fatalf("Save refused a restored pre-B3 config: %v", err)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.HZInstances, []HZInstance{wantLiveInstance}) {
		t.Fatalf("restored = %+v", again.HZInstances)
	}
}

// Re-running over a file that already has the instance (the same record) is
// a no-op; a DIFFERENT instance of that name is refused, never overwritten.
func TestMigrationOverAnExistingInstance(t *testing.T) {
	same := strings.Replace(legacyNestedLiveShape(""), `"projects"`,
		`"hz_instances": [{"name": "redline-loadtest", "project": "redline", "url": "http://10.100.0.9:8080", "vpn_client": "redline-loadtest-upstream", "token_sha256": "`+liveDigest+`"}],
  "projects"`, 1)
	cfg, err := LoadFromJSON([]byte(same))
	if err != nil {
		t.Fatalf("an identical instance must be accepted: %v", err)
	}
	if len(cfg.HZInstances) != 1 {
		t.Fatalf("instances = %+v", cfg.HZInstances)
	}
	different := strings.Replace(same, `"url": "http://10.100.0.9:8080", "vpn_client"`, `"url": "http://192.0.2.7:8080", "vpn_client"`, 1)
	if _, err := LoadFromJSON([]byte(different)); err == nil || !strings.Contains(err.Error(), "different values") {
		t.Fatalf("a conflicting instance must be refused: %v", err)
	}
}

// Removing a project re-attributes its instances to global, never deletes
// them — the same rule machines follow.
func TestRemovingAProjectReattributesItsInstances(t *testing.T) {
	c := &Config{}
	if err := c.AddProject("redline", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.AddHZInstance(HZInstance{Name: "rl-hz", Project: "redline", URL: "http://192.0.2.4:8080"}); err != nil {
		t.Fatal(err)
	}
	removes, _, err := c.ProjectRemoval("redline", true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range removes {
		if d.Kind == "hz-instance" && d.Name == "rl-hz" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the dry run does not name the instance: %+v", removes)
	}
	if _, err := c.RemoveProject("redline", true); err != nil {
		t.Fatal(err)
	}
	if h, ok := c.FindHZInstance("rl-hz"); !ok || h.Project != "" {
		t.Fatalf("instance after project removal: %+v %v", h, ok)
	}
}
