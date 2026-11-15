package monitor

// Edge diagnosis: turning a set of probe results into a cause and an
// instruction.
//
// # Why this is not a field on Result
//
// A single probe result cannot name a cause. "connection refused" means one
// thing when the name resolves to the address hz expects and a completely
// different thing when it resolves somewhere else, and the probe that got
// refused does not know which. The cause is a property of the SET of results
// for one target — the ladder DNS -> TCP -> TLS -> HTTP — and the first rung
// that fails is what names it.
//
// # Why this lives on hz and not in the agent
//
// internal/probe's package doc commits to an agent that is deliberately
// ignorant: "the only facts that cross the wire are ones the public internet
// already holds ... Backends, LAN CIDRs and VPN ranges stay on hz." Every
// rung above DNS needs something the agent must not hold — what hz's public
// IP currently is, how fresh hz's idea of it is, whether a name is meant to
// be pinned elsewhere, which vantage cadence makes a reading stale. Teaching
// the agent those would break the property outright.
//
// So the classifier is in package monitor, not package probe. That is the
// structural version of the rule rather than the commented version: cmd/hz-probe
// imports internal/probe and does not import internal/monitor, so an agent
// CANNOT call this, and wiring it in would mean pulling hz's whole monitor
// into the agent binary — a change nobody makes by accident.
//
// # Shape
//
// Diagnosis follows projection.Gap: a stable KEY a screen can branch on
// (Cause), plus prose that names what would close it (Fix). Gap's reasoning
// applies unchanged here — "hz does not know" without "and here is what would
// tell it" is a dead end, and so is "this is broken" without "on which box".
//
// The router is the extreme case and the reason this exists. hz declares and
// observes; it never reaches into a machine, and the edge router is a machine
// hz has no path to at all. For the router, an instruction IS the entire
// deliverable, which is why Diagnosis carries Device and HZCanFix: a screen
// must never offer a fix button for a change that has to happen on somebody
// else's box.

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/probe"
)

// StatusUnknown is a target hz has no current outside-in reading for.
//
// It is NOT StatusOK and it is NOT StatusPending. This repo's founding bug is
// two different states rendering identically, and "no vantage has reported on
// this name" is exactly the state that silently reads as a pass: the row is
// not red, so the eye files it under working. It gets its own status and its
// own cause keys so that a screen cannot conflate them without choosing to.
const StatusUnknown = "unknown"

// Cause keys. Stable strings — a screen branches on these, and each one has a
// different next action on a different box.
const (
	// CauseOK — every rung the target asked for passed.
	CauseOK = "ok"

	// CauseNoReport — no vantage has reported on this target at all. Not a
	// pass. See StatusUnknown.
	CauseNoReport = "no-report"

	// CauseStale — a vantage reported once and has gone quiet past its own
	// cadence. What is on screen was true then, not now.
	CauseStale = "stale"

	// CauseDNSMissing — the name does not resolve.
	CauseDNSMissing = "dns-missing"

	// CauseDNSWrong — it resolves, to none of the addresses hz expects. The
	// public record is stale or points elsewhere.
	CauseDNSWrong = "dns-wrong"

	// CauseDNSPartial — it resolves to some of them. Propagation in flight,
	// or a half-finished record edit. A warning, not a failure.
	CauseDNSPartial = "dns-partial"

	// CauseEdgeUnreachable — the name resolves to the right address and
	// nothing there accepted the connection. THE ROUTER CASE: the forward or
	// DMZ that is supposed to carry the port to hz is not carrying it. hz
	// cannot fix this and must not pretend it can.
	CauseEdgeUnreachable = "edge-unreachable"

	// CauseTLSBroken — the connection was accepted and the handshake failed.
	CauseTLSBroken = "tls-broken"

	// CauseCertExpiring — the handshake worked and the certificate is close
	// enough to expiry that renewal is broken rather than pending.
	CauseCertExpiring = "cert-expiring"

	// CauseBackendDown — DNS, the forward, TLS and the proxy all worked, and
	// what is behind them did not.
	CauseBackendDown = "backend-down"

	// CauseUnclassified — a rung failed in a way the ladder cannot name.
	// Deliberately its own key rather than being folded into the nearest
	// plausible one: a wrong instruction costs more than no instruction.
	CauseUnclassified = "unclassified"
)

