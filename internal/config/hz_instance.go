package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
)

// Hz Instances: a nested hz with its OWN config (operator, 2026-10-02 — the
// kinds of node in CLAUDE.md: Machine, Vantage, Hz Instance, Hz Node, Build
// Agent; never fold one into another).
//
// AN HZ INSTANCE IS NOT A MACHINE. A Machine is a box hz projects a network and
// units ONTO, and its credential is an agent enrolment. An Hz Instance is a
// separate hz — its own records, its own keys — that pulls the desired state
// of the rungs placed in it (Environment.Upstream names it) with an instance
// token. Until B3 it was a marker on a Machine (`machines[].hz`), which listed
// every nested hz as a box on the Machines page and made "remove the machine"
// the way to remove an hz.
//
// It confers nothing on its own: hz never dials the URL (CLAUDE.md #1) and no
// renderer reads the record (TestNestedMarkersChangeNoRenderedArtifact). The
// token digest is the child's only power: desired, artifact download, the
// forwarded report — for rungs whose Upstream names this instance — and the
// bootstrap's binary download.

// HZInstance is one nested hz.
type HZInstance struct {
	// Name is the instance's identity: what Environment.Upstream names, what
	// the instance token authenticates as (instance:<name>) and what its pulls
	// are recorded under. Unique among instances; it shares no namespace with
	// machines.
	Name string `json:"name"`
	// Project is the owning project, or "" for global. Responsibility, not
	// placement — the same meaning Machine.Project has.
	Project string `json:"project,omitempty"`
	// URL is where that hz answers, http or https with a host. A statement
	// for the operator and the address the Instances list shows; this hz
	// never contacts it.
	URL string `json:"url"`
	// VPNClient is the VPN client (a wg0 peer, by name) the instance reaches
	// THIS hz through — profile `upstream`, so it reaches only this hz's API
	// (vpn_upstream.go). "" is no link. When set it must name a client that
	// exists and is `upstream` (validateUpstreamLinks); removing the instance
	// is blocked by it, and cascade removes it.
	VPNClient string `json:"vpn_client,omitempty"`
	// TokenSHA256 is the sha256 (hex) of the instance token this hz minted
	// for it. The token is shown once and never stored; "" is "none minted".
	// No edit sets it — only a mint (SetInstanceTokenHash).
	TokenSHA256 string `json:"token_sha256,omitempty"`
	// Note is free text for the operator.
	Note string `json:"note,omitempty"`
}

// LegacyMachineHZ is the pre-B3 nested-hz marker, `machines[].hz`, with the
// machine it sat on. Read only — Config.UnmarshalJSON captures it and
// MigrateLegacyMachineHZ moves it; nothing writes it.
type LegacyMachineHZ struct {
	Machine     string `json:"-"`
	URL         string `json:"url"`
	VPNClient   string `json:"vpn_client,omitempty"`
	TokenSHA256 string `json:"token_sha256,omitempty"`
}

