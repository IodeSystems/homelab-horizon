// Package dnsmasq computes and applies the gateway's dnsmasq configuration.
//
// The package is split along one seam, and the split is load-bearing for the
// hz-agent work (plan/architecture.md, "hz-agent de-roots the hz web surface"):
//
//	render.go   pure     desired state in, bytes out. No files, no commands,
//	                     no clock, no environment. Runs anywhere, as anyone.
//	apply.go    root     the file side effects: write dnsmasq.conf and the
//	                     records file, and read the records file back.
//	unit.go     root+    a *different* privilege: install and drive the
//	                     systemd unit. Kept apart from apply.go on purpose.
//	dnsmasq.go  manager  holds the desired state and joins the halves.
//	probe.go    observe  reads interfaces and asks resolvers questions.
//	forward.go  observe  "does the forwarder forward" health check.
//	stats.go    observe  dnsmasq's own counters, for /metrics.
//
// Anything that computes what a config *should* be belongs in render.go, so
// that half can later run in an unprivileged hz web process while apply.go's
// and unit.go's short lists of side effects move to the agent. seam_test.go
// guards that.
//
// On state of record: the records file is an output, not an input. Every hz
// write path hands SetRecords the complete set derived from config
// (config.DeriveDNSRecords), and the generated file says as much in its own
// header. GetMappings reads it back for the other direction — what is actually
// on the box — and AddMapping/RemoveMapping are the one read-modify-write pair,
// for single-record edits made without a config in hand.
package dnsmasq

import (
	"strings"
	"sync"
)

// DNSMasq is the manager: it holds the desired state, and joins the pure
// renderer to the privileged halves. It is the only thing in the package that
// knows both where the files live and what should be in them.
type DNSMasq struct {
	mu         sync.Mutex
	configPath string
	hostsPath  string
	// localDomain, when set, is appended to bare host records so a single
	// record answers for both "desktop" and "desktop.<domain>".
	localDomain string
	interfaces  []string
	upstream    []string
}

func New(configPath, hostsPath string, interfaces []string, upstream []string) *DNSMasq {
	return &DNSMasq{
		configPath: configPath,
		hostsPath:  hostsPath,
		interfaces: interfaces,
		upstream:   upstream,
	}
}

// configInput gathers the manager's desired state into the pure renderer's
// input for dnsmasq.conf. Unlike HAProxy's, it reads nothing from the machine
// at all — every field is state the manager was handed.
//
// Caller must hold d.mu.
func (d *DNSMasq) configInput() ConfigInput {
	return ConfigInput{
		ConfigPath:  d.configPath,
		HostsPath:   d.hostsPath,
		Interfaces:  d.interfaces,
		Upstream:    d.upstream,
		LocalDomain: d.localDomain,
	}
}

// hostsInput pairs the caller's complete record set with the manager's local
// domain, which is the only thing the renderer needs that the caller does not
// supply.
//
// Caller must hold d.mu.
func (d *DNSMasq) hostsInput(records []Record) HostsInput {
	return HostsInput{Records: records, LocalDomain: d.localDomain}
}

// GenerateConfig returns the contents WriteConfig would write to dnsmasq.conf.
//
// The preview accessor, and the one hz-agent's desired state is built from
// (internal/server/handlers_agent.go). WriteConfig calls it rather than
// rendering a second time, so "what the agent is told to write" and "what hz
// would write itself" cannot become two different answers.
func (d *DNSMasq) GenerateConfig() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.renderConfigLocked()
}

// renderConfigLocked is the one render call. Caller must hold d.mu — which
// WriteConfig already does, so it takes this door and GenerateConfig takes the
// locking one. Neither renders independently.
func (d *DNSMasq) renderConfigLocked() string { return RenderConfig(d.configInput()) }

// renderRecordsLocked is the same arrangement for the records file.
// Caller must hold d.mu.
func (d *DNSMasq) renderRecordsLocked(records []Record) string {
	return RenderHosts(d.hostsInput(records))
}

// GenerateRecords returns the contents SetRecords would write for this record
// set. Same no-drift reason as GenerateConfig.
func (d *DNSMasq) GenerateRecords(records []Record) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.renderRecordsLocked(records)
}

// SetLocalDomain sets the domain appended to bare host records. Empty disables
// expansion, which is the previous behaviour.
func (d *DNSMasq) SetLocalDomain(domain string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.localDomain = strings.ToLower(strings.TrimSpace(domain))
}

// SetMappings replaces the served records, treating every entry as a wildcard.
//
// Kept because that is what hz has always written for service domains, and
// narrowing them to exact-match now would silently stop answering for any
// subdomain someone has come to rely on. New callers should prefer SetRecords.
func (d *DNSMasq) SetMappings(mappings map[string]string) error {
	records := make([]Record, 0, len(mappings))
	for name, ip := range mappings {
		records = append(records, Record{Name: name, IP: ip, Wildcard: true})
	}
	return d.SetRecords(records)
}

func (d *DNSMasq) UpdateUpstream(servers []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.upstream = servers
}

func (d *DNSMasq) UpdateInterfaces(interfaces []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.interfaces = interfaces
}
