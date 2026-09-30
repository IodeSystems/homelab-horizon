package server

import (
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

// The VPN listener (vpn_listener.go): hz also answers on the gateway's WG
// address when its primary bind is a specific non-WG address.

func TestVPNListenTarget(t *testing.T) {
	for _, tc := range []struct {
		primary, wgIP string
		enabled       bool
		want, why     string
	}{
		// Added: a specific address that is not the WG address.
		{"127.0.0.1:8080", "10.100.0.1", true, "10.100.0.1:8080", ""},
		{"192.168.1.10:8080", "10.100.0.1", true, "10.100.0.1:8080", ""},
		{"[::1]:9000", "10.100.0.1", true, "10.100.0.1:9000", ""},
		// Not added: the primary already covers wg0.
		{":8080", "10.100.0.1", true, "", "every interface"},
		{"0.0.0.0:8080", "10.100.0.1", true, "", "every interface"},
		{"[::]:8080", "10.100.0.1", true, "", "every interface"},
		{"10.100.0.1:8080", "10.100.0.1", true, "", "is the WireGuard address"},
		// Not added: disabled, or nothing to bind.
		{"127.0.0.1:8080", "10.100.0.1", false, "", "vpn_listen: false"},
		{"127.0.0.1:8080", "", true, "", "not known"},
		{"127.0.0.1", "10.100.0.1", true, "", "has no port"},
	} {
		got, why := vpnListenTarget(tc.primary, tc.wgIP, tc.enabled)
		if got != tc.want || (tc.why != "" && !strings.Contains(why, tc.why)) {
			t.Errorf("vpnListenTarget(%q, %q, %v) = %q %q; want %q, why containing %q",
				tc.primary, tc.wgIP, tc.enabled, got, why, tc.want, tc.why)
		}
	}
}

// The operator's decision: the LAN never sees hz's plain-HTTP port. Swept over
// every primary × WG address shape, including a WG address that is itself
// "every interface", and then the listener is handed one directly.
func TestTheVPNListenerNeverBindsEveryInterface(t *testing.T) {
	primaries := []string{"127.0.0.1:8080", "192.168.1.10:8080", ":8080", "0.0.0.0:8080", "[::]:8080", "[::1]:8080", "10.100.0.1:8080"}
	wgIPs := []string{"10.100.0.1", "0.0.0.0", "::", "", "fd00::1", "127.0.0.2"}
	for _, p := range primaries {
		for _, w := range wgIPs {
			got, _ := vpnListenTarget(p, w, true)
			if got == "" {
				continue
			}
			host, _, err := net.SplitHostPort(got)
			ip := net.ParseIP(host)
			if err != nil || ip == nil || ip.IsUnspecified() {
				t.Errorf("vpnListenTarget(%q, %q) = %q — every interface, or not an address", p, w, got)
			}
		}
	}

	var asked []string
	var mu sync.Mutex
	v := &vpnListener{
		serve: func(net.Listener) error { return nil },
		listen: func(_, addr string) (net.Listener, error) {
			mu.Lock()
			asked = append(asked, addr)
			mu.Unlock()
			return nil, errors.New("test")
		},
		retryMin: time.Millisecond, retryMax: time.Millisecond,
	}
	defer v.Close()
	for _, addr := range []string{"0.0.0.0:8080", "[::]:8080", ":8080"} {
		v.Sync(addr)
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 0 || v.Bound() != "" {
		t.Fatalf("the listener tried to bind %v", asked)
	}
}

// vpnListenerServer is a test Server whose wg0.conf gives the gateway wgAddr,
// with the primary listener really bound on 127.0.0.1 and served by the same
// http.Server the VPN listener joins. Returns the server, the primary's port
// and the http.Server.
func vpnListenerServer(t *testing.T, wgAddr string) (*Server, string, *http.Server) {
	s, port, srv, _ := vpnListenerServerAt(t, wgAddr)
	return s, port, srv
}

func vpnListenerServerAt(t *testing.T, wgAddr string) (*Server, string, *http.Server, string) {
	t.Helper()
	primary, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(primary.Addr().String())
	s := newTestServer(t, &config.Config{ListenAddr: "127.0.0.1:" + port, VPNRange: "10.100.0.0/24"})
	path := filepath.Join(t.TempDir(), "wg0.conf")
	writeWGAddress(t, path, wgAddr)
	s.wg = wireguard.NewConfig(path, "wg0")
	if err := s.wg.Load(); err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: s.handler()}
	go func() { _ = srv.Serve(primary) }()
	t.Cleanup(func() {
		if v := s.vpnLn.Load(); v != nil {
			v.Close()
		}
		_ = srv.Close()
	})
	return s, port, srv, path
}

func writeWGAddress(t *testing.T, path, addr string) {
	t.Helper()
	conf := "[Interface]\nPrivateKey = cGFzc3dvcmQ=\nAddress = " + addr + "/24\nListenPort = 51820\n"
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
}

// waitBound polls until the VPN listener reports want as bound.
func waitBound(t *testing.T, s *Server, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.vpnListenerBound() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("VPN listener bound = %q, want %q", s.vpnListenerBound(), want)
}

func vpnGet(t *testing.T, url string, cookie *http.Cookie) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// Same handler, same auth: a request to either listener gets the same answer,
// and an unauthenticated admin request is refused on both.
func TestBothListenersServeTheSameRoutes(t *testing.T) {
	s, port, srv := vpnListenerServer(t, "127.0.0.2")
	s.startVPNListener(srv)
	vpnAddr := "127.0.0.2:" + port
	waitBound(t, s, vpnAddr)

	admin := &http.Cookie{Name: "session", Value: s.signCookie("admin")}
	for _, host := range []string{"127.0.0.1:" + port, vpnAddr} {
		if code, body := vpnGet(t, "http://"+host+"/health", nil); code != http.StatusOK && code != http.StatusServiceUnavailable {
			t.Errorf("%s /health = %d %s", host, code, body)
		}
		if code, _ := vpnGet(t, "http://"+host+"/api/v1/vpn/invites", nil); code != http.StatusUnauthorized {
			t.Errorf("%s: an unauthenticated admin request got %d, want 401", host, code)
		}
		if code, body := vpnGet(t, "http://"+host+"/api/v1/vpn/invites", admin); code != http.StatusOK {
			t.Errorf("%s: an admin request got %d %s", host, code, body)
		}
	}
	c1, b1 := vpnGet(t, "http://127.0.0.1:"+port+"/health", nil)
	c2, b2 := vpnGet(t, "http://"+vpnAddr+"/health", nil)
	if c1 != c2 || b1 != b2 {
		t.Errorf("the listeners answer differently: %d %q vs %d %q", c1, b1, c2, b2)
	}
}

// Not added when the primary already covers wg0, or when turned off.
func TestTheVPNListenerIsNotAddedWhenNotWanted(t *testing.T) {
	s, _, srv := vpnListenerServer(t, "127.0.0.2")
	var calls atomic.Int32
	s.vpnListen = func(network, addr string) (net.Listener, error) {
		calls.Add(1)
		return net.Listen(network, addr)
	}
	off := false
	cfg := *s.cfg()
	cfg.VPNListen = &off
	s.config.Store(&cfg)
	s.startVPNListener(srv)

	cfg2 := *s.cfg()
	cfg2.VPNListen = nil
	cfg2.SetNoVPNListen(true)
	s.config.Store(&cfg2)
	s.syncVPNListener()

	cfg3 := *s.cfg()
	cfg3.SetNoVPNListen(false)
	cfg3.SetListenOverride(":" + strings.Split(cfg3.ListenAddr, ":")[1])
	s.config.Store(&cfg3)
	s.syncVPNListener()

	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 0 || s.vpnListenerBound() != "" {
		t.Fatalf("bound %q after %d binds; want none", s.vpnListenerBound(), n)
	}
}

// IP_FREEBIND binds an address the box does not have — the WG address before
// wg0 is up. 192.0.2.1 is TEST-NET-1 (RFC 5737), on no interface here; the
// plain bind failing on it first is the proof of that.
func TestFreebindBindsAnAddressNotYetOnTheBox(t *testing.T) {
	if !freebindSupported {
		t.Skip("no IP_FREEBIND on this platform; the VPN listener retries in the background instead (TestAFailedBindIsRetriedInTheBackground)")
	}
	if ln, err := net.Listen("tcp4", "192.0.2.1:0"); err == nil {
		ln.Close()
		t.Skip("192.0.2.1 is assigned on this box, so it cannot show FREEBIND binding an absent address")
	}
	ln, err := listenFreebind("tcp4", "192.0.2.1:0")
	if err != nil {
		t.Fatalf("FREEBIND bind of an absent address: %v", err)
	}
	ln.Close()
}

// Nothing waits on the VPN bind: a bind that never returns does not hold up
// startVPNListener, and so not hz's start.
func TestTheVPNBindNeverBlocksStart(t *testing.T) {
	s, _, srv := vpnListenerServer(t, "127.0.0.2")
	release := make(chan struct{})
	defer close(release)
	s.vpnListen = func(string, string) (net.Listener, error) {
		<-release
		return nil, errors.New("released")
	}
	done := make(chan struct{})
	go func() {
		s.startVPNListener(srv)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("startVPNListener blocked on the VPN bind")
	}
	if s.vpnListenerBound() != "" {
		t.Fatal("reported bound while the bind has not returned")
	}
}

// The fallback where FREEBIND is unavailable or fails: retried with backoff
// until the address exists, never given up.
func TestAFailedBindIsRetriedInTheBackground(t *testing.T) {
	var attempts atomic.Int32
	v := &vpnListener{
		serve: func(ln net.Listener) error {
			for {
				c, err := ln.Accept()
				if err != nil {
					return err
				}
				c.Close()
			}
		},
		listen: func(network, _ string) (net.Listener, error) {
			if attempts.Add(1) < 3 {
				return nil, errors.New("cannot assign requested address")
			}
			return net.Listen(network, "127.0.0.1:0")
		},
		retryMin: time.Millisecond, retryMax: 4 * time.Millisecond,
	}
	defer v.Close()
	v.Sync("10.100.0.1:8080")
	deadline := time.Now().Add(3 * time.Second)
	for v.Bound() == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if v.Bound() != "10.100.0.1:8080" || attempts.Load() != 3 {
		t.Fatalf("bound %q after %d attempts; want bound after 3", v.Bound(), attempts.Load())
	}
}

// The VPN range is settable: a new WG address rebinds, without a restart — the
// old address stops answering and the new one starts. Then a config change
// that turns it off closes it.
func TestANewWGAddressRebinds(t *testing.T) {
	s, port, srv, wgPath := vpnListenerServerAt(t, "127.0.0.2")
	s.startVPNListener(srv)
	oldAddr, newAddr := "127.0.0.2:"+port, "127.0.0.3:"+port
	waitBound(t, s, oldAddr)

	writeWGAddress(t, wgPath, "127.0.0.3")
	if err := s.wg.Load(); err != nil {
		t.Fatal(err)
	}
	s.syncVPNListener() // what the health tick and every config swap call
	waitBound(t, s, newAddr)
	if code, _ := vpnGet(t, "http://"+newAddr+"/api/v1/vpn/invites", nil); code != http.StatusUnauthorized {
		t.Fatalf("the new address answered %d", code)
	}
	if c, err := net.DialTimeout("tcp", oldAddr, time.Second); err == nil {
		c.Close()
		t.Fatal("the old WG address still answers after the rebind")
	}

	off := false
	if err := s.updateConfig(func(c *config.Config) { c.VPNListen = &off }); err != nil {
		t.Fatal(err)
	}
	waitBound(t, s, "")
	if c, err := net.DialTimeout("tcp", newAddr, time.Second); err == nil {
		c.Close()
		t.Fatal("vpn_listen: false left the listener answering")
	}
}

// N1b's gate: a loopback primary is accepted once the VPN listener is bound at
// the admitted address, and refused again when it is turned off.
func TestTheUpstreamGateAcceptsTheVPNListener(t *testing.T) {
	s, path := upstreamServer(t, "127.0.0.1:8080")
	// 10.100.0.1:8080 is the admitted address; the test stands a loopback
	// socket in for it (the bind itself is FREEBIND's test, above).
	s.vpnListen = func(string, string) (net.Listener, error) { return net.Listen("tcp4", "127.0.0.1:0") }
	srv := &http.Server{Handler: s.handler()}
	v := s.startVPNListener(srv)
	defer v.Close()
	waitBound(t, s, "10.100.0.1:8080")

	u, err := s.upstreamParentURL()
	if err != nil || u != "http://10.100.0.1:8080" {
		t.Fatalf("with the VPN listener bound: %q %v", u, err)
	}

	off := false
	if err := s.updateConfig(func(c *config.Config) { c.VPNListen = &off }); err != nil {
		t.Fatal(err)
	}
	waitBound(t, s, "")
	if _, err := s.upstreamParentURL(); err == nil || !strings.Contains(err.Error(), "VPN listener is not bound") {
		t.Fatalf("with vpn_listen off and a loopback primary: %v", err)
	}
	// And over the API: refused, and before wg0.conf is touched.
	msg := postDeclareErr(t, s, s.handleAPIAddPeer, "/api/v1/vpn/peers/add",
		apitypes.PeerAddReq{Name: "next-hz-vpn", Profile: config.ProfileUpstream})
	if !strings.Contains(msg, "not the gateway's WireGuard address") {
		t.Fatalf("refusal: %s", msg)
	}
	wg0Unchanged(t, path)
}

// Where this instance binds is its own: a pulled config does not turn its VPN
// listener on or off.
func TestPeerSyncPinsVPNListen(t *testing.T) {
	off, on := false, true
	local := &config.Config{VPNListen: &off}
	remote := &config.Config{VPNListen: &on}
	if got := mergeRemoteIntoLocal(remote, local); got.VPNListen == nil || *got.VPNListen {
		t.Fatalf("merged vpn_listen = %v, want the local false", got.VPNListen)
	}
}
