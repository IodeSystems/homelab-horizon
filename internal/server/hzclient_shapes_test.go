package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/iodesystems/homelab-horizon/hzclient"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/sitedeploy"
)

// hzclient mirrors hz's wire shapes rather than importing internal/apitypes, so
// that giving it its own go.mod stays possible — a nested module cannot import
// its parent's internal tree, and that option is what would keep a consumer
// from inheriting hz's whole dependency graph. configmgr made the same call;
// see configmgr_shapes_test.go, whose jsonShape/assertSameShape helpers this
// file reuses.
//
// The cost of mirroring is two definitions of one JSON shape. THESE TESTS ARE
// WHY THAT COST IS BOUNDED, and the reason they exist is not theoretical:
// bin/hz-client read created_at/expires_at while the server marshalled
// createdAt/expiresAt, so every ban it listed printed "created=- expires=never"
// for as long as the file existed. Nothing caught it, because the only drift
// test compared the script to its own copy.

func TestHZClientShapesMatchAPITypes(t *testing.T) {
	assertSameShape(t, "deploy slot status", hzclient.DeploySlotStatus{}, apitypes.DeploySlotStatus{})
	assertSameShape(t, "deploy status", hzclient.DeployStatus{}, apitypes.DeployStatus{})
	assertSameShape(t, "deploy state change", hzclient.DeployStateChangeResponse{}, apitypes.DeployStateChangeResponse{})
	assertSameShape(t, "deploy swap", hzclient.DeploySwapResponse{}, apitypes.DeploySwapResponse{})
	assertSameShape(t, "ban request", hzclient.BanRequest{}, apitypes.BanRequest{})
	assertSameShape(t, "unban request", hzclient.UnbanRequest{}, apitypes.UnbanRequest{})
	assertSameShape(t, "ban entry", hzclient.BanEntry{}, apitypes.BanEntry{})
	assertSameShape(t, "ban list", hzclient.BanListResponse{}, apitypes.BanListResponse{})
	assertSameShape(t, "ok", hzclient.OKResponse{}, apitypes.OKResponse{})

	// The site release list is not an apitypes shape: the handler writes
	// sitedeploy's own type straight out.
	assertSameShape(t, "site release", hzclient.SiteRelease{}, sitedeploy.Release{})
}

// Shape equality is necessary but not sufficient: it would pass with a type
// changed on one side only. The status is the shape every rolling step reads,
// so it round-trips through hz's own type here.
func TestDeployStatusRoundTripsIntoTheClient(t *testing.T) {
	src := apitypes.DeployStatus{
		Service: "app", Domain: "app.example.com",
		Domains:    []string{"app.example.com", "www.example.com"},
		ActiveSlot: "b", Balance: "roundrobin", HealthCheck: "/healthz",
		Current:            apitypes.DeploySlotStatus{Slot: "b", Backend: "10.0.0.1:9002", State: "up"},
		Next:               apitypes.DeploySlotStatus{Slot: "a", Backend: "10.0.0.1:9001", State: "maint"},
		MaintenancePageMD5: "d41d8cd98f00b204e9800998ecf8427e",
	}
	raw, err := json.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	var dst hzclient.DeployStatus
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&dst); err != nil {
		t.Fatalf("hz's own deploy status does not decode as the client's type: %v", err)
	}
	if dst.Service != src.Service || dst.ActiveSlot != "b" || dst.HealthCheck != "/healthz" ||
		dst.Current.Backend != "10.0.0.1:9002" || dst.Next.State != "maint" ||
		dst.MaintenancePageMD5 == "" || len(dst.Domains) != 2 {
		t.Fatalf("round trip lost or changed a field: %+v", dst)
	}
	// And the phase machine reads what it needs off it.
	if got := hzclient.PhaseFrom(&dst); got.Phase != hzclient.PhaseNextDown {
		t.Fatalf("current=up next=maint inferred as %s", got)
	}
}

// THE BANS BUG, pinned end to end.
//
// This drives hz's real conversion and asserts the timestamps arrive NON-ZERO
// in the client's type. A test that only compared field tags would pass against
// a mirror that had kept the script's created_at, because tags matching is not
// the same as a value surviving; a test that only constructed a BanEntry would
// pass against anything at all.
func TestBanListTimestampsSurviveIntoTheClient(t *testing.T) {
	const created, expires = int64(1757000000), int64(1757003600)
	entries := banListEntries([]config.IPBan{
		{IP: "1.2.3.4", Timeout: 3600, CreatedAt: created, ExpiresAt: expires, Reason: "brute force", Service: "app"},
		{IP: "5.6.7.8", CreatedAt: created, Reason: "permanent"},
	})
	raw, err := json.Marshal(apitypes.BanListResponse{Bans: entries})
	if err != nil {
		t.Fatal(err)
	}

	var got hzclient.BanListResponse
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("hz's own ban list does not decode as the client's type: %v", err)
	}
	if len(got.Bans) != 2 {
		t.Fatalf("got %d bans", len(got.Bans))
	}
	if got.Bans[0].CreatedAt != created {
		t.Errorf("createdAt arrived as %d, want %d — this is the created=- bug", got.Bans[0].CreatedAt, created)
	}
	if got.Bans[0].ExpiresAt != expires {
		t.Errorf("expiresAt arrived as %d, want %d — this is the expires=never bug", got.Bans[0].ExpiresAt, expires)
	}
	if got.Bans[0].IP != "1.2.3.4" || got.Bans[0].Timeout != 3600 ||
		got.Bans[0].Reason != "brute force" || got.Bans[0].Service != "app" {
		t.Errorf("other fields lost: %+v", got.Bans[0])
	}
	// A permanent ban carries no expiry, which is a different fact from
	// "expires at the epoch".
	if _, ok := got.Bans[1].Expires(); ok {
		t.Errorf("a permanent ban reported an expiry: %+v", got.Bans[1])
	}
}

