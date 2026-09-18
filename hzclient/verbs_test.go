package hzclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/hzapi"
)

// fakeHZ answers the real routes with the real wire shapes, so a verb test
// exercises the path, the method and the body as well as the decode.
type fakeHZ struct {
	status  DeployStatus
	bans    []BanEntry
	release []SiteRelease

	// what the last request carried, for the assertions that care
	lastPath   string
	lastMethod string
	lastBody   []byte
}

func (f *fakeHZ) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.lastPath, f.lastMethod, f.lastBody = r.URL.Path, r.Method, body
		hzapi.Advertise(w.Header())
		w.Header().Set("Content-Type", "application/json")

		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch {
		case r.URL.Path == "/api/deploy/status":
			write(f.status)
		case r.URL.Path == "/api/deploy/swap":
			write(DeploySwapResponse{Status: "ok", ActiveSlot: "b", Current: "10.0.0.1:9002", Next: "10.0.0.1:9001"})
		case r.URL.Path == "/api/deploy/maint-page/set":
			write(map[string]string{"status": "ok", "maintenance_page_md5": "d41d8cd98f00b204e9800998ecf8427e"})
		case r.URL.Path == "/api/deploy/maint-page/clear":
			write(map[string]string{"status": "ok"})
		case strings.HasPrefix(r.URL.Path, "/api/deploy/"):
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/deploy/"), "/")
			if len(parts) != 2 {
				http.Error(w, "unknown action", http.StatusBadRequest)
				return
			}
			write(DeployStateChangeResponse{Status: "ok", Server: parts[0], State: parts[1]})
		case r.URL.Path == "/api/ban/ban", r.URL.Path == "/api/ban/unban":
			write(OKResponse{OK: true})
		case r.URL.Path == "/api/ban/list":
			write(BanListResponse{Bans: f.bans})
		case r.URL.Path == "/api/site/rollback":
			write(map[string]string{"rolledBackTo": "20260101T000001Z"})
		case r.URL.Path == "/api/site/releases":
			write(f.release)
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
		}
	}
}

func newFake(t *testing.T) (*Client, *fakeHZ) {
	t.Helper()
	f := &fakeHZ{status: upUpStatus()}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	c, err := New(Options{BaseURL: srv.URL, Token: "tok-123"})
	if err != nil {
		t.Fatal(err)
	}
	return c, f
}

func upUpStatus() DeployStatus {
	return DeployStatus{
		Service: "app", Domain: "app.example.com", ActiveSlot: "a",
		Balance: "first", HealthCheck: "/healthz",
		Current: DeploySlotStatus{Slot: "a", Backend: "10.0.0.1:9001", State: "up"},
		Next:    DeploySlotStatus{Slot: "b", Backend: "10.0.0.1:9002", State: "up"},
	}
}

func TestDeployStatusDecodesTheWholeShape(t *testing.T) {
	c, f := newFake(t)
	f.status.MaintenancePageMD5 = "abc123"
	f.status.Domains = []string{"app.example.com", "www.example.com"}

	got, err := c.DeployStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Service != "app" || got.ActiveSlot != "a" || got.HealthCheck != "/healthz" {
		t.Errorf("top-level fields lost: %+v", got)
	}
	if got.Current.Backend != "10.0.0.1:9001" || got.Next.State != "up" {
		t.Errorf("slot fields lost: %+v", got)
	}
	if got.MaintenancePageMD5 != "abc123" || len(got.Domains) != 2 {
		t.Errorf("optional fields lost: %+v", got)
	}
	if got.Slot(SlotCurrent) != got.Current || got.Slot(SlotNext) != got.Next {
		t.Error("Slot does not address slots by role")
	}
}

