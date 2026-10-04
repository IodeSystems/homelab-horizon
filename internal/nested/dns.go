package nested

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// The I/O half of the child's DNS request: build the full desired set
// (DNSAsks, from config), POST it to the parent's
// /api/v1/hz-instances/<instance>/dns with the instance token, and keep the
// parent's answer — in memory and in <Dir>/parent-dns.json, so a restart or
// a parent outage shows the LAST answer rather than nothing.
//
// Boot never waits on this, and neither does sync (#5): a failure is
// recorded beside the last answer and the next round retries.

// DNSFile is the persisted last answer under Dir.
const DNSFile = "parent-dns.json"

type dnsMemo struct {
	mu sync.Mutex
	// state is the persisted last answer plus the last attempt.
	state       dnsPersisted
	loaded      bool
	lastSent    string
	lastClean   bool
	lastAttempt int64
	unasked     string
	lastError   string
}

type dnsPersisted struct {
	Records      []apitypes.InstanceDNSRecordStatus `json:"records"`
	Removed      []apitypes.InstanceDNSRemoved      `json:"removed,omitempty"`
	LastAnswerAt int64                              `json:"last_answer_at"`
}

// SendsDNS reports whether this child asks its parent for DNS at all: it
// needs its name on the parent (upstream.instance) and something to ask.
func (c *Child) SendsDNS() bool { return c.Instance != "" && c.DNSAsks != nil }

func (c *Child) dnsPath() string { return filepath.Join(c.Dir, DNSFile) }

func (c *Child) loadDNS() {
	c.dns.mu.Lock()
	defer c.dns.mu.Unlock()
	if c.dns.loaded {
		return
	}
	c.dns.loaded = true
	raw, err := os.ReadFile(c.dnsPath())
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		c.loud("nested hz cannot read its last DNS answer from the parent; it shows none until the next one", "path", c.dnsPath(), "error", err)
		return
	}
	var st dnsPersisted
	if err := json.Unmarshal(raw, &st); err != nil {
		c.loud("nested hz's last DNS answer from the parent does not parse; it shows none until the next one", "path", c.dnsPath(), "error", err)
		return
	}
	c.dns.state = st
}

// DNSStatus is the child's view for its UI: the parent's last answer, the
// last attempt, and why it did not ask when it did not.
func (c *Child) DNSStatus() apitypes.ParentDNSResp {
	out := apitypes.ParentDNSResp{Parent: c.URL, Instance: c.Instance, Records: []apitypes.InstanceDNSRecordStatus{}}
	if c.Instance == "" {
		out.Off = "upstream.instance is not set, so this hz has no name on its parent to ask under"
		return out
	}
	if c.DNSAsks == nil {
		out.Off = "this hz builds no DNS request"
		return out
	}
	out.Enabled = true
	c.loadDNS()
	c.dns.mu.Lock()
	defer c.dns.mu.Unlock()
	out.Records = append(out.Records, c.dns.state.Records...)
	out.Removed = append(out.Removed, c.dns.state.Removed...)
	out.LastAnswerAt = c.dns.state.LastAnswerAt
	out.LastAttemptAt = c.dns.lastAttempt
	out.LastError = c.dns.lastError
	out.Unasked = c.dns.unasked
	return out
}

// maybeSyncDNS is the Run loop's round: ask only when DNSDue says so.
func (c *Child) maybeSyncDNS(ctx context.Context) {
	if !c.SendsDNS() {
		return
	}
	req, unasked := c.DNSAsks()
	c.dns.mu.Lock()
	due := unasked == "" && DNSDue(DNSFingerprint(req), c.dns.lastSent, c.dns.lastAttempt, time.Now().Unix(), c.dns.lastClean)
	c.dns.mu.Unlock()
	if unasked != "" {
		c.setDNSOutcome(unasked, "")
		return
	}
	if due {
		_, _ = c.SyncDNSOnce(ctx)
	}
}

// SyncDNSOnce sends the full desired set now and records the answer. It
// returns the status the UI shows either way; on failure the last answer is
// kept and the error says why.
func (c *Child) SyncDNSOnce(ctx context.Context) (apitypes.ParentDNSResp, error) {
	if !c.SendsDNS() {
		return c.DNSStatus(), nil
	}
	c.loadDNS()
	req, unasked := c.DNSAsks()
	if unasked != "" {
		c.setDNSOutcome(unasked, "")
		return c.DNSStatus(), nil
	}
	fp := DNSFingerprint(req)
	resp, err := c.postDNS(ctx, req)
	now := time.Now().Unix()
	c.dns.mu.Lock()
	c.dns.lastAttempt = now
	c.dns.lastSent = fp
	if err != nil {
		c.dns.lastClean = false
		c.dns.mu.Unlock()
		c.setDNSOutcome("", err.Error())
		return c.DNSStatus(), err
	}
	c.dns.lastClean = DNSAnswerClean(resp)
	c.dns.state = dnsPersisted{Records: resp.Records, Removed: resp.Removed, LastAnswerAt: now}
	st := c.dns.state
	c.dns.mu.Unlock()
	c.setDNSOutcome("", "")
	if raw, merr := json.MarshalIndent(st, "", "  "); merr == nil {
		if werr := writeAtomic(c.dnsPath(), raw); werr != nil {
			c.loudOnce("nested hz could not keep its parent's DNS answer on disk; it is kept in memory", "path", c.dnsPath(), "error", werr)
		}
	}
	for _, r := range resp.Records {
		switch {
		case r.Status == apitypes.InstanceDNSRefused:
			c.loudOnce("nested hz: the parent REFUSED a DNS record — "+DNSLine(r), "parent", c.URL)
		case r.Converted:
			c.Log.Warn("LOUD: nested hz: "+DNSLine(r), "parent", c.URL)
		}
	}
	return c.DNSStatus(), nil
}

func (c *Child) setDNSOutcome(unasked, lastErr string) {
	c.dns.mu.Lock()
	defer c.dns.mu.Unlock()
	c.dns.unasked, c.dns.lastError = unasked, lastErr
}

func (c *Child) postDNS(ctx context.Context, req apitypes.InstanceDNSReq) (apitypes.InstanceDNSResp, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body, err := json.Marshal(req)
	if err != nil {
		return apitypes.InstanceDNSResp{}, err
	}
	hr, err := c.request(ctx, http.MethodPost, apitypes.HZInstancesPath+"/"+url.PathEscape(c.Instance)+apitypes.HZInstanceDNSSuffix, bytes.NewReader(body))
	if err != nil {
		return apitypes.InstanceDNSResp{}, err
	}
	hr.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return apitypes.InstanceDNSResp{}, fmt.Errorf("parent unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return apitypes.InstanceDNSResp{}, fmt.Errorf("parent answered %d: %s", resp.StatusCode, errorBody(resp))
	}
	var out apitypes.InstanceDNSResp
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return apitypes.InstanceDNSResp{}, fmt.Errorf("unreadable DNS answer: %w", err)
	}
	if out.Instance != c.Instance {
		return apitypes.InstanceDNSResp{}, fmt.Errorf("the parent answered for instance %q; this hz is %q", out.Instance, c.Instance)
	}
	return out, nil
}