// Device names whose box has to change. A key, not prose, so a screen can
// group by it — an operator fixing the router wants the router rows together.
const (
	DeviceNone    = ""
	DeviceDNS     = "dns"     // the DNS provider, or the dynamic-DNS updater
	DeviceRouter  = "router"  // the edge router. hz has no path to it.
	DeviceHZ      = "hz"      // this box
	DeviceBackend = "backend" // whatever hz proxies to
	DeviceVantage = "vantage" // the probe agent itself
)

// Diagnosis is one target's verdict from one vantage.
type Diagnosis struct {
	Target  string `json:"target"`
	Host    string `json:"host"`
	Vantage string `json:"vantage"`

	// Status is the probe vocabulary plus StatusUnknown.
	Status string `json:"status"`

	// Cause is the branchable key; Device is whose box it is.
	Cause  string `json:"cause"`
	Device string `json:"device"`

	// HZCanFix is false whenever the change has to happen somewhere hz cannot
	// reach. A screen renders an action only when this is true — everything
	// else is an instruction for a person.
	HZCanFix bool `json:"hzCanFix"`

	// Summary is what is wrong, in one line. Fix names the device and the
	// change. Confirm is how the operator knows it worked, because an
	// instruction with no confirmation is a guess.
	Summary string `json:"summary"`
	Fix     string `json:"fix"`
	Confirm string `json:"confirm"`

	// Evidence is the ladder as observed, in order, one line per rung.
	//
	// Never null and never omitted: an empty list is hz saying "no rung was
	// climbed", which is a fact about a target nothing has reported on, and a
	// missing key would make it indistinguishable from a client that failed to
	// read one.
	Evidence []string `json:"evidence"`

	// At is the newest result the verdict rests on. The zero time means there
	// are none — read it with Status, which says StatusUnknown in that case.
	At time.Time `json:"at"`
}

// Facts are the hz-side inputs the ladder needs and the agent must not hold.
type Facts struct {
	// Vantage is whose view this is, for the prose.
	Vantage string

	// PublicIP is hz's current idea of its own public address, and
	// PublicIPStale says whether that idea is old enough to distrust. The two
	// together are what separate "my public IP changed and DNS has not caught
	// up" from "DNS is right and the forward is stale" — without them those
	// are one undifferentiated failure, which is the state this replaces.
	PublicIP      string
	PublicIPStale bool

	// LocalAddress is the address hz currently sees ITSELF at on the LAN —
	// config.LocalInterface, which despite the name holds an IP and not an
	// interface name (config.DetectLocalInterface returns GetInterfaceIP; the
	// NIC name is the separate LastLocalIface). The codebase already treats it
	// as "the address other hosts reach this hz on": DeriveLocalDNSMappings
	// rewrites a service's `localhost` record to it, and ValidateForwards
	// refuses a port-forward backend equal to it as "points at the gateway
	// itself".
	//
	// It is what turns the router instruction from a category into an address.
	// Carried here rather than read from config for the same reason PublicIP
	// is: Diagnose stays pure, and a test can pin it.
	//
	// EMPTY IS ALLOWED AND MEANS "do not name one". DetectLocalInterface falls
	// back through eth0 to the VPN range to a hardcoded last resort, so the
	// value is not guaranteed to be a LAN address; the prose says hz is
	// reporting where it sees itself rather than asserting where it is.
	LocalAddress string

	// Now anchors every age. Passed rather than read so the classifier stays
	// pure and the table test can pin a clock.
	Now time.Time

	// ReportEvery is the vantage's cadence; StaleAfter is how far past it a
	// reading may drift before the honest answer is StatusUnknown.
	ReportEvery time.Duration
	StaleAfter  time.Duration
}

