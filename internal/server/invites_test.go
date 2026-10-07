package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

// Keys from RFC 7748 §6.1 test vectors, base64 — public halves only. The
// browser's keypair is stood in for by a public key the test chose, which is
// exactly what the server is given.
const (
	inviteDevKeyA = "hSDwCYkwp1R0i33ctD73Wg2/Og0mOBr066SpjqqbTmo="
	inviteDevKeyB = "3p7bfXt9wbTTW2HC7OQ1Nz+DQ8hbeGdNrfx+FG+IK08="
)

const inviteWG0 = `[Interface]
PrivateKey = cGFzc3dvcmQ=
Address = 10.101.0.1/24
ListenPort = 51820
`

const inviteKiosk = "kiosk.hz.loadtest.example.com"

// inviteServer is a nested hz with a kiosk, a users store with one account
// (carl), an empty wg0 and no machine side effects.
func inviteServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	wgPath := filepath.Join(dir, "wg0.conf")
	if err := os.WriteFile(wgPath, []byte(inviteWG0), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		VPNRange:        "10.101.0.0/24",
		DNS:             "10.101.0.1",
		ServerPublicKey: "c2VydmVyLXB1YmtleS1zZXJ2ZXItcHVia2V5LXNlcnY=",
		ServerEndpoint:  inviteKiosk + ":51820",
		KioskURL:        "https://" + inviteKiosk,
		InvitesFile:     filepath.Join(dir, "invites.txt"),
		WGConfigPath:    wgPath,
	}
	s := newTestServer(t, cfg)
	t.Cleanup(s.monitor.Stop)
	s.wg = wireguard.NewConfig(wgPath, "wg0")
	if err := s.wg.Load(); err != nil {
		t.Fatal(err)
	}
	s.peerApply = func() {}
	store, err := db.Open(filepath.Join(dir, "hz.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateUser(context.Background(), "carl", "", db.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	s.users = store
	return s, wgPath
}

func mintInvite(t *testing.T, s *Server, req apitypes.CreateInviteReq) apitypes.CreateInviteResponse {
	t.Helper()
	raw, _ := json.Marshal(req)
	r := asAdmin(s, http.MethodPost, "/api/v1/vpn/invites/create", string(raw))
	r.Host = "admin.hz.loadtest.example.com"
	w := httptest.NewRecorder()
	s.handleAPICreateInvite(w, r)
	var out apitypes.CreateInviteResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("create invite: %d %s", w.Code, w.Body.String())
	}
	return out
}

func tokenOf(t *testing.T, link string) string {
	t.Helper()
	tok, ok := strings.CutPrefix(link, "https://"+inviteKiosk+"/invite/")
	if !ok || tok == "" {
		t.Fatalf("invite link %q is not https://%s/invite/<token>", link, inviteKiosk)
	}
	return tok
}

// kioskPost sends an unauthenticated invitee request through the whole mux,
// on the kiosk vhost unless host says otherwise.
func kioskPost(s *Server, host, path string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	r.Host = host
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	return w
}

func redeem(s *Server, token, pub, name string) *httptest.ResponseRecorder {
	return kioskPost(s, inviteKiosk, "/api/v1/vpn/invite/redeem",
		apitypes.InviteRedeemReq{Token: token, PublicKey: pub, Name: name})
}

