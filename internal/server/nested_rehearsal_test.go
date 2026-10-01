package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
	"github.com/iodesystems/homelab-horizon/internal/nested"
)

// The rehearsal, end to end, with two real hz servers: the office hz (parent,
// served over HTTP) and redline-prod-hz (the child, configured with
// `upstream`). redline-ops on prod knows only the CHILD's URL and its own
// service token there.
//
// staging uploads + reports → promote → desired still 404 (held by default)
// → apply → the child serves the artifact → hold → desired carries the hold
// → unhold. And the direction (#1): the address the parent has for the child
// is never dialled, and the child sends the parent nothing but GET desired,
// GET an artifact and POST a report.

const childSvcToken = "svc-redline-prod-on-the-child"

type recordedCall struct{ method, path, auth string }

func TestRehearsalNestedPullApplyHold(t *testing.T) {
	f := newN4aFixture(t)
	tok := f.mint(t, nestedMachine)

	var mu sync.Mutex
	var calls []recordedCall
	parentHandler := f.s.handler()
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, recordedCall{r.Method, r.URL.Path, r.Header.Get("Authorization")})
		mu.Unlock()
		parentHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(parent.Close)

	// The child: its own hz with its own config — the rung declared locally so
	// prod's service token can be attributed to it — and the upstream block.
	childData := t.TempDir()
	tokenFile := filepath.Join(childData, "upstream.token")
	if err := os.WriteFile(tokenFile, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	childCfg := &config.Config{
		Projects:     []config.Project{{Name: "redline"}},
		Environments: []config.Environment{{Project: "redline", Name: "prod", Posture: "prod"}},
		Services: []config.Service{{Name: "redline-prod", Domains: []string{"redline.test"}, Project: "redline",
			Environment: "prod", Token: childSvcToken}},
		Upstream: &config.UpstreamHZ{URL: parent.URL, TokenFile: tokenFile,
			Rungs: []config.UpstreamRung{{Project: "redline", Environment: "prod"}}},
	}
	child := newTestServer(t, childCfg)
	store, err := db.Open(filepath.Join(childData, "hz.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	child.users = store
	child.child = nested.New(*childCfg.Upstream, childData)
	var logs bytes.Buffer
	child.child.Log = slog.New(slog.NewTextHandler(&logs, nil))
	childH := child.handler()
	ctx := context.Background()

	ops := func(method, path string, body any) *httptest.ResponseRecorder {
		var rdr io.Reader = strings.NewReader("")
		if body != nil {
			raw, _ := json.Marshal(body)
			rdr = bytes.NewReader(raw)
		}
		r := httptest.NewRequest(method, path, rdr)
		r.Header.Set("Authorization", "Bearer "+childSvcToken)
		w := httptest.NewRecorder()
		childH.ServeHTTP(w, r)
		return w
	}
	desiredPath := "/api/v1/deploys/desired?" + url.Values{"project": {"redline"}, "environment": {"prod"}}.Encode()

	// Before anything: the child has never reached the parent.
	if w := ops("GET", desiredPath, nil); w.Code != http.StatusServiceUnavailable || !strings.Contains(errorOf(w), "never reached the parent hz") {
		t.Fatalf("before the first poll: %d %q", w.Code, errorOf(w))
	}

	// Staging uploads, then reports; promote.
	sha := f.stageAndPromote(t, "1.4.0", "redline bundle 1.4.0")

	// Promoted, not applied: the parent says nothing applied; so does the child.
	child.child.PollOnce(ctx)
	if w := ops("GET", desiredPath, nil); w.Code != http.StatusNotFound || !strings.Contains(errorOf(w), "nothing applied") {
		t.Fatalf("held by default: %d %q", w.Code, errorOf(w))
	}

	// Apply. The child pulls on its next poll and serves it.
	if w := f.apply("prod", "1.4.0"); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	child.child.PollOnce(ctx)
	w := ops("GET", desiredPath, nil)
	var d apitypes.DesiredResp
	_ = json.NewDecoder(w.Body).Decode(&d)
	if w.Code != http.StatusOK || d.Version != "1.4.0" || d.ArtifactSHA256 != sha || d.Hold != nil {
		t.Fatalf("after apply, the child answers %d %+v", w.Code, d)
	}
	if w := ops("GET", "/api/v1/artifacts/"+sha, nil); w.Code != http.StatusOK || w.Body.String() != "redline bundle 1.4.0" {
		t.Fatalf("the child's artifact: %d %q", w.Code, w.Body.String())
	}

	// Hold: desired carries it, through the child.
	hw := f.call(f.s.handleAPIEnvironmentHold(true), http.MethodPost, "/api/v1/environments/hold", f.pat,
		apitypes.HoldReq{Project: "redline", Environment: "prod", Reason: "cutover at 02:00"})
	if hw.Code != http.StatusOK {
		t.Fatal(hw.Body.String())
	}
	child.child.PollOnce(ctx)
	w = ops("GET", desiredPath, nil)
	d = apitypes.DesiredResp{}
	_ = json.NewDecoder(w.Body).Decode(&d)
	if d.Hold == nil || d.Hold.Reason != "cutover at 02:00" {
		t.Fatalf("held: the child answers %+v", d)
	}

	// Unhold.
	if uw := f.call(f.s.handleAPIEnvironmentHold(false), http.MethodPost, "/api/v1/environments/unhold", f.pat,
		apitypes.HoldReq{Project: "redline", Environment: "prod"}); uw.Code != http.StatusOK {
		t.Fatal(uw.Body.String())
	}
	child.child.PollOnce(ctx)
	w = ops("GET", desiredPath, nil)
	d = apitypes.DesiredResp{}
	_ = json.NewDecoder(w.Body).Decode(&d)
	if w.Code != http.StatusOK || d.Hold != nil || d.Version != "1.4.0" {
		t.Fatalf("unheld: %d %+v", w.Code, d)
	}

	// Prod reports to the CHILD; the child records it and forwards it.
	rep := apitypes.DeployReportReq{Project: "redline", Environment: "prod", App: "redline", Version: "1.4.0",
		ArtifactSHA256: sha, Host: "prod-1"}
	rw := ops("POST", "/api/v1/deploys/report", rep)
	var rr apitypes.DeployReportResultResp
	_ = json.NewDecoder(rw.Body).Decode(&rr)
	if rw.Code != http.StatusOK || !rr.Recorded || rr.Upstream != "queued" {
		t.Fatalf("local report: %d %+v %s", rw.Code, rr, rw.Body.String())
	}
	if local, err := store.LatestDeployReport(ctx, "redline", "prod"); err != nil || local.ReportedBy != "service:redline-prod" {
		t.Fatalf("the child's own record: %+v %v", local, err)
	}
	if err := child.child.DrainOnce(ctx); err != nil {
		t.Fatal(err)
	}
	up, err := f.s.users.LatestDeployReport(ctx, "redline", "prod")
	if err != nil || up.ReportedBy != "instance:"+nestedMachine+" (for service:redline-prod)" {
		t.Fatalf("the parent's record: %+v %v", up, err)
	}

	// The parent goes away: the child keeps answering, with the header.
	parent.Close()
	child.child.PollOnce(ctx)
	w = ops("GET", desiredPath, nil)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get(nested.HeaderUpstream), "unreachable since ") {
		t.Fatalf("parent gone: %d header %q", w.Code, w.Header().Get(nested.HeaderUpstream))
	}

	// The direction. Positive control first: calls were recorded at all.
	mu.Lock()
	defer mu.Unlock()
	instanceCalls := 0
	for _, c := range calls {
		if c.auth != "Bearer "+tok {
			continue
		}
		instanceCalls++
		switch {
		case c.method == "GET" && c.path == "/api/v1/deploys/desired",
			c.method == "GET" && strings.HasPrefix(c.path, "/api/v1/artifacts/"),
			c.method == "POST" && c.path == "/api/v1/deploys/report":
		default:
			t.Errorf("the child sent the parent %s %s", c.method, c.path)
		}
	}
	if instanceCalls < 4 {
		t.Fatalf("instrument: the parent recorded %d instance calls: %+v", instanceCalls, calls)
	}
	if n := f.childHits.count(); n != 0 {
		t.Fatalf("the parent DIALLED the child's address %d time(s) — the child pulls, the parent never initiates", n)
	}

	// The child's local scope: the parent's
	// instance token and no token are refused; forwarded_for from a local
	// caller is refused.
	for name, c := range map[string]struct {
		token string
		want  int
	}{
		"no token":       {"", http.StatusUnauthorized},
		"instance token": {tok, http.StatusUnauthorized}, // the child does not know the token it holds for the parent
		"unknown token":  {"nope", http.StatusUnauthorized},
	} {
		r := httptest.NewRequest("GET", desiredPath, nil)
		if c.token != "" {
			r.Header.Set("Authorization", "Bearer "+c.token)
		}
		w := httptest.NewRecorder()
		childH.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("child desired with %s: %d, want %d", name, w.Code, c.want)
		}
	}
	bad := rep
	bad.ForwardedFor = "service:someone"
	if w := ops("POST", "/api/v1/deploys/report", bad); w.Code != http.StatusBadRequest {
		t.Errorf("a local report carrying forwarded_for: %d", w.Code)
	}
}