// Diagnose runs the ladder over one target's results.
//
// Pure: no clock, no network, no config lookup. Everything it needs arrives
// in Facts, the same discipline internal/projection keeps, and for the same
// reason — the verdict for a target hz is not currently probing has to be
// computable, and it has to be testable with no agent anywhere.
func Diagnose(t probe.Target, results []probe.Result, f Facts) Diagnosis {
	d := Diagnosis{
		Target:   t.Name,
		Host:     t.Host,
		Vantage:  f.Vantage,
		Evidence: []string{},
	}

	kinds := wantedKinds(t)
	byKind := newestByKind(results, kinds)

	// Rung 0: is there a reading at all? A target with no result is not
	// passing, and this is the only place that can say so — the check rows
	// for a never-reported target do not exist, which renders as nothing.
	if len(byKind) == 0 {
		return noReport(d, t, f)
	}

	d.At = newestAt(byKind)
	// Evidence is attached before any verdict, including the stale one: a
	// reading hz no longer trusts is still the last thing anybody saw, and
	// hiding it would leave the operator with a bare "unknown".
	d.Evidence = evidence(byKind, kinds)

	if f.StaleAfter > 0 && f.Now.Sub(d.At) > f.StaleAfter {
		return stale(d, t, f)
	}

	dns, hasDNS := byKind[probe.KindDNS]
	tcp, hasTCP := byKind[probe.KindTCP]
	https, hasHTTPS := byKind[probe.KindHTTPS]

	// Rung 1: DNS. A failure here stops the ladder — everything below it
	// dialled whatever the name happened to resolve to, so no verdict about
	// the edge means anything yet.
	if hasDNS && dns.Status == probe.StatusFailed {
		if looksLikeLookupFailure(dns) {
			return dnsMissing(d, t, f)
		}
		return dnsWrong(d, t, f, dns)
	}

	// A DNS *warning* does not stop the ladder. A partially-propagated record
	// plus a refused connection is still the router's problem, and reporting
	// the propagation would send the operator to the wrong box. Held here and
	// answered only if nothing below it failed.
	dnsPartial := hasDNS && dns.Status == probe.StatusWarning

	// Rung 2+: the transport. TCP, when the target asked for it, is the
	// cleaner reading — it fails only on reachability. HTTPS collapses
	// TCP, TLS and HTTP into one result, so a TCP result that succeeded is
	// what tells an HTTPS timeout apart from an unreachable edge.
	tcpReachable := hasTCP && tcp.Status == probe.StatusOK

	if hasTCP && tcp.Status == probe.StatusFailed {
		return transportVerdict(d, t, f, tcp, false)
	}
	if hasHTTPS && https.Status == probe.StatusFailed {
		if code := httpCode(https); code >= 500 {
			return backendDown(d, t, f, code)
		}
		return transportVerdict(d, t, f, https, tcpReachable)
	}
	if hasHTTPS && https.Status == probe.StatusWarning {
		return certExpiring(d, t, f, https)
	}

	if dnsPartial {
		return dnsPartialVerdict(d, t, f, dns)
	}

	d.Status = probe.StatusOK
	d.Cause = CauseOK
	d.Device = DeviceNone
	d.Summary = fmt.Sprintf("%s answers from %s: every check %s asked for passed.",
		t.Host, vantageName(f), t.Host)
	d.Fix = "Nothing to do."
	d.Confirm = ""
	return d
}

// wantedKinds is the probe set for a target, mirroring probe.Run's default so
// a target that names no kinds is judged on the rungs it actually ran.
func wantedKinds(t probe.Target) []string {
	if len(t.Kinds) == 0 {
		return []string{probe.KindDNS, probe.KindHTTPS}
	}
	return t.Kinds
}