// hz gaining a field must not break a consumer compiled before it existed.
// That is what the version header is for; refusing here would make every
// additive change breaking.
func TestUnknownServerFieldsAreIgnored(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"service":"app","active_slot":"a","somethingNew":{"deep":1}}`))
	})
	got, err := c.DeployStatus(context.Background())
	if err != nil {
		t.Fatalf("a field this build has never heard of broke the decode: %v", err)
	}
	if got.Service != "app" {
		t.Errorf("got %+v", got)
	}
}

func TestSetSlotStateHitsTheRightPath(t *testing.T) {
	c, f := newFake(t)
	for _, tc := range []struct {
		slot  SlotName
		state SlotState
		path  string
	}{
		{SlotCurrent, StateUp, "/api/deploy/current/up"},
		{SlotCurrent, StateDrain, "/api/deploy/current/drain"},
		{SlotCurrent, StateDown, "/api/deploy/current/down"},
		{SlotNext, StateUp, "/api/deploy/next/up"},
		{SlotNext, StateDrain, "/api/deploy/next/drain"},
		{SlotNext, StateDown, "/api/deploy/next/down"},
	} {
		resp, err := c.SetSlotState(context.Background(), tc.slot, tc.state)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.slot, tc.state, err)
		}
		if f.lastPath != tc.path || f.lastMethod != http.MethodPost {
			t.Errorf("%s %s went to %s %s, want POST %s", tc.slot, tc.state, f.lastMethod, f.lastPath, tc.path)
		}
		if resp.Server != string(tc.slot) || resp.State != string(tc.state) {
			t.Errorf("response did not echo the change: %+v", resp)
		}
	}
}

// The two arguments are distinct types so they cannot be transposed, and a
// value outside the set is refused before a request goes out.
func TestSetSlotStateRefusesNonsenseWithoutACall(t *testing.T) {
	c, f := newFake(t)
	if _, err := c.SetSlotState(context.Background(), SlotName("sideways"), StateUp); err == nil {
		t.Error("an unknown slot was sent to hz")
	}
	if _, err := c.SetSlotState(context.Background(), SlotNext, SlotState("sideways")); err == nil {
		t.Error("an unknown state was sent to hz")
	}
	if f.lastPath != "" {
		t.Errorf("a refused call still reached hz at %s", f.lastPath)
	}
}

func TestDeploySwapReportsTheLabelsAfterwards(t *testing.T) {
	c, f := newFake(t)
	got, err := c.DeploySwap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != http.MethodPost || f.lastPath != "/api/deploy/swap" {
		t.Errorf("went to %s %s", f.lastMethod, f.lastPath)
	}
	if got.ActiveSlot != "b" || got.Current != "10.0.0.1:9002" {
		t.Errorf("swap response lost fields: %+v", got)
	}
}

func TestMaintPageSetSendsHTMLAndReturnsTheMD5(t *testing.T) {
	c, f := newFake(t)
	got, err := c.MaintPageSet(context.Background(), "<h1>brb</h1>")
	if err != nil {
		t.Fatal(err)
	}
	var sent map[string]string
	if err := json.Unmarshal(f.lastBody, &sent); err != nil {
		t.Fatalf("body was not JSON: %s", f.lastBody)
	}
	if sent["html"] != "<h1>brb</h1>" {
		t.Errorf("body carried %q", sent["html"])
	}
	if got.MaintenancePageMD5 == "" {
		t.Error("the md5 that confirms hz stored the bytes was dropped")
	}
}

// Clearing is a separate verb, so an empty string here is a caller that meant
// to clear — or a truncated read. Refusing beats silently wiping the page.
func TestMaintPageSetRefusesAnEmptyPage(t *testing.T) {
	c, f := newFake(t)
	if _, err := c.MaintPageSet(context.Background(), ""); err == nil {
		t.Fatal("an empty page was accepted")
	}
	if f.lastPath != "" {
		t.Errorf("it still called %s", f.lastPath)
	}
}

func TestMaintPageClearCarriesNoMD5(t *testing.T) {
	c, f := newFake(t)
	got, err := c.MaintPageClear(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/api/deploy/maint-page/clear" {
		t.Errorf("went to %s", f.lastPath)
	}
	if got.MaintenancePageMD5 != "" {
		t.Errorf("a cleared page reported an md5: %+v", got)
	}
}

func TestBanAddSendsSecondsAndReason(t *testing.T) {
	c, f := newFake(t)
	if err := c.BanAdd(context.Background(), "1.2.3.4", time.Hour, "brute force"); err != nil {
		t.Fatal(err)
	}
	var sent BanRequest
	if err := json.Unmarshal(f.lastBody, &sent); err != nil {
		t.Fatalf("body was not JSON: %s", f.lastBody)
	}
	if sent.IP != "1.2.3.4" || sent.Timeout != 3600 || sent.Reason != "brute force" {
		t.Errorf("sent %+v, want 3600 seconds", sent)
	}

	// Zero is permanent, and travels as an absent field because hz's own type
	// is omitempty. That is hz's meaning, not this package's.
	if err := c.BanAdd(context.Background(), "1.2.3.4", 0, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(f.lastBody), "timeout") {
		t.Errorf("a permanent ban sent a timeout field: %s", f.lastBody)
	}
}

// A sub-second duration truncates to zero, and zero means PERMANENT. Silently
// turning 500ms into a permanent ban is exactly the class of bug a typed client
// is for.
func TestBanAddRefusesASubSecondTimeout(t *testing.T) {
	c, f := newFake(t)
	err := c.BanAdd(context.Background(), "1.2.3.4", 500*time.Millisecond, "")
	if err == nil {
		t.Fatal("500ms was accepted and would have become a permanent ban")
	}
	if !strings.Contains(err.Error(), "PERMANENT") {
		t.Errorf("the refusal does not say what would have happened: %v", err)
	}
	if err := c.BanAdd(context.Background(), "1.2.3.4", -time.Second, ""); err == nil {
		t.Error("a negative timeout was accepted")
	}
	if f.lastPath != "" {
		t.Errorf("a refused ban still reached hz at %s", f.lastPath)
	}
}

func TestBanRemoveSendsTheIP(t *testing.T) {
	c, f := newFake(t)
	if err := c.BanRemove(context.Background(), "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	if f.lastPath != "/api/ban/unban" {
		t.Errorf("went to %s", f.lastPath)
	}
	var sent UnbanRequest
	_ = json.Unmarshal(f.lastBody, &sent)
	if sent.IP != "1.2.3.4" {
		t.Errorf("sent %+v", sent)
	}
	if err := c.BanRemove(context.Background(), ""); err == nil {
		t.Error("an empty IP was sent to hz")
	}
}

// A 200 is not the whole answer: hz says ok, and a body saying otherwise is hz
// declining.
func TestBanCallsInsistOnOK(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false}`))
	})
	if err := c.BanAdd(context.Background(), "1.2.3.4", 0, ""); err == nil {
		t.Fatal("ok=false was treated as success")
	}
}

