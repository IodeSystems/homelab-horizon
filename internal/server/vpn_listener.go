package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// THE VPN LISTENER: hz ALSO answers on the gateway's WireGuard address.
//
// On a gateway where hz binds loopback (--listen 127.0.0.1:8080, PCI DSS 2.2.7:
// the admin UI speaks plain HTTP, so only HAProxy's TLS frontend may reach it),
// nothing answers at <wg IP>:<port> — the one address a nested hz's upstream
// client is admitted to (config/vpn_upstream.go, iptables upstreamRules). This
// adds a second net.Listener at exactly that address, served by the SAME
// http.Server as the primary: same handler, same auth, same middleware, and
// Shutdown drains both.
//
// NEVER 0.0.0.0. The operator's decision (2026-09-29): the LAN must never see
// hz's plain-HTTP port. vpnListenTarget refuses an unspecified address, and
// vpnListener.attempt refuses one again before it binds.
//
// BOOT ORDER (CLAUDE.md §5). wg0 may not be up when hz starts. On Linux the
// socket is bound with IP_FREEBIND, which binds an address the box does not
// have yet. Where that fails (another platform, or a real error), the bind is
// retried in the background with backoff and logged at Error each time. Every
// attempt runs off the caller's goroutine: nothing here can hold up the
// primary listener or hz's start.
//
// THE ADDRESS FOLLOWS wg0.conf, the same source (Server.gatewayWGIP) the
// upstream rule's -d and ParentAPIURL use, so the listener is where the rule
// admits and not a second answer that could disagree with it. When it changes
// the old socket is closed and a new one opened (Server.syncVPNListener, on the
// health tick and after every config swap) — no restart.

// vpnListenTarget decides the VPN listener's address from the primary bind and
// the gateway's WG IP. It returns "" and why when none should be added.
func vpnListenTarget(primary, wgIP string, enabled bool) (addr, why string) {
	if !enabled {
		return "", "off (vpn_listen: false or --no-vpn-listen)"
	}
	ip := net.ParseIP(strings.TrimSpace(wgIP))
	if ip == nil {
		return "", "off (the gateway's WireGuard address is not known — wg0.conf has no Address)"
	}
	if ip.IsUnspecified() {
		return "", "off (the gateway's WireGuard address is " + ip.String() + ", which is every interface — refused)"
	}
	host, port, err := net.SplitHostPort(strings.TrimSpace(primary))
	if err != nil || port == "" {
		return "", "off (the primary listen address " + primary + " has no port)"
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "", "0.0.0.0", "::", "*":
		return "", "not needed (the primary listener already binds every interface)"
	}
	if h := net.ParseIP(host); h != nil && h.Equal(ip) {
		return "", "not needed (the primary listener is the WireGuard address)"
	}
	return net.JoinHostPort(ip.String(), port), ""
}

// vpnListener owns the second listener's lifecycle: bind, serve, rebind on a
// new address, retry a failed bind in the background.
type vpnListener struct {
	serve    func(net.Listener) error                         // http.Server.Serve
	listen   func(network, addr string) (net.Listener, error) // listenFreebind
	retryMin time.Duration
	retryMax time.Duration
	primary  string // for the log line only

	mu     sync.Mutex
	want   string        // the address asked for; "" = none
	ln     net.Listener  // bound at want, or nil
	stop   chan struct{} // closed when want changes or the listener closes
	closed bool
}

func newVPNListener(srv *http.Server, primary string, listen func(network, addr string) (net.Listener, error)) *vpnListener {
	if listen == nil {
		listen = listenFreebind
	}
	return &vpnListener{
		serve:    srv.Serve,
		listen:   listen,
		retryMin: time.Second,
		retryMax: time.Minute,
		primary:  primary,
	}
}

// Sync makes want the listener's address: nothing when it already is; else the
// old socket (and any pending retry) is closed and a bind to the new address
// starts in the background. It never blocks on a bind. Reports a change.
func (v *vpnListener) Sync(want string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed || want == v.want {
		return false
	}
	v.teardownLocked()
	v.want = want
	if want == "" {
		return true
	}
	stop := make(chan struct{})
	v.stop = stop
	go v.bindLoop(want, stop)
	return true
}

// Bound is the address the listener is bound to right now, or "".
func (v *vpnListener) Bound() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.ln == nil {
		return ""
	}
	return v.want
}

// Close stops the listener and any retry, for good.
func (v *vpnListener) Close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.teardownLocked()
	v.want = ""
	v.closed = true
}