// newestByKind keeps the most recent result per kind, ignoring kinds the
// target does not ask for — a leftover row from a target that used to be
// probed differently must not decide a verdict.
func newestByKind(results []probe.Result, kinds []string) map[string]probe.Result {
	want := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		want[k] = true
	}
	out := make(map[string]probe.Result, len(kinds))
	for _, r := range results {
		if !want[r.Kind] {
			continue
		}
		if prev, ok := out[r.Kind]; ok && !r.At.After(prev.At) {
			continue
		}
		out[r.Kind] = r
	}
	return out
}

func newestAt(byKind map[string]probe.Result) time.Time {
	var newest time.Time
	for _, r := range byKind {
		if r.At.After(newest) {
			newest = r.At
		}
	}
	return newest
}

// evidence renders the ladder in the order it was climbed.
func evidence(byKind map[string]probe.Result, kinds []string) []string {
	out := make([]string, 0, len(byKind))
	for _, k := range kinds {
		r, ok := byKind[k]
		if !ok {
			continue
		}
		line := k + ": " + r.Status
		switch {
		case r.Error != "":
			line += " — " + r.Error
		case r.Detail != "":
			line += " — " + r.Detail
		}
		out = append(out, line)
	}
	return out
}

// Transport failure kinds, read off the error text.
//
// The text is what every agent version already sends: probeHTTPS and probeTCP
// record err.Error() from net/http and net, and nothing structured crosses
// the wire. Classifying here rather than adding a field to probe.Result keeps
// an agent that was installed six months ago diagnosable — a new field would
// be empty from every existing agent and the ladder would silently fall to
// CauseUnclassified for exactly the fleet that needs it most.
type transportKind int

const (
	transportUnknownKind transportKind = iota
	transportDNS
	transportRefused
	transportTimeout
	transportUnreachable
	transportReset
	transportTLS
)

// classifyTransport names what stopped a connection.
//
// Order is load-bearing: a DNS failure inside an HTTP error mentions "lookup",
// a TLS error mentions "tls:" or "x509:", and a refusal mentions neither, so
// the specific markers are tested before the generic ones.
func classifyTransport(errText string) transportKind {
	e := strings.ToLower(errText)
	switch {
	case e == "":
		return transportUnknownKind
	case strings.Contains(e, "no such host"), strings.Contains(e, "server misbehaving"),
		strings.Contains(e, "dns"):
		return transportDNS
	case strings.Contains(e, "connection refused"):
		return transportRefused
	case strings.Contains(e, "network is unreachable"),
		strings.Contains(e, "no route to host"),
		strings.Contains(e, "host is unreachable"):
		return transportUnreachable
	case strings.Contains(e, "tls:"), strings.Contains(e, "x509:"),
		strings.Contains(e, "handshake"), strings.Contains(e, "certificate"):
		return transportTLS
	case strings.Contains(e, "i/o timeout"), strings.Contains(e, "deadline exceeded"),
		strings.Contains(e, "timeout"):
		return transportTimeout
	case strings.Contains(e, "connection reset"), strings.Contains(e, "eof"),
		strings.Contains(e, "broken pipe"):
		return transportReset
	default:
		return transportUnknownKind
	}
}

// looksLikeLookupFailure separates "the name does not exist" from "it
// resolves to the wrong thing". probeDNS records the answer in Detail and
// only reaches a verdict when it got one, so an empty Detail means the
// resolver never answered.
func looksLikeLookupFailure(r probe.Result) bool {
	return strings.TrimSpace(r.Detail) == ""
}

// httpCode reads the status code probeHTTPS put at the front of Detail
// ("503, cert 41d left"). Returns 0 when there is none.
func httpCode(r probe.Result) int {
	head := r.Detail
	if i := strings.IndexByte(head, ','); i >= 0 {
		head = head[:i]
	}
	code, err := strconv.Atoi(strings.TrimSpace(head))
	if err != nil {
		return 0
	}
	return code
}

// port is the port the target is actually probed on.
func port(t probe.Target) int {
	if t.Port == 0 {
		return 443
	}
	return t.Port
}

func vantageName(f Facts) string {
	if f.Vantage == "" {
		return "the outside vantage"
	}
	return f.Vantage
}