// THE BUG THIS PACKAGE EXISTS TO END.
//
// bin/hz-client read created_at/expires_at while the server marshalled
// createdAt/expiresAt, so every ban it ever listed printed "created=-
// expires=never" — for as long as the file existed, past review, past a drift
// test that only compared the script to its own copy.
//
// So this decodes a real server-shaped payload and asserts the timestamps
// arrive NON-ZERO. A test that merely constructed a BanEntry and checked it
// compiled would have passed against the broken names too.
func TestBanListDecodesServerTimestamps(t *testing.T) {
	// Exactly what internal/apitypes.BanEntry marshals to.
	const serverPayload = `{"bans":[
		{"ip":"1.2.3.4","timeout":3600,"createdAt":1757000000,"expiresAt":1757003600,"reason":"brute force","service":"app"},
		{"ip":"5.6.7.8","createdAt":1757000123,"reason":"permanent"}
	]}`
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(serverPayload))
	})
	bans, err := c.BanList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(bans) != 2 {
		t.Fatalf("got %d bans, want 2", len(bans))
	}

	first := bans[0]
	if first.CreatedAt == 0 {
		t.Error("createdAt decoded as zero — this is the created=- bug, again")
	}
	if first.ExpiresAt == 0 {
		t.Error("expiresAt decoded as zero — this is the expires=never bug, again")
	}
	if got, ok := first.Created(); !ok || got != 1757000000 {
		t.Errorf("Created() = %d, %v", got, ok)
	}
	if got, ok := first.Expires(); !ok || got != 1757003600 {
		t.Errorf("Expires() = %d, %v", got, ok)
	}
	if first.IP != "1.2.3.4" || first.Timeout != 3600 || first.Reason != "brute force" || first.Service != "app" {
		t.Errorf("other fields lost: %+v", first)
	}

	// A permanent ban carries no expiry at all, which is a different fact from
	// "expires at the epoch".
	second := bans[1]
	if _, ok := second.Expires(); ok {
		t.Error("a permanent ban reported an expiry")
	}
	if _, ok := second.Created(); !ok {
		t.Error("a permanent ban lost its creation time")
	}
}

func TestBanListEmpty(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"bans":null}`))
	})
	bans, err := c.BanList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(bans) != 0 {
		t.Errorf("got %d bans", len(bans))
	}
}

func TestSiteRollbackNamesTheReleaseNowLive(t *testing.T) {
	c, f := newFake(t)
	got, err := c.SiteRollback(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != http.MethodPost || f.lastPath != "/api/site/rollback" {
		t.Errorf("went to %s %s", f.lastMethod, f.lastPath)
	}
	if got.RolledBackTo != "20260101T000001Z" {
		t.Errorf("rolledBackTo decoded as %q", got.RolledBackTo)
	}
}

// A site deployed exactly once has nothing to roll back to, and hz says so with
// a 400. That is an ordinary state, so it must arrive readable.
func TestSiteRollbackWithNoPreviousRelease(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no previous release to roll back to", http.StatusBadRequest)
	})
	_, err := c.SiteRollback(context.Background())
	var se *StatusError
	if !errors.As(err, &se) || !strings.Contains(se.Body, "no previous release") {
		t.Fatalf("got %v", err)
	}
}

func TestSiteReleasesIsNewestFirstWithTheLiveOneMarked(t *testing.T) {
	c, f := newFake(t)
	f.release = []SiteRelease{
		{ID: "20260101T000002Z", Current: true},
		{ID: "20260101T000001Z"},
	}
	got, err := c.SiteReleases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != http.MethodGet || f.lastPath != "/api/site/releases" {
		t.Errorf("went to %s %s", f.lastMethod, f.lastPath)
	}
	if len(got) != 2 || !got[0].Current || got[1].Current || got[0].ID != "20260101T000002Z" {
		t.Errorf("releases decoded as %+v", got)
	}
}
