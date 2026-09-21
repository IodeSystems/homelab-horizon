package dnsmasq

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderStaysPure is the guard on the seam itself: render.go computes
// desired state and must not reach for the machine, so that half can run
// unprivileged (and off-box) once hz-agent owns the privileged half.
//
// An import list is a coarse check, but it is the one that catches the way this
// actually rots — someone adds an os.ReadFile to the renderer because the value
// was easier to read than to pass in. Which is exactly where this package
// started: the records file was read back inside the write path.
func TestRenderStaysPure(t *testing.T) {
	banned := map[string]string{
		"os":            "reads or writes the filesystem",
		"os/exec":       "runs commands",
		"net":           "talks to the network",
		"net/http":      "talks to the network",
		"time":          "reads the clock",
		"math/rand":     "is not deterministic",
		"crypto/rand":   "is not deterministic",
		"io/ioutil":     "reads or writes the filesystem",
		"path/filepath": "resolves paths against a real filesystem",
	}

	f, err := parser.ParseFile(token.NewFileSet(), "render.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse render.go: %v", err)
	}
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if why, bad := banned[path]; bad {
			t.Errorf("render.go imports %q, which %s — the pure half takes its inputs as arguments", path, why)
		}
	}
}

// TestUnitPrivilegeStaysOutOfApply keeps the two privileges apart. apply.go
// needs write access to /etc/dnsmasq.d; unit.go can install a systemd unit that
// runs anything as root. An exec.Command creeping into apply.go collapses the
// second into the first, and hz-agent then cannot offer one without the other.
func TestUnitPrivilegeStaysOutOfApply(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "apply.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse apply.go: %v", err)
	}
	for _, imp := range f.Imports {
		if path := strings.Trim(imp.Path.Value, `"`); path == "os/exec" {
			t.Error(`apply.go imports "os/exec" — running commands is unit.go's privilege, not the file writer's`)
		}
	}
}

// TestRenderNeedsNoMachine renders both files against paths that do not exist.
// The renderer never looks: the interface list, the upstreams and the record
// set all arrive as arguments, which is what lets "what would this box serve"
// be answered without being on it.
func TestRenderNeedsNoMachine(t *testing.T) {
	cfg := RenderConfig(ConfigInput{
		ConfigPath:  "/definitely/not/a/real/dir/hz.conf",
		HostsPath:   "/definitely/not/a/real/dir/hosts.conf",
		Interfaces:  []string{"wg0", "eth0"},
		Upstream:    []string{"192.0.2.53"},
		LocalDomain: "lan",
	})
	for _, want := range []string{
		"interface=wg0",
		"interface=eth0",
		"server=192.0.2.53",
		"domain=lan",
		"local=/lan/",
		"conf-file=/definitely/not/a/real/dir/hosts.conf",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("want %q in rendered config:\n%s", want, cfg)
		}
	}

	hosts := RenderHosts(HostsInput{
		LocalDomain: "lan",
		Records: []Record{
			{Name: "desktop", IP: "192.0.2.10"},
			{Name: "wiki.example.com", IP: "192.0.2.11", Wildcard: true, Comment: "service"},
		},
	})
	for _, want := range []string{
		"host-record=desktop,desktop.lan,192.0.2.10",
		"# service\naddress=/wiki.example.com/192.0.2.11",
	} {
		if !strings.Contains(hosts, want) {
			t.Errorf("want %q in rendered hosts:\n%s", want, hosts)
		}
	}
}

// TestConfFileIncludeOmittedWhenPathsMatch pins the one conditional in the
// config render that is not about the local domain. A records file that *is*
// the config file must not be included from itself.
func TestConfFileIncludeOmittedWhenPathsMatch(t *testing.T) {
	same := RenderConfig(ConfigInput{ConfigPath: "/etc/hz.conf", HostsPath: "/etc/hz.conf"})
	if strings.Contains(same, "conf-file=") {
		t.Errorf("a config that is its own records file must not include itself:\n%s", same)
	}
	empty := RenderConfig(ConfigInput{ConfigPath: "/etc/hz.conf"})
	if strings.Contains(empty, "conf-file=") {
		t.Errorf("no records path means no include:\n%s", empty)
	}
}