// every renders a cadence for prose, falling back to something honest when
// the caller does not know it.
func every(f Facts) string {
	if f.ReportEvery <= 0 {
		return "on its next round"
	}
	return "every " + f.ReportEvery.Round(time.Second).String()
}

func humanAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// curlLine is the check an operator runs from outside. It is in the
// instruction because "wait and see if the row goes green" is not a
// confirmation an operator standing at a router can act on.
func curlLine(t probe.Target) string {
	scheme := "https://" + t.Host
	if p := port(t); p != 443 {
		scheme += ":" + strconv.Itoa(p)
	}
	return "curl -sS -o /dev/null -w '%{http_code}\\n' " + scheme + "/"
}

// --- verdicts -------------------------------------------------------------

func noReport(d Diagnosis, t probe.Target, f Facts) Diagnosis {
	d.Status = StatusUnknown
	d.Cause = CauseNoReport
	d.Device = DeviceVantage
	d.HZCanFix = false
	d.Summary = fmt.Sprintf(
		"No outside vantage has reported on %s. hz has no reading for this name — that is not the same as it working.",
		t.Host)
	d.Fix = fmt.Sprintf(
		"Nothing is wrong with %s that hz knows of, and nothing is confirmed about it either. "+
			"If %s was installed just now, its first report is due %s. If it has been longer, "+
			"the agent is not running or cannot reach hz: the Outside vantages panel says whether "+
			"hz has heard from it at all.",
		t.Host, vantageName(f), every(f))
	d.Confirm = fmt.Sprintf(
		"This line is replaced by a real verdict as soon as %s reports. It will not turn green on its own.",
		vantageName(f))
	return d
}

func stale(d Diagnosis, t probe.Target, f Facts) Diagnosis {
	age := humanAge(f.Now.Sub(d.At))
	d.Status = StatusUnknown
	d.Cause = CauseStale
	d.Device = DeviceVantage
	d.HZCanFix = false
	d.Summary = fmt.Sprintf(
		"The last reading for %s from %s is %s old (expected %s). It describes what was true then, not now.",
		t.Host, vantageName(f), age, every(f))
	d.Fix = fmt.Sprintf(
		"The vantage stopped reporting, so treat the last verdict as history. Check %s in the "+
			"Outside vantages panel: its agent row says whether hz has heard from it.",
		vantageName(f))
	d.Confirm = "A fresh reading replaces this as soon as the agent reports again."
	return d
}

func dnsMissing(d Diagnosis, t probe.Target, f Facts) Diagnosis {
	d.Status = probe.StatusFailed
	d.Cause = CauseDNSMissing
	d.Device = DeviceDNS
	d.HZCanFix = false
	d.Summary = fmt.Sprintf("%s does not resolve at all from %s.", t.Host, vantageName(f))
	d.Fix = fmt.Sprintf(
		"The public record for %s is missing. Create it wherever that name's DNS is published — "+
			"that is the registrar or DNS provider for the zone, not hz. Until the name resolves, "+
			"nothing outside can reach the service by name whatever hz is serving.",
		t.Host)
	d.Confirm = fmt.Sprintf("From outside the network: dig +short %s should print %s.",
		t.Host, expectList(t, f))
	return d
}