// Two of the responses the client mirrors have NO server type to compare
// against: the handlers write map[string]string literals. Those are pinned by
// driving the real handler and decoding the real body, which is a stronger
// check than shape equality anyway.
func TestSiteRollbackBodyDecodesAsTheClientsType(t *testing.T) {
	live := filepath.Join(t.TempDir(), "site")
	s := newSiteTestServer(t, live)

	for _, v := range []string{"v1", "v2"} {
		rec := httptest.NewRecorder()
		s.handleSiteAPI(rec, siteReq(http.MethodPost, "/api/site/upload", "tok-123", siteTarGz(t, map[string]string{"index.html": v})))
		if rec.Code != http.StatusOK {
			t.Fatalf("upload %s = %d: %s", v, rec.Code, rec.Body)
		}
	}

	rec := httptest.NewRecorder()
	s.handleSiteAPI(rec, siteReq(http.MethodPost, "/api/site/rollback", "tok-123", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback = %d: %s", rec.Code, rec.Body)
	}
	var got hzclient.SiteRollbackResponse
	dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("hz's rollback body does not decode as the client's type: %v (body %s)", err, rec.Body)
	}
	if got.RolledBackTo == "" {
		t.Fatalf("rolledBackTo arrived empty from %s", rec.Body)
	}

	// The releases list, from the same handler, through the same mirror.
	rec = httptest.NewRecorder()
	s.handleSiteAPI(rec, siteReq(http.MethodGet, "/api/site/releases", "tok-123", nil))
	var rels []hzclient.SiteRelease
	dec = json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rels); err != nil {
		t.Fatalf("hz's releases body does not decode as the client's type: %v (body %s)", err, rec.Body)
	}
	if len(rels) != 2 {
		t.Fatalf("got %d releases from %s", len(rels), rec.Body)
	}
	// Newest first, and after a rollback the live one is the OLDER of the two.
	// Exactly one is marked: Current is a real field here, not a zero value the
	// mirror silently drops.
	if rels[0].ID == "" || rels[1].ID == "" {
		t.Errorf("a release arrived with no id: %+v", rels)
	}
	if rels[0].Current || !rels[1].Current {
		t.Errorf("after a rollback the live release is the older one; got %+v", rels)
	}
	if rels[1].ID != got.RolledBackTo {
		t.Errorf("rollback named %q but the live release is %q", got.RolledBackTo, rels[1].ID)
	}
}

// The maintenance page response is the other map literal, and it is the one
// shape here with nothing to pin against structurally: handleDeployMaintPage
// builds it inline, and reaching it needs a live HAProxy. So this pins the two
// keys it writes — kept in sync BY HAND with handlers_deploy.go — and checks
// the md5 key against the only other place the server spells it, which is
// apitypes.DeployStatus.
func TestMaintPageBodyDecodesAsTheClientsType(t *testing.T) {
	// Copied from handleDeployMaintPage. If you change it there, change it
	// here, and this test is the reminder that the client has a third copy.
	set := map[string]string{"status": "ok", "maintenance_page_md5": "d41d8cd98f00b204e9800998ecf8427e"}
	cleared := map[string]string{"status": "ok"}

	for name, body := range map[string]map[string]string{"set": set, "clear": cleared} {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		var got hzclient.MaintPageResponse
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&got); err != nil {
			t.Fatalf("maint-page %s body does not decode as the client's type: %v", name, err)
		}
		if got.Status != "ok" {
			t.Errorf("maint-page %s: status arrived as %q", name, got.Status)
		}
	}

	var got hzclient.MaintPageResponse
	raw, _ := json.Marshal(set)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.MaintenancePageMD5 == "" {
		t.Error("the md5 that confirms hz stored the page arrived empty")
	}

	// Same key, same server, two places. If one is renamed the other should be.
	clientTag := jsonTagOf(t, hzclient.MaintPageResponse{}, "MaintenancePageMD5")
	serverTag := jsonTagOf(t, apitypes.DeployStatus{}, "MaintenancePageMD5")
	if clientTag != serverTag {
		t.Errorf("the client spells the md5 %q and hz spells it %q", clientTag, serverTag)
	}
}

// jsonTagOf returns a field's json name, without options.
func jsonTagOf(t *testing.T, v any, field string) string {
	t.Helper()
	f, ok := reflect.TypeOf(v).FieldByName(field)
	if !ok {
		t.Fatalf("%T has no field %s", v, field)
	}
	tag := f.Tag.Get("json")
	if i := len(tag); i > 0 {
		for j := 0; j < i; j++ {
			if tag[j] == ',' {
				return tag[:j]
			}
		}
	}
	return tag
}