func (v *vpnListener) teardownLocked() {
	if v.stop != nil {
		close(v.stop)
		v.stop = nil
	}
	if v.ln != nil {
		_ = v.ln.Close()
		v.ln = nil
	}
}

// bindLoop binds addr, retrying with backoff until it succeeds or stop closes.
func (v *vpnListener) bindLoop(addr string, stop chan struct{}) {
	delay := v.retryMin
	for {
		ln, err := v.attempt(addr)
		if errors.Is(err, errEveryInterface) {
			// Permanent, so not retried: no later attempt could be allowed.
			slog.Error("VPN listener: REFUSED", "addr", addr, "err", err)
			return
		}
		if err == nil {
			if v.adopt(ln, addr, stop) {
				slog.Info("listening on " + v.primary + " and " + addr + " (VPN)")
				v.serveOn(ln, addr, stop)
			} else {
				_ = ln.Close()
			}
			return
		}
		slog.Error("VPN listener: bind FAILED — a nested hz cannot reach this hz over its upstream client until it binds; retrying in the background",
			"addr", addr, "err", err, "retry_in", delay.String())
		select {
		case <-stop:
			return
		case <-time.After(delay):
		}
		if delay *= 2; delay > v.retryMax {
			delay = v.retryMax
		}
	}
}

var errEveryInterface = errors.New("the VPN listener binds only the gateway's WireGuard address, never every interface")

// attempt is one bind, refusing every-interface a second time: the one
// property of this listener that must hold whatever vpnListenTarget returned.
func (v *vpnListener) attempt(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || ip.IsUnspecified() {
		return nil, fmt.Errorf("refusing to bind %s: %w", addr, errEveryInterface)
	}
	network := "tcp6"
	if ip.To4() != nil {
		network = "tcp4"
	}
	return v.listen(network, addr)
}

// adopt records ln as the live listener if addr is still wanted.
func (v *vpnListener) adopt(ln net.Listener, addr string, stop chan struct{}) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	select {
	case <-stop:
		return false
	default:
	}
	if v.closed || v.want != addr {
		return false
	}
	v.ln = ln
	return true
}

// serveOn serves ln until it is closed. A close we did (rebind, Close,
// Shutdown) is the normal end; anything else loses the listener, which is said
// loudly and retried.
func (v *vpnListener) serveOn(ln net.Listener, addr string, stop chan struct{}) {
	err := v.serve(ln)
	select {
	case <-stop:
		return
	default:
	}
	if errors.Is(err, http.ErrServerClosed) {
		return
	}
	v.mu.Lock()
	lost := v.ln == ln
	if lost {
		v.ln = nil
	}
	v.mu.Unlock()
	if lost {
		slog.Error("VPN listener stopped serving; rebinding", "addr", addr, "err", err)
		v.bindLoop(addr, stop)
	}
}

// syncVPNListener points the VPN listener at where it belongs now. Cheap when
// nothing changed; called at start, on the health tick and after a config swap,
// so a new WG address rebinds without a restart.
func (s *Server) syncVPNListener() {
	v := s.vpnLn.Load()
	if v == nil {
		return
	}
	cfg := s.cfg()
	want, why := vpnListenTarget(cfg.EffectiveListenAddr(), s.gatewayWGIP(), cfg.VPNListenEnabled())
	if v.Sync(want) && want == "" {
		slog.Info("listening on " + cfg.EffectiveListenAddr() + "; VPN listener " + why)
	}
}

// startVPNListener attaches the VPN listener to srv. Never blocks on a bind.
func (s *Server) startVPNListener(srv *http.Server) *vpnListener {
	v := newVPNListener(srv, s.cfg().EffectiveListenAddr(), s.vpnListen)
	s.vpnLn.Store(v)
	cfg := s.cfg()
	want, why := vpnListenTarget(cfg.EffectiveListenAddr(), s.gatewayWGIP(), cfg.VPNListenEnabled())
	if want == "" {
		slog.Info("listening on " + cfg.EffectiveListenAddr() + "; VPN listener " + why)
	}
	v.Sync(want)
	return v
}

// vpnListenerBound is the VPN listener's bound address, or "".
func (s *Server) vpnListenerBound() string {
	if v := s.vpnLn.Load(); v != nil {
		return v.Bound()
	}
	return ""
}

// upstreamParentURL is the URL an upstream client is handed, or why there is
// none: N1b's add-time gate. The VPN listener counts only while it is BOUND at
// the address the upstream rule admits.
func (s *Server) upstreamParentURL() (string, error) {
	return s.cfg().ParentAPIURL(s.gatewayWGIP(), s.vpnListenerBound())
}