// dnsWrong is the record pointing somewhere hz did not expect.
//
// The two readings that used to be indistinguishable are separated here: if
// what hz expects IS hz's own current public address, then the record is
// behind hz's address. If hz's idea of its own address is stale or missing,
// hz says so rather than accusing the record — it genuinely cannot tell which
// of the two is out of date, and guessing sends the operator to the wrong box.
func dnsWrong(d Diagnosis, t probe.Target, f Facts, dns probe.Result) Diagnosis {
	got := dns.Detail
	if strings.TrimSpace(got) == "" {
		got = "something else"
	}
	d.Status = probe.StatusFailed
	d.Cause = CauseDNSWrong
	d.Device = DeviceDNS
	d.HZCanFix = false
	d.Summary = fmt.Sprintf("%s resolves to %s, not %s.", t.Host, got, expectList(t, f))

	switch {
	case f.PublicIP == "" || f.PublicIPStale:
		d.Fix = fmt.Sprintf(
			"Either the public record for %s is behind, or hz's idea of its own public address is. "+
				"hz cannot tell you which: it last confirmed its own address too long ago to trust, "+
				"so the address it is comparing against may itself be wrong. Settle hz's public IP "+
				"first (Settings shows when it was last detected), then fix the record at the DNS "+
				"provider if it is still wrong.",
			t.Host)
	case expects(t, f.PublicIP):
		d.Fix = fmt.Sprintf(
			"hz's public address is %s and the record still says %s — the record is behind, not the "+
				"forward. Update the A record for %s at its DNS provider, or fix the dynamic-DNS "+
				"updater that is supposed to do it. hz does not publish this record and cannot "+
				"change it.",
			f.PublicIP, got, t.Host)
	default:
		d.Fix = fmt.Sprintf(
			"%s is pinned to %s in hz's records and resolves to %s instead. Either the DNS record is "+
				"wrong, or the pin in hz is. Fix whichever is out of date — the record at the DNS "+
				"provider, or the service's external DNS entry on the Services page.",
			t.Host, expectList(t, f), got)
	}
	d.Confirm = fmt.Sprintf("From outside the network: dig +short %s should print %s.",
		t.Host, expectList(t, f))
	return d
}

func dnsPartialVerdict(d Diagnosis, t probe.Target, f Facts, dns probe.Result) Diagnosis {
	d.Status = probe.StatusWarning
	d.Cause = CauseDNSPartial
	d.Device = DeviceDNS
	d.HZCanFix = false
	d.Summary = fmt.Sprintf(
		"%s resolves to %s — some of what hz expects (%s), not all of it. Everything below DNS passed.",
		t.Host, dns.Detail, expectList(t, f))
	d.Fix = fmt.Sprintf(
		"Usually a record edit still propagating, which clears itself within a few minutes. If it is "+
			"still short on the next report, an address was dropped: add it back at %s's DNS provider.",
		t.Host)
	d.Confirm = fmt.Sprintf("dig +short %s from outside should print all of %s.",
		t.Host, expectList(t, f))
	return d
}

// transportVerdict is the router rung and its neighbours.
//
// tcpReachable is what stops an HTTPS timeout being blamed on the router when
// a plain TCP probe to the same host connected fine: the packets are getting
// through, so whatever is hanging is above the forward.
func transportVerdict(d Diagnosis, t probe.Target, f Facts, r probe.Result, tcpReachable bool) Diagnosis {
	switch classifyTransport(r.Error) {
	case transportDNS:
		return dnsMissing(d, t, f)

	case transportRefused:
		return edgeUnreachable(d, t, f, "the connection was refused")

	case transportUnreachable:
		return edgeUnreachable(d, t, f, "the network was unreachable")

	case transportTimeout:
		if tcpReachable {
			return acceptedThenSilent(d, t, f, "the connection was accepted and the request timed out")
		}
		return edgeUnreachable(d, t, f, "the connection timed out with no answer")

	case transportReset:
		if tcpReachable {
			return tlsBroken(d, t, f, r)
		}
		return edgeUnreachable(d, t, f, "the connection was reset before anything answered")

	case transportTLS:
		return tlsBroken(d, t, f, r)

	default:
		return unclassified(d, t, f, r)
	}
}