// TestInviteRedeemUsesExactlyTheSubmittedKey: the peer hz creates has the
// public key the browser sent — byte for byte — the invite's profile and
// owner, and the config handed back has the placeholder where the private key
// goes. Nowhere in the response, wg0.conf, config.json or invites.txt is there
// a private key or the token.
func TestInviteRedeemUsesExactlyTheSubmittedKey(t *testing.T) {
	s, wgPath := inviteServer(t)
	inv := mintInvite(t, s, apitypes.CreateInviteReq{Name: "carl-phone", Profile: config.ProfileLanAccess, User: "carl"})
	tok := tokenOf(t, inv.URL)

	w := redeem(s, tok, inviteDevKeyA, "ignored-because-the-invite-names-it")
	var out apitypes.InviteRedeemResp
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("redeem: %d %s", w.Code, w.Body.String())
	}
	if out.Name != "carl-phone" || out.PublicKey != inviteDevKeyA || out.Address != "10.101.0.2" {
		t.Fatalf("redeem answer: %+v", out)
	}
	p := s.wg.GetPeerByPublicKey(inviteDevKeyA)
	if p == nil || p.Name != "carl-phone" || p.PublicKey != inviteDevKeyA {
		t.Fatalf("stored peer = %+v, want carl-phone with exactly the submitted key", p)
	}
	if got := s.cfg().GetPeerProfile("carl-phone"); got != config.ProfileLanAccess {
		t.Errorf("profile = %q, want lan-access", got)
	}
	owner, err := s.users.PeerOwner(context.Background(), "carl-phone")
	if acct, _ := s.users.UserByUsername(context.Background(), "carl"); err != nil || acct == nil || owner != acct.ID {
		t.Errorf("owner = %q (%v), want carl", owner, err)
	}

	// The config: one PrivateKey line, and it is the placeholder.
	var priv []string
	for _, line := range strings.Split(out.Config, "\n") {
		if strings.HasPrefix(line, "PrivateKey") {
			priv = append(priv, line)
		}
	}
	if len(priv) != 1 || priv[0] != "PrivateKey = "+apitypes.InvitePrivateKeyPlaceholder {
		t.Fatalf("config PrivateKey lines = %q, want exactly the placeholder", priv)
	}
	// wg0.conf holds only the server's own key; nothing else is a private key.
	conf, _ := os.ReadFile(wgPath)
	if n := strings.Count(string(conf), "PrivateKey"); n != 1 {
		t.Fatalf("wg0.conf has %d PrivateKey lines, want only the server's:\n%s", n, conf)
	}
	for _, f := range []string{s.cfg().InvitesFile, s.configPath} {
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), tok) {
			t.Errorf("%s holds the invite token", f)
		}
		if strings.Contains(strings.ToLower(string(b)), "privatekey") || strings.Contains(strings.ToLower(string(b)), "private_key") {
			t.Errorf("%s mentions a private key:\n%s", f, b)
		}
	}
}

// TestInviteIsSingleUse: the second redeem of a token is refused and adds
// nothing.
func TestInviteIsSingleUse(t *testing.T) {
	s, _ := inviteServer(t)
	tok := tokenOf(t, mintInvite(t, s, apitypes.CreateInviteReq{}).URL)
	if w := redeem(s, tok, inviteDevKeyA, "dev-a"); w.Code != http.StatusOK {
		t.Fatalf("first redeem: %d %s", w.Code, w.Body.String())
	}
	w := redeem(s, tok, inviteDevKeyB, "dev-b")
	if w.Code != http.StatusGone {
		t.Fatalf("second redeem of one token = %d %s, want 410", w.Code, w.Body.String())
	}
	if s.wg.GetPeerByPublicKey(inviteDevKeyB) != nil {
		t.Fatal("the second redeem created a peer")
	}
	if got := s.cfg().GetPeerProfile("dev-a"); got != config.ProfileVPNOnly {
		t.Errorf("default profile = %q, want vpn-only", got)
	}
	if c := kioskPost(s, inviteKiosk, "/api/v1/vpn/invite/check", apitypes.InviteCheckReq{Token: tok}); !strings.Contains(c.Body.String(), `"valid":false`) {
		t.Errorf("check after use: %s", c.Body.String())
	}
}

// TestAnInviteeMistakeDoesNotBurnTheLink: a bad key or a taken name is
// refused before the token is consumed.
func TestAnInviteeMistakeDoesNotBurnTheLink(t *testing.T) {
	s, _ := inviteServer(t)
	tok := tokenOf(t, mintInvite(t, s, apitypes.CreateInviteReq{}).URL)
	if w := redeem(s, tok, "not-a-key", "dev"); w.Code != http.StatusBadRequest {
		t.Fatalf("bad key: %d", w.Code)
	}
	if w := redeem(s, tok, inviteDevKeyA, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("no name: %d", w.Code)
	}
	if w := redeem(s, tok, inviteDevKeyA, "dev"); w.Code != http.StatusOK {
		t.Fatalf("the link was burned by a mistake: %d %s", w.Code, w.Body.String())
	}
}