// UnmarshalJSON decodes a config as usual and, beside it, captures every
// pre-B3 `machines[].hz` marker into LegacyMachineHZ — the Machine record no
// longer has the field, so without this a config written before B3 would lose
// its nested hz (and the instance token in use) on the first decode.
//
// It lives on Config, not on Machine, because every decode path goes through
// it: LoadFromJSON, peer-sync, and the backup restore that decodes with plain
// json.Unmarshal and then calls Save (which migrates, as it does for B2's
// legacy `builders`).
func (c *Config) UnmarshalJSON(data []byte) error {
	type plain Config
	if err := json.Unmarshal(data, (*plain)(c)); err != nil {
		return err
	}
	var legacy struct {
		Machines []struct {
			Name string           `json:"name"`
			HZ   *LegacyMachineHZ `json:"hz"`
		} `json:"machines"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return err
	}
	var found []LegacyMachineHZ
	for _, m := range legacy.Machines {
		if m.HZ == nil {
			continue
		}
		h := *m.HZ
		h.Machine = m.Name
		found = append(found, h)
	}
	c.LegacyMachineHZ = append(c.LegacyMachineHZ, found...)
	return nil
}

// MigrateLegacyMachineHZ turns every pre-B3 `machines[].hz` marker into an
// HZInstance with the SAME name, owner, URL, VPN client and token digest — so
// the token the child holds keeps authenticating and nothing is
// re-bootstrapped — and clears the legacy capture.
//
// The machine that carried the marker is then a CANDIDATE for removal, never
// removed here: config cannot read the agent credential store (it lives beside
// the config and does not ride peer-sync, MachineRemoval's comment), and a
// carrier with an agent enrolment is a real box. So:
//
//   - a carrier with segments or a note is KEPT, said LOUD: it is a box as
//     well as an hz, and what it holds is the operator's to remove;
//   - every other carrier goes on LegacyHZCarriers, and
//     SettleLegacyHZCarriers — called by the server, which holds the store —
//     removes it unless it is enrolled.
//
// An instance of the same name that already exists is accepted when it is the
// same record (a re-run), and refused otherwise: the operator has to see it.
func (c *Config) MigrateLegacyMachineHZ() error {
	if len(c.LegacyMachineHZ) == 0 {
		c.LegacyMachineHZ = nil
		return nil
	}
	instances := append([]HZInstance(nil), c.HZInstances...)
	carriers := append([]string(nil), c.LegacyHZCarriers...)
	for _, old := range c.LegacyMachineHZ {
		m, ok := c.FindMachine(old.Machine)
		if !ok {
			return fmt.Errorf("legacy hz marker names machine %q, which is not declared", old.Machine)
		}
		inst := HZInstance{
			Name:        m.Name,
			Project:     m.Project,
			URL:         strings.TrimSpace(old.URL),
			VPNClient:   strings.TrimSpace(old.VPNClient),
			TokenSHA256: strings.TrimSpace(old.TokenSHA256),
		}
		dup := false
		for _, have := range instances {
			if have.Name != inst.Name {
				continue
			}
			if have != inst {
				return fmt.Errorf("machine %q carries a legacy hz marker and an hz instance %q already exists with different values — reconcile them in config.json", m.Name, inst.Name)
			}
			dup = true
		}
		if !dup {
			instances = append(instances, inst)
		}
		if len(m.Segments) > 0 || strings.TrimSpace(m.Note) != "" {
			slog.Warn("LOUD: hz instance migrated off machine "+m.Name+"; the MACHINE is kept because it holds more than the marker — remove it with `hz machine rm "+m.Name+"` if it is not a box hz projects onto",
				"machine", m.Name, "segments", strings.Join(m.Segments, ","), "note", m.Note)
			continue
		}
		carriers = append(carriers, m.Name)
	}
	sort.SliceStable(instances, func(i, j int) bool { return instances[i].Name < instances[j].Name })
	c.HZInstances = instances
	c.LegacyHZCarriers = carriers
	c.LegacyMachineHZ = nil
	return nil
}

// SettleLegacyHZCarriers removes each machine MigrateLegacyMachineHZ left as
// a removal candidate, unless enrolled says hz holds an agent credential for
// it — then it is KEPT and said LOUD. nil enrolled is "nobody can tell", and
// keeps them all. It clears the candidate list either way.
func (c *Config) SettleLegacyHZCarriers(enrolled func(string) bool) (removed, kept []string) {
	if len(c.LegacyHZCarriers) == 0 {
		c.LegacyHZCarriers = nil
		return nil, nil
	}
	drop := map[string]bool{}
	for _, name := range c.LegacyHZCarriers {
		if enrolled == nil || enrolled(name) {
			kept = append(kept, name)
			slog.Warn("LOUD: hz instance migrated off machine "+name+"; the MACHINE is kept because hz holds an agent credential for it (or could not tell) — remove it with `hz machine rm "+name+" --cascade` if it is not a box hz projects onto",
				"machine", name)
			continue
		}
		drop[name] = true
	}
	if len(drop) > 0 {
		machines := make([]Machine, 0, len(c.Machines))
		for _, m := range c.Machines {
			if drop[m.Name] {
				removed = append(removed, m.Name)
				continue
			}
			machines = append(machines, m)
		}
		if len(machines) == 0 {
			machines = nil
		}
		c.Machines = machines
		for _, name := range removed {
			slog.Info("hz instance migrated off machine "+name+"; the machine held nothing else and was removed", "instance", name)
		}
	}
	c.LegacyHZCarriers = nil
	return removed, kept
}

// hzInstanceNameRe is the bootstrap's name charset: an instance name becomes a
// VPN client name, a URL path segment and a log label.
var hzInstanceNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// CheckHZInstanceName refuses a name outside the charset.
func CheckHZInstanceName(name string) error {
	if !hzInstanceNameRe.MatchString(name) {
		return fmt.Errorf("hz instance name %q must be letters, digits, '.', '_' or '-', start with a letter or digit, and be at most 63 characters", name)
	}
	return nil
}

// FindHZInstance is the instance named name, if any.
func (c *Config) FindHZInstance(name string) (HZInstance, bool) {
	for _, h := range c.HZInstances {
		if h.Name == name {
			return h, true
		}
	}
	return HZInstance{}, false
}

// ValidateHZInstances refuses an instance with a name outside the charset or
// used twice, an undeclared owner, a URL that is not http(s) with a host, or a
// malformed token digest; and an unmigrated legacy marker. VPN client links
// are checked by validateUpstreamLinks.
func (c *Config) ValidateHZInstances() error {
	if len(c.LegacyMachineHZ) > 0 {
		return fmt.Errorf("the legacy `machines[].hz` marker was not migrated to `hz_instances` (MigrateLegacyMachineHZ)")
	}
	seen := map[string]bool{}
	for _, h := range c.HZInstances {
		if err := CheckHZInstanceName(h.Name); err != nil {
			return err
		}
		if seen[h.Name] {
			return fmt.Errorf("hz instance %q is declared twice", h.Name)
		}
		seen[h.Name] = true
		if err := c.CheckProjectRef(h.Project); err != nil {
			return fmt.Errorf("hz instance %q: %w", h.Name, err)
		}
		if err := CheckHZURL(h.URL); err != nil {
			return fmt.Errorf("hz instance %q: %w", h.Name, err)
		}
		if h.TokenSHA256 != "" && len(h.TokenSHA256) != 64 {
			return fmt.Errorf("hz instance %q has a malformed token digest", h.Name)
		}
	}
	return c.validateUpstreamLinks()
}

// AddHZInstance declares a nested hz. It writes nothing anywhere but the
// record: the child dials, this hz never does.
func (c *Config) AddHZInstance(h HZInstance) error {
	h = HZInstance{
		Name:      strings.TrimSpace(h.Name),
		Project:   strings.TrimSpace(h.Project),
		URL:       strings.TrimSpace(h.URL),
		VPNClient: strings.TrimSpace(h.VPNClient),
		Note:      strings.TrimSpace(h.Note),
	}
	if _, exists := c.FindHZInstance(h.Name); exists {
		return fmt.Errorf("hz instance %q already exists — `hz instance ls` lists them", h.Name)
	}
	next := c.copyForWrite()
	next.HZInstances = append(next.HZInstances, h)
	sort.Slice(next.HZInstances, func(i, j int) bool { return next.HZInstances[i].Name < next.HZInstances[j].Name })
	if err := next.validateModel(); err != nil {
		return err
	}
	c.adopt(next)
	return nil
}

// HZInstancePatch is a partial edit: nil leaves a field alone, a non-nil ""
// clears it (Project: global; VPNClient: unlink). URL cannot be cleared — an
// instance with no address is the unknown the field exists to replace. The
// token digest is not here: it is minted, never edited.
type HZInstancePatch struct {
	Project   *string
	URL       *string
	VPNClient *string
	Note      *string
}

// SetHZInstance edits an instance in place. It keeps the token digest, so a
// typo fix in the URL never costs the child its credential.
func (c *Config) SetHZInstance(name string, p HZInstancePatch) (HZInstance, error) {
	if p.Project == nil && p.URL == nil && p.VPNClient == nil && p.Note == nil {
		return HZInstance{}, fmt.Errorf("nothing to change on hz instance %q — give a project, a URL, a VPN client or a note", name)
	}
	next := c.copyForWrite()
	idx := -1
	for i, h := range next.HZInstances {
		if h.Name == name {
			idx = i
		}
	}
	if idx < 0 {
		return HZInstance{}, fmt.Errorf("no hz instance %q%s", name, c.hzInstanceHint())
	}
	h := next.HZInstances[idx]
	if p.Project != nil {
		h.Project = strings.TrimSpace(*p.Project)
	}
	if p.URL != nil {
		h.URL = strings.TrimSpace(*p.URL)
	}
	if p.VPNClient != nil {
		h.VPNClient = strings.TrimSpace(*p.VPNClient)
	}
	if p.Note != nil {
		h.Note = strings.TrimSpace(*p.Note)
	}
	next.HZInstances[idx] = h
	if err := next.validateModel(); err != nil {
		return HZInstance{}, err
	}
	c.adopt(next)
	return h, nil
}

// HZInstanceRemoval reports what removing an instance would take (`removes`,
// beginning with the instance) and what stands in the way (`blocked`): its VPN
// client, and every rung whose Upstream names it. Cascade removes the client
// and CLEARS each Upstream — the rung is kept, and reads as placed here.
func (c *Config) HZInstanceRemoval(name string, cascade bool) (removes, blocked []Dependant, err error) {
	h, ok := c.FindHZInstance(name)
	if !ok {
		return nil, nil, fmt.Errorf("no hz instance %q%s", name, c.hzInstanceHint())
	}
	removes = []Dependant{{Kind: "hz-instance", Name: name, How: "the hz instance being removed"}}
	if h.TokenSHA256 != "" {
		removes = append(removes, Dependant{Kind: "credential", Name: name,
			How: "its instance token goes with the record — that hz stops being able to pull from this one"})
	}
	if h.VPNClient != "" {
		d := Dependant{Kind: "vpn-client", Name: h.VPNClient,
			How: "is " + name + "'s VPN client (profile upstream), which would still reach this hz's API for an instance hz no longer declares"}
		if cascade {
			d.How = "is " + name + "'s VPN client (profile upstream), and it would be REMOVED from wg0.conf — that hz loses its tunnel to this one"
			removes = append(removes, d)
		} else {
			blocked = append(blocked, d)
		}
	}
	for _, rung := range c.upstreamUsers(name) {
		d := Dependant{Kind: "environment", Name: rung,
			How: "names " + name + " as its upstream hz, so it would name an instance nobody declares"}
		if cascade {
			d.How = "names " + name + " as its upstream hz; its Upstream would be CLEARED and the rung read as placed here (the rung itself is kept)"
			removes = append(removes, d)
		} else {
			blocked = append(blocked, d)
		}
	}
	sortDependants(blocked)
	sortDependants(removes[1:])
	return removes, blocked, nil
}

// RemoveHZInstance removes the record, and with cascade clears every Upstream
// naming it. Removing the VPN client from wg0.conf is the caller's: wg0 is
// the server's.
func (c *Config) RemoveHZInstance(name string, cascade bool) ([]Dependant, error) {
	removes, blocked, err := c.HZInstanceRemoval(name, cascade)
	if err != nil {
		return nil, err
	}
	if len(blocked) > 0 {
		return nil, blockedError("hz instance", name, blocked)
	}
	next := c.copyForWrite()
	var out []HZInstance
	for _, h := range next.HZInstances {
		if h.Name != name {
			out = append(out, h)
		}
	}
	next.HZInstances = out
	for i := range next.Environments {
		if next.Environments[i].Upstream == name {
			next.Environments[i].Upstream = ""
		}
	}
	if err := next.validateModel(); err != nil {
		return nil, err
	}
	c.adopt(next)
	return removes, nil
}

// upstreamUsers is every rung whose Upstream names this instance, as
// "<project>/<name>", sorted.
func (c *Config) upstreamUsers(instance string) []string {
	var out []string
	for _, e := range c.Environments {
		if e.Upstream == instance {
			out = append(out, e.Project+"/"+e.Name)
		}
	}
	sort.Strings(out)
	return out
}

// hzInstanceHint lists the declared instances — the only names an Upstream
// may take.
func (c *Config) hzInstanceHint() string {
	if len(c.HZInstances) == 0 {
		return ". No hz instance is declared"
	}
	names := make([]string, 0, len(c.HZInstances))
	for _, h := range c.HZInstances {
		names = append(names, h.Name)
	}
	sort.Strings(names)
	return ". Declared hz instances: " + strings.Join(names, ", ")
}