// edgeUnreachable is the case this whole file exists for.
//
// DNS is right, so the packets went to the address hz publishes and nothing
// carried them the rest of the way. The device is the edge router and hz has
// no path to it — no API, no credentials, no route. The instruction IS the
// deliverable, so it says plainly that hz cannot do this, what the setting is
// called on a router, what to point it at, and how to know it worked without
// coming back to this screen.
func edgeUnreachable(d Diagnosis, t probe.Target, f Facts, flavour string) Diagnosis {
	p := port(t)
	d.Status = probe.StatusFailed
	d.Cause = CauseEdgeUnreachable
	d.Device = DeviceRouter
	d.HZCanFix = false
	d.Summary = fmt.Sprintf(
		"%s resolves to %s, which is the address hz expects — and nothing accepted a connection on "+
			"port %d: %s.",
		t.Host, expectList(t, f), p, flavour)
	d.Fix = fmt.Sprintf(
		"hz cannot fix this, and there is no button here that would. DNS is right, so the name is "+
			"not the problem: whatever forwards port %d from %s is not sending it to hz. That setting "+
			"is on the edge router — its DMZ host, or its port-%d forward — which hz can neither read "+
			"nor change. Open the router's admin page and point the DMZ host (or the port-%d forward) "+
			"%s",
		p, expectList(t, f), p, p, pointItAt(f))
	d.Confirm = fmt.Sprintf(
		"From a host OUTSIDE the network, run: %s — any HTTP code at all, even 404 or 502, means the "+
			"forward is carrying again. A connection error means it still is not. This row also clears "+
			"on its own when %s next reports (%s).",
		curlLine(t), vantageName(f), every(f))
	return d
}

// pointItAt finishes the router instruction: an actual address when hz knows
// one, and the category when it does not.
//
// Naming the address is the difference between an instruction somebody can
// follow at a router admin page and one they have to go and research. Naming
// an EMPTY address would be worse than the category — "point the DMZ host at
// ." is an instruction to type nothing — so the fallback is kept and tested
// rather than left to chance.
//
// The prose reports rather than promises. hz is saying where it currently sees
// itself, which after a move is already the new address (that is what makes it
// the useful one); it is not asserting that the browser showing this sentence
// is looking at a live hz.
func pointItAt(f Facts) string {
	addr := usableLocalAddress(f.LocalAddress)
	if addr == "" {
		return "at the LAN address of the machine running hz. If that machine's address changed " +
			"recently — a move to wireless gives it a new one — the forward is still aimed at where " +
			"it used to be."
	}
	return fmt.Sprintf(
		"at %s — where hz sees itself on the LAN right now, so after a move that is already the new "+
			"address. hz is reporting, not promising: if this page has gone stale because hz is "+
			"unreachable, reload it before typing the address in.",
		addr)
}

// usableLocalAddress is hz's LAN address when it is one worth putting in an
// instruction, and empty otherwise.
//
// config.DetectLocalInterface falls back through the default route to eth0 to
// the VPN range to a hardcoded last resort, and the field can also be edited
// by hand, so the value is not guaranteed to be anything in particular. A
// loopback or unspecified address reaching a router's DMZ field would be an
// actively wrong instruction — worse than the category it replaced — so those
// fall back alongside the unparseable ones.
func usableLocalAddress(s string) string {
	s = strings.TrimSpace(s)
	ip := net.ParseIP(s)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		return ""
	}
	return s
}

// acceptedThenSilent is a connection that got through and then went nowhere.
// Not the router — the forward is carrying — and not TLS either, since
// nothing rejected the handshake.
func acceptedThenSilent(d Diagnosis, t probe.Target, f Facts, flavour string) Diagnosis {
	p := port(t)
	d.Status = probe.StatusFailed
	d.Cause = CauseBackendDown
	d.Device = DeviceHZ
	d.HZCanFix = true
	d.Summary = fmt.Sprintf(
		"A plain TCP connection to %s:%d succeeded, so the forward is carrying — but %s.",
		t.Host, p, flavour)
	d.Fix = fmt.Sprintf(
		"This is hz's side, not the router's. Something is listening on port %d and not completing "+
			"requests: check HAProxy on this box (the System health tab) and the backend behind %s "+
			"on the Services page.",
		p, t.Host)
	d.Confirm = fmt.Sprintf("From outside: %s should answer with a code instead of hanging.", curlLine(t))
	return d
}