func TestAnExpiredInviteIsRefused(t *testing.T) {
	s, _ := inviteServer(t)
	tok := "expired-token-0123456789"
	rec := inviteRecord{SHA256: inviteHash(tok), Expires: time.Now().Add(-time.Minute).Unix()}
	line, _ := json.Marshal(rec)
	if err := os.WriteFile(s.cfg().InvitesFile, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	// Instrument check: the same record, unexpired, is accepted.
	if _, ok := s.findInvite(tok); ok {
		t.Fatal("an expired invite reads as live")
	}
	if w := redeem(s, tok, inviteDevKeyA, "dev"); w.Code != http.StatusGone {
		t.Fatalf("expired redeem = %d %s, want 410", w.Code, w.Body.String())
	}
	if s.wg.GetPeerByPublicKey(inviteDevKeyA) != nil {
		t.Fatal("an expired invite created a peer")
	}
	rec.Expires = time.Now().Add(time.Hour).Unix()
	line, _ = json.Marshal(rec)
	_ = os.WriteFile(s.cfg().InvitesFile, append(line, '\n'), 0o600)
	if _, ok := s.findInvite(tok); !ok {
		t.Fatal("instrument: the same record unexpired is not found, so the refusal above proves nothing")
	}
}

// TestALegacyInviteLineStillWorksAndIsHashedOnWrite: invites.txt from before
// this release ("token:expiry") redeems, and is rewritten without the token.
func TestALegacyInviteLineStillWorksAndIsHashedOnWrite(t *testing.T) {
	s, _ := inviteServer(t)
	other := "legacy-other-token-abcdef"
	tok := "legacy-token-0123456789"
	exp := time.Now().Add(time.Hour).Unix()
	body := tok + ":" + strconv.FormatInt(exp, 10) + "\n" + other + ":" + strconv.FormatInt(exp, 10) + "\n"
	_ = os.WriteFile(s.cfg().InvitesFile, []byte(body), 0o600)
	if w := redeem(s, tok, inviteDevKeyA, "legacy-dev"); w.Code != http.StatusOK {
		t.Fatalf("legacy redeem: %d %s", w.Code, w.Body.String())
	}
	b, _ := os.ReadFile(s.cfg().InvitesFile)
	if strings.Contains(string(b), other) || !strings.Contains(string(b), inviteHash(other)) {
		t.Fatalf("legacy line not rewritten as a hash:\n%s", b)
	}
}

func TestInviteCreateRules(t *testing.T) {
	s, _ := inviteServer(t)
	inv := mintInvite(t, s, apitypes.CreateInviteReq{})
	exp, err := time.Parse(time.RFC3339, inv.ExpiresAt)
	if err != nil || exp.Sub(time.Now()) < 23*time.Hour || exp.Sub(time.Now()) > 25*time.Hour {
		t.Errorf("default expiry %q, want ~24h", inv.ExpiresAt)
	}
	if inv.Profile != config.ProfileVPNOnly {
		t.Errorf("default profile %q", inv.Profile)
	}
	tokenOf(t, inv.URL) // the kiosk host, not the admin host the request came in on

	for _, tc := range []struct {
		req  apitypes.CreateInviteReq
		want string
	}{
		{apitypes.CreateInviteReq{ExpiresSeconds: 8 * 24 * 3600}, "7 days"},
		{apitypes.CreateInviteReq{Profile: config.ProfileUpstream}, "nested hz"},
		{apitypes.CreateInviteReq{Profile: "sideways"}, "not one of"},
		{apitypes.CreateInviteReq{User: "nobody"}, "not an enabled account"},
	} {
		raw, _ := json.Marshal(tc.req)
		w := httptest.NewRecorder()
		s.handleAPICreateInvite(w, asAdmin(s, http.MethodPost, "/api/v1/vpn/invites/create", string(raw)))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%+v: %d %s, want 400 naming %q", tc.req, w.Code, w.Body.String(), tc.want)
		}
	}
	// 7 days exactly is allowed.
	mintInvite(t, s, apitypes.CreateInviteReq{ExpiresSeconds: 7 * 24 * 3600})

	// The list shows metadata, never the token or the link.
	w := httptest.NewRecorder()
	s.handleAPIListInvites(w, asAdmin(s, http.MethodGet, "/api/v1/vpn/invites", ""))
	if strings.Contains(w.Body.String(), tokenOf(t, inv.URL)) || strings.Contains(w.Body.String(), "/invite/") {
		t.Fatalf("the list leaks the link: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), inv.ID) {
		t.Fatalf("the list lacks the invite: %s", w.Body.String())
	}
}

// TestTheInvitePathIsKioskVhostOnly: the invitee is not on the VPN, so the
// link and its two calls are served on the kiosk (public) vhost — and 404 on
// the admin hostname, like /admin/hz-child/install.
func TestTheInvitePathIsKioskVhostOnly(t *testing.T) {
	s, _ := inviteServer(t)
	tok := tokenOf(t, mintInvite(t, s, apitypes.CreateInviteReq{}).URL)

	r := httptest.NewRequest(http.MethodGet, "/invite/"+tok, nil)
	r.Host = inviteKiosk
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/app/invite/"+tok {
		t.Fatalf("kiosk /invite/: %d -> %q", w.Code, w.Header().Get("Location"))
	}
	if c := kioskPost(s, inviteKiosk, "/api/v1/vpn/invite/check", apitypes.InviteCheckReq{Token: tok}); !strings.Contains(c.Body.String(), `"valid":true`) {
		t.Fatalf("kiosk check: %d %s", c.Code, c.Body.String())
	}

	admin := "admin.hz.loadtest.example.com"
	r = httptest.NewRequest(http.MethodGet, "/invite/"+tok, nil)
	r.Host = admin
	w = httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("admin-host /invite/ = %d, want 404", w.Code)
	}
	for _, p := range []string{"/api/v1/vpn/invite/check", "/api/v1/vpn/invite/redeem"} {
		if c := kioskPost(s, admin, p, apitypes.InviteRedeemReq{Token: tok, PublicKey: inviteDevKeyA, Name: "x"}); c.Code != http.StatusNotFound {
			t.Errorf("admin-host %s = %d, want 404", p, c.Code)
		}
	}
	if s.wg.GetPeerByPublicKey(inviteDevKeyA) != nil {
		t.Fatal("a redeem on the admin host created a peer")
	}
}