// TestParseMappingsInvertsRenderHosts is the seam's other direction. The
// records file is an output, but it is an output someone can edit on the box,
// and ParseMappings is how hz sees what is actually there. Pinning it as the
// inverse of RenderHosts is what makes a drift check between the two honest.
func TestParseMappingsInvertsRenderHosts(t *testing.T) {
	in := HostsInput{
		Records: []Record{
			{Name: "desktop", IP: "192.0.2.10"},
			{Name: "wiki.example.com", IP: "192.0.2.11", Wildcard: true},
			{Name: "notes.example.com", IP: "192.0.2.12", Comment: "a comment that must not parse as a record"},
		},
	}
	got := ParseMappings(RenderHosts(in))
	want := map[string]string{
		"desktop":           "192.0.2.10",
		"wiki.example.com":  "192.0.2.11",
		"notes.example.com": "192.0.2.12",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d mappings, want %d: %v", len(got), len(want), got)
	}
	for name, ip := range want {
		if got[name] != ip {
			t.Errorf("mappings[%q] = %q, want %q", name, got[name], ip)
		}
	}
}

// TestParseMappingsMisreadsExpandedHostRecords pins a REAL BUG, deliberately
// left alone by the refactor that found it (plan/icebox.md).
//
// `host-record=` takes a list of names followed by the address, and the local
// domain expansion writes exactly that — `host-record=desktop,desktop.lan,IP`.
// The parser splits on "," and takes field 1 as the address, so it reads the
// qualified NAME as the IP and never sees the address at all.
//
// The write path is unaffected: hz derives records from config and never reads
// this back. The read-back is wrong for any expanded record, which means a
// drift check built on it would report a difference that does not exist. Fixing
// it is a behaviour change and belongs in its own commit, so this test states
// what the code does today rather than what it should do.
func TestParseMappingsMisreadsExpandedHostRecords(t *testing.T) {
	got := ParseMappings(RenderHosts(HostsInput{
		LocalDomain: "lan",
		Records:     []Record{{Name: "desktop", IP: "192.0.2.10"}},
	}))
	if got["desktop"] != "desktop.lan" {
		t.Fatalf("the known bug changed shape: mappings[\"desktop\"] = %q (was %q). "+
			"If this was fixed on purpose, delete this test and the icebox entry; "+
			"the correct value is %q", got["desktop"], "desktop.lan", "192.0.2.10")
	}
}

// TestGetMappingsSeesEditsMadeOutsideHz is the reason the read-back survives
// the refactor. hz derives every record from its config and overwrites the file
// wholesale, so nothing on the write path reads it — but the file lives on a
// box an admin can log into, and this is the one call that notices.
func TestGetMappingsSeesEditsMadeOutsideHz(t *testing.T) {
	dir := t.TempDir()
	hostsPath := filepath.Join(dir, "hosts.conf")
	d := New(filepath.Join(dir, "hz.conf"), hostsPath, nil, nil)

	if err := d.SetRecords([]Record{{Name: "wiki.example.com", IP: "192.0.2.11", Wildcard: true}}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Someone edits the box by hand.
	body, err := os.ReadFile(hostsPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	edited := string(body) + "address=/added.example.com/192.0.2.99\n"
	if err := os.WriteFile(hostsPath, []byte(edited), 0644); err != nil {
		t.Fatalf("hand edit: %v", err)
	}

	got, err := d.GetMappings()
	if err != nil {
		t.Fatalf("GetMappings: %v", err)
	}
	if got["added.example.com"] != "192.0.2.99" {
		t.Errorf("read-back missed a hand edit: %v", got)
	}

	// And the next config-derived sync wins, which is the documented contract
	// the file's own header states.
	if err := d.SetRecords([]Record{{Name: "wiki.example.com", IP: "192.0.2.11", Wildcard: true}}); err != nil {
		t.Fatalf("resync: %v", err)
	}
	got, err = d.GetMappings()
	if err != nil {
		t.Fatalf("GetMappings: %v", err)
	}
	if _, still := got["added.example.com"]; still {
		t.Error("a config-derived sync must replace the file, not merge into it")
	}
}