func tlsBroken(d Diagnosis, t probe.Target, f Facts, r probe.Result) Diagnosis {
	d.Status = probe.StatusFailed
	d.Cause = CauseTLSBroken
	d.Device = DeviceHZ
	d.HZCanFix = true
	reason := r.Error
	if strings.TrimSpace(reason) == "" {
		reason = "the handshake did not complete"
	}
	d.Summary = fmt.Sprintf(
		"The connection reached %s:%d and TLS failed: %s.", t.Host, port(t), reason)
	d.Fix = fmt.Sprintf(
		"DNS and the forward both worked, so this one is hz's. Check the certificate hz serves for "+
			"%s — the Domains page lists which names have one — and that HAProxy loaded the bundle "+
			"after the last renewal.",
		t.Host)
	d.Confirm = fmt.Sprintf(
		"From outside: openssl s_client -connect %s:%d -servername %s </dev/null should print a "+
			"certificate chain for %s.",
		t.Host, port(t), t.Host, t.Host)
	return d
}

func certExpiring(d Diagnosis, t probe.Target, f Facts, r probe.Result) Diagnosis {
	d.Status = probe.StatusWarning
	d.Cause = CauseCertExpiring
	d.Device = DeviceHZ
	d.HZCanFix = true
	d.Summary = fmt.Sprintf(
		"%s answers and the certificate it serves is close to expiry (%s).", t.Host, r.Detail)
	d.Fix = fmt.Sprintf(
		"It still works today and it will stop. Renewal at this point is broken rather than pending: "+
			"check hz's certificate renewal for %s on the Domains page.",
		t.Host)
	d.Confirm = "The next report shows a longer remaining life once renewal succeeds."
	return d
}

func backendDown(d Diagnosis, t probe.Target, f Facts, code int) Diagnosis {
	d.Status = probe.StatusFailed
	d.Cause = CauseBackendDown
	d.Device = DeviceBackend
	d.HZCanFix = true
	d.Summary = fmt.Sprintf(
		"DNS, the forward and TLS all worked for %s, and hz answered HTTP %d.", t.Host, code)
	switch code {
	case 502, 503:
		d.Fix = fmt.Sprintf(
			"The edge is fine — this is the service behind it. %d from HAProxy means it has no healthy "+
				"backend for %s: the application is down, or its health check is failing. Check it on "+
				"the Services page.",
			code, t.Host)
	default:
		d.Fix = fmt.Sprintf(
			"The edge is fine — the application itself returned %d. Check the service behind %s; hz "+
				"proxied the request successfully and the answer came from the backend.",
			code, t.Host)
	}
	d.Confirm = fmt.Sprintf("From outside: %s should stop printing %d.", curlLine(t), code)
	return d
}

func unclassified(d Diagnosis, t probe.Target, f Facts, r probe.Result) Diagnosis {
	d.Status = probe.StatusFailed
	d.Cause = CauseUnclassified
	d.Device = DeviceNone
	d.HZCanFix = false
	d.Summary = fmt.Sprintf("The %s probe of %s from %s failed: %s",
		r.Kind, t.Host, vantageName(f), r.Error)
	d.Fix = "hz cannot name which rung of the ladder this is, so it is not sending you to a box it " +
		"has not confirmed. The evidence below is the whole of what the vantage saw."
	d.Confirm = ""
	return d
}

// expectList renders what the name is supposed to answer. Falls back to hz's
// own public address when the target carries no expectation, and says so
// plainly when there is nothing to fall back to.
func expectList(t probe.Target, f Facts) string {
	if len(t.ExpectIPs) > 0 {
		ips := append([]string(nil), t.ExpectIPs...)
		sort.Strings(ips)
		return strings.Join(ips, ", ")
	}
	if f.PublicIP != "" && !f.PublicIPStale {
		return f.PublicIP
	}
	return "hz's public address (which hz cannot currently confirm)"
}

// expects reports whether ip is one of the addresses the target expects.
func expects(t probe.Target, ip string) bool {
	for _, want := range t.ExpectIPs {
		if want == ip {
			return true
		}
	}
	return false
}