// TestNoInviteTypeCarriesAPrivateKey pins the wire and storage types by exact
// field list: adding a field to any of them — a private key above all — fails
// here and has to be argued for.
func TestNoInviteTypeCarriesAPrivateKey(t *testing.T) {
	want := map[any][]string{
		apitypes.CreateInviteReq{}:      {"Name", "Profile", "User", "ExpiresSeconds"},
		apitypes.CreateInviteResponse{}: {"OK", "URL", "ID", "Name", "Profile", "User", "ExpiresAt"},
		apitypes.InviteResp{}:           {"ID", "Name", "Profile", "User", "ExpiresAt"},
		apitypes.InviteCheckReq{}:       {"Token"},
		apitypes.InviteCheckResp{}:      {"Valid", "Name", "Profile", "ExpiresAt"},
		apitypes.InviteRedeemReq{}:      {"Token", "PublicKey", "Name"},
		apitypes.InviteRedeemResp{}:     {"OK", "Name", "Address", "PublicKey", "Config"},
		inviteRecord{}:                  {"SHA256", "Expires", "Name", "Profile", "User"},
	}
	for v, fields := range want {
		typ := reflect.TypeOf(v)
		var got []string
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			got = append(got, f.Name)
			l := strings.ToLower(f.Name + " " + f.Tag.Get("json"))
			if strings.Contains(l, "priv") || strings.Contains(l, "secret") {
				t.Errorf("%s.%s names a private key", typ.Name(), f.Name)
			}
		}
		if !reflect.DeepEqual(got, fields) {
			t.Errorf("%s fields = %v, want %v", typ.Name(), got, fields)
		}
	}
}
