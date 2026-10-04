package nested

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// dnsParent answers /api/v1/hz-instances/child/dns like the real parent:
// every record accepted and published, except names under refused.example.com.
type dnsParent struct {
	srv   *httptest.Server
	mu    sync.Mutex
	posts []apitypes.InstanceDNSReq
	down  bool
}

func newDNSParent(t *testing.T) *dnsParent {
	p := &dnsParent{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.down {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "the parent is restarting"})
			return
		}
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+parentToken || r.URL.Path != "/api/v1/hz-instances/"+certInstance+"/dns" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var req apitypes.InstanceDNSReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		p.posts = append(p.posts, req)
		out := apitypes.InstanceDNSResp{Instance: certInstance}
		for _, rec := range req.Records {
			st := apitypes.InstanceDNSRecordStatus{Name: rec.Name, Type: rec.Type, Values: rec.Values, Status: apitypes.InstanceDNSPublished}
			if strings.HasSuffix(rec.Name, ".refused.example.com") {
				st.Status, st.Reason = apitypes.InstanceDNSRefused, rec.Name+" is outside hz instance child's DNS grant"
			}
			out.Records = append(out.Records, st)
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *dnsParent) count() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.posts) }

func asking(recs ...apitypes.InstanceDNSRecordReq) func() (apitypes.InstanceDNSReq, string) {
	return func() (apitypes.InstanceDNSReq, string) {
		return apitypes.InstanceDNSReq{Records: recs, PublicIP: "203.0.113.20"}, ""
	}
}

func TestChildAsksTheParentAndKeepsTheAnswer(t *testing.T) {
	p := newDNSParent(t)
	c := newCertChild(t, p.srv.URL)
	c.DNSAsks = asking(
		apitypes.InstanceDNSRecordReq{Name: "app.hz.loadtest.example.com", Type: "A", Values: []string{"203.0.113.20"}},
		apitypes.InstanceDNSRecordReq{Name: "x.refused.example.com", Type: "A", Values: []string{"203.0.113.20"}},
	)
	st, err := c.SyncDNSOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || len(st.Records) != 2 || st.LastAnswerAt == 0 || st.LastError != "" {
		t.Fatalf("status: %+v", st)
	}
	lines := map[string]string{}
	for _, r := range st.Records {
		lines[r.Name] = DNSLine(r)
	}
	if !strings.HasSuffix(lines["app.hz.loadtest.example.com"], ": published by parent") ||
		!strings.Contains(lines["x.refused.example.com"], ": refused: x.refused.example.com is outside") {
		t.Fatalf("lines: %v", lines)
	}
	if p.posts[0].PublicIP != "203.0.113.20" || len(p.posts[0].Records) != 2 {
		t.Fatalf("posted: %+v", p.posts[0])
	}

	// A parent outage: the sync returns (it does not block), the error is
	// recorded, and the last answer is kept.
	p.mu.Lock()
	p.down = true
	p.mu.Unlock()
	st, err = c.SyncDNSOnce(context.Background())
	if err == nil || !strings.Contains(st.LastError, "502") {
		t.Fatalf("outage: %v %+v", err, st)
	}
	if len(st.Records) != 2 || st.Records[0].Status != apitypes.InstanceDNSPublished {
		t.Fatalf("the last answer was not kept: %+v", st.Records)
	}

	// A restart: the answer is read back from disk.
	fresh := &Child{URL: c.URL, Instance: c.Instance, Dir: c.Dir, Log: c.Log, DNSAsks: c.DNSAsks}
	if st := fresh.DNSStatus(); len(st.Records) != 2 || st.LastAnswerAt == 0 {
		t.Fatalf("after a restart: %+v", st)
	}
}

func TestChildAsksNothingWhileTheSetIsUnknown(t *testing.T) {
	p := newDNSParent(t)
	c := newCertChild(t, p.srv.URL)
	c.DNSAsks = func() (apitypes.InstanceDNSReq, string) {
		return apitypes.InstanceDNSReq{}, "this hz's public IP is unknown"
	}
	st, err := c.SyncDNSOnce(context.Background())
	if err != nil || p.count() != 0 {
		t.Fatalf("asked anyway: %v, %d posts", err, p.count())
	}
	if !strings.Contains(st.Unasked, "public IP is unknown") {
		t.Fatalf("status: %+v", st)
	}
	c.maybeSyncDNS(context.Background())
	if p.count() != 0 {
		t.Fatal("the loop asked with an unknown set")
	}
}

func TestChildWithoutAnInstanceNameAsksNothing(t *testing.T) {
	p := newDNSParent(t)
	c := newCertChild(t, p.srv.URL)
	c.Instance = ""
	c.DNSAsks = asking()
	if st := c.DNSStatus(); st.Enabled || !strings.Contains(st.Off, "upstream.instance") {
		t.Fatalf("status: %+v", st)
	}
	c.maybeSyncDNS(context.Background())
	if p.count() != 0 {
		t.Fatal("asked with no instance name")
	}
}

// The loop re-sends a changed set at once, an unchanged clean one only on
// the refresh, and a pending or failed one on the retry.
func TestDNSDue(t *testing.T) {
	const now = 10_000
	for _, tc := range []struct {
		name          string
		fp, last      string
		lastAt        int64
		clean, wantGo bool
	}{
		{"never sent", "a", "", 0, true, true},
		{"changed", "b", "a", now - 1, true, true},
		{"unchanged, clean, recent", "a", "a", now - 60, true, false},
		{"unchanged, clean, stale", "a", "a", now - DNSRefreshSeconds, true, true},
		{"unchanged, pending, retry not yet", "a", "a", now - 30, false, false},
		{"unchanged, pending, retry due", "a", "a", now - DNSRetrySeconds, false, true},
	} {
		if got := DNSDue(tc.fp, tc.last, tc.lastAt, now, tc.clean); got != tc.wantGo {
			t.Errorf("%s: DNSDue = %v", tc.name, got)
		}
	}
	a := apitypes.InstanceDNSReq{Records: []apitypes.InstanceDNSRecordReq{
		{Name: "a.example.com", Type: "A", Values: []string{"203.0.113.2", "203.0.113.1"}},
		{Name: "b.example.com", Type: "A", Values: []string{"203.0.113.1"}},
	}}
	b := apitypes.InstanceDNSReq{Records: []apitypes.InstanceDNSRecordReq{
		{Name: "B.example.com", Type: "a", Values: []string{"203.0.113.1"}},
		{Name: "a.example.com", Type: "A", Values: []string{"203.0.113.1", "203.0.113.2"}},
	}}
	if DNSFingerprint(a) != DNSFingerprint(b) {
		t.Fatal("the same set in another order fingerprints differently")
	}
	b.PublicIP = "203.0.113.9"
	if DNSFingerprint(a) == DNSFingerprint(b) {
		t.Fatal("a changed address does not change the fingerprint")
	}
}
