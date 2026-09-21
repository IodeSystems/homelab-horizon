package dnsmasq

import (
	"fmt"
	"os"
	"path/filepath"
)

// This file is the PRIVILEGED half of the package for *files*: everything hz
// needs write access to /etc/dnsmasq.d for.
//
//   - write dnsmasq.conf
//   - write the records file it includes, and seed it when missing
//   - read the records file back, to see what is actually on the box
//
// Nothing here decides what either file should say — that is render.go, which
// is pure. Installing and poking the systemd unit is a *different* privilege
// and lives in unit.go. When this half moves into hz-agent
// (plan/architecture.md, phase 4, item 10), this list is what moves.

// WriteConfig writes dnsmasq.conf, creating its directory if needed, and seeds
// the records file when it does not exist yet.
//
// The seed matters: dnsmasq.conf ends in a `conf-file=` include, and dnsmasq
// refuses to start when the included file is missing. A fresh box therefore
// needs the empty records file to exist before the unit is started, even though
// nothing has been published yet.
func (d *DNSMasq) WriteConfig() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	dir := filepath.Dir(d.configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	config := RenderConfig(d.configInput())

	if err := os.WriteFile(d.configPath, []byte(config), 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	if _, err := os.Stat(d.hostsPath); os.IsNotExist(err) {
		if err := os.WriteFile(d.hostsPath, []byte(initialHostsFile), 0644); err != nil {
			return fmt.Errorf("failed to create hosts file: %w", err)
		}
	}

	return nil
}

// SetRecords replaces the served records.
//
// The caller supplies the complete desired set; this does not read the file
// back and merge. Config is hz's state of record for DNS
// (config.DeriveDNSRecords) and the generated file says so in its own header —
// which is why the renderer can be pure.
func (d *DNSMasq) SetRecords(records []Record) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	hosts := RenderHosts(d.hostsInput(records))

	if err := os.WriteFile(d.hostsPath, []byte(hosts), 0644); err != nil {
		return fmt.Errorf("failed to write hosts file: %w", err)
	}

	return nil
}

// GetMappings reads the records file back and reports what it actually serves.
//
// This is the package's one read-back, and it is the only thing that can notice
// the file having been edited underneath hz — by hand on the box, or by an
// older hz. Nothing on hz's write path calls it (see SetRecords: the desired
// set always arrives from config), so today it is a diagnostic rather than a
// reconcile: the DNS page and any caller diffing "what we would write" against
// "what is there" go through here.
//
// A missing file is not an error — it is an empty answer, which is what an
// un-provisioned box should report.
func (d *DNSMasq) GetMappings() (map[string]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	body, err := os.ReadFile(d.hostsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}

	return ParseMappings(string(body)), nil
}

// AddMapping adds one wildcard record, preserving whatever else the file
// already serves.
//
// Read-modify-write over the file, so it is the one path where the file *is*
// treated as the state of record. That is deliberate for a single-record edit
// made without a config in hand, and it is why the read and the write are not
// one locked operation — a concurrent SetRecords between them wins, which is
// correct, because SetRecords carries the whole config-derived set.
func (d *DNSMasq) AddMapping(hostname, ip string) error {
	mappings, err := d.GetMappings()
	if err != nil {
		return err
	}

	mappings[hostname] = ip
	return d.SetMappings(mappings)
}

// RemoveMapping drops one record, preserving the rest. Same read-modify-write
// caveat as AddMapping.
func (d *DNSMasq) RemoveMapping(hostname string) error {
	mappings, err := d.GetMappings()
	if err != nil {
		return err
	}

	delete(mappings, hostname)
	return d.SetMappings(mappings)
}

// checkMissingInterfaces compares configured interfaces against what's in the
// config file. An unreadable config reports nothing missing rather than
// everything — Status only calls this once it has seen the file exists.
func (d *DNSMasq) checkMissingInterfaces() []string {
	data, err := os.ReadFile(d.configPath)
	if err != nil {
		return nil
	}
	return missingInterfaces(string(data), d.interfaces)
}
