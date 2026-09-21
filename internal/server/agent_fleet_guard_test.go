package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// The guard is plan/ha-and-the-agent.md §6 option B: hz-agent and HA peer-sync
// are mutually exclusive on one machine, and hz refuses to serve desired state
// to a machine in a fleet.
//
// These tests exist because the agent is INERT today. Nothing applies, so
// nothing about the current gateway would notice the guard being wrong — which
// is exactly the condition under which a guard rots. Every test below asserts
// the refusal itself rather than a consequence of it, so they stay true at item
// 12 step 4 when --apply goes into the unit and the refusal starts mattering.

// captureLogs points slog at a buffer for the duration of a test, so an
// operator-facing log line can be asserted as the product it is.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// joinFleet puts a fleet into a running server's live config, the way an
// operator editing config.json and restarting would, or a join would.
func joinFleet(t *testing.T, s *Server, peerID string) {
	t.Helper()
	next := *s.cfg()
	next.PeerID = peerID
	next.Peers = []config.Peer{{ID: "other-site", WGAddr: "10.100.0.2:8080", Primary: true}}
	s.config.Store(&next)
}

// armAnAgent makes hz believe an agent on this machine is running with
// --apply, through the REAL ingest path — a POST with the machine's own agent
// credential, not a hand-written file. hz's only signal that an agent is armed
// is what the machine reported, so a test that wrote the store directly would
// be asserting against a fact hz cannot actually obtain.
func armAnAgent(t *testing.T, s *Server, applying bool) {
	t.Helper()
	secret := enrolledAgent(t, s)
	machine := s.buildAgentDesired().Machine
	w := postReport(t, s, secret, agent.StateReport{
		Machine:    machine,
		Generation: "whatever-hz-last-served",
		Applying:   applying,
	})
	if w.Code != http.StatusNoContent {
		t.Fatalf("report: want 204, got %d: %s", w.Code, w.Body.String())
	}
}

// CHECK 1 — AT BOOT.
//
// A machine that starts up already in a fleet says so once, loudly, naming both
// features and the document. This is the line that stops an operator finding
// out months later that the agent they installed has been refused since the day
// they joined a peer.
func TestABootWithAFleetSaysTheAgentIsDisarmed(t *testing.T) {
	s, _ := agentTestServer(t)
	joinFleet(t, s, "site-b")

	logs := captureLogs(t)
	s.announceAgentGuardAtBoot()

	got := logs.String()
	for _, want := range []string{"level=ERROR", "hz-agent", "peer-sync", "ha-and-the-agent.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("the boot line does not mention %q:\n%s", want, got)
		}
	}
}

// AND IT IS ACTUALLY WIRED INTO BOOT.
//
// The test above calls announceAgentGuardAtBoot by name, so on its own it would
// stay green if the call were dropped out of Run — a boot check nothing boots.
// This reads server.go the way internal/agent/seam_test.go reads its own
// package and asserts the call is there, before startPeerSync: "at boot" means
// before the loops it is about, so an operator reading a journal finds the
// reason above the thing it explains.
func TestTheBootCheckIsWiredIntoRun(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "server.go", nil, 0)
	if err != nil {
		t.Fatalf("parse server.go: %v", err)
	}
	var order []string
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "RunWithTokenCallback" {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				switch sel.Sel.Name {
				case "announceAgentGuardAtBoot", "startPeerSync", "startBanSync":
					order = append(order, sel.Sel.Name)
				}
			}
			return true
		})
		return false
	})
	if len(order) == 0 || order[0] != "announceAgentGuardAtBoot" {
		t.Fatalf("RunWithTokenCallback does not announce the guard before the peer-sync loops: %v", order)
	}
}

// And a standalone boot says nothing. A guard that logs on every gateway is a
// guard people learn to ignore.
func TestABootWithoutAFleetIsSilent(t *testing.T) {
	s, _ := agentTestServer(t)
	logs := captureLogs(t)
	s.announceAgentGuardAtBoot()
	if got := logs.String(); strings.Contains(got, "hz-agent guard") {
		t.Errorf("a standalone gateway logged a guard line:\n%s", got)
	}
}

// CHECK 3 — THE ENFORCEMENT POINT.
//
// The refusal is what disarms the agent, and it must not depend on the agent
// agreeing to be disarmed: hz is the only source of desired state, so an agent
// of any version, armed or inert, has nothing to apply after a 409.
func TestAFleetIsRefusedTheDesiredState(t *testing.T) {
	s, _ := agentTestServer(t)
	if w := agentGET(t, s, ""); w.Code != http.StatusOK {
		t.Fatalf("standalone: want 200, got %d: %s", w.Code, w.Body.String())
	}

	joinFleet(t, s, "site-b")
	w := agentGET(t, s, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("fleet: want 409, got %d: %s", w.Code, w.Body.String())
	}
	for _, want := range []string{"hz-agent", "peer-sync", "ha-and-the-agent.md"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("the refusal does not mention %q: %s", want, w.Body.String())
		}
	}
	// Not a degraded payload. An empty or partial Desired is a sentence the
	// agent already understands ("hz does not manage this here") and would act
	// on; only refusing the whole read cannot be mistaken for an instruction.
	var d agent.Desired
	if err := json.Unmarshal(w.Body.Bytes(), &d); err == nil && d.Fingerprint() != (&agent.Desired{}).Fingerprint() {
		t.Error("the refusal body decoded as a usable Desired")
	}
}

// REFUSING TO SERVE IS NOT REFUSING TO LISTEN.
//
// The report route is untouched, deliberately. An agent that was running when
// the fleet appeared keeps its carried payload and keeps reporting on its
// heartbeat, so the machine stays on hz's drift screen with a reading and an
// age instead of dropping to "silent" — which is what a dead agent looks like,
// and a disarmed one is not that.
func TestAFleetDoesNotSilenceAMachinesReports(t *testing.T) {
	s, _ := agentTestServer(t)
	secret := enrolledAgent(t, s)
	joinFleet(t, s, "site-b")

	if w := agentGETWith(t, s, secret, ""); w.Code != http.StatusConflict {
		t.Fatalf("the poll should be refused: got %d", w.Code)
	}
	w := postReport(t, s, secret, agent.StateReport{
		Machine:    s.buildAgentDesired().Machine,
		Generation: "the-one-it-still-carries",
	})
	if w.Code != http.StatusNoContent {
		t.Fatalf("report: want 204, got %d: %s", w.Code, w.Body.String())
	}
	if _, ok := s.agentObservations().Get(s.buildAgentDesired().Machine); !ok {
		t.Error("the report was not recorded, so the machine reads as silent")
	}
}

// Recomputed per request, never latched. A latch is one forgotten update site
// away from being wrong; this asserts the two directions a latch would miss —
// a fleet appearing after boot, and a fleet removed without a restart.
func TestTheRefusalTracksTheLiveConfigInBothDirections(t *testing.T) {
	s, _ := agentTestServer(t)
	secret := enrolledAgent(t, s)

	if w := agentGETWith(t, s, secret, ""); w.Code != http.StatusOK {
		t.Fatalf("before: want 200, got %d", w.Code)
	}
	joinFleet(t, s, "site-b")
	if w := agentGETWith(t, s, secret, ""); w.Code != http.StatusConflict {
		t.Fatalf("after joining: want 409, got %d", w.Code)
	}
	standalone := *s.cfg()
	standalone.PeerID, standalone.Peers = "", nil
	s.config.Store(&standalone)
	if w := agentGETWith(t, s, secret, ""); w.Code != http.StatusOK {
		t.Fatalf("after leaving: want 200, got %d — the agent did not re-arm without a restart", w.Code)
	}
}

// The refusal comes AFTER the credential check. Whether this machine is in a
// fleet is not something an unauthenticated caller gets to learn by asking.
func TestAnUnauthenticatedCallerLearnsNothingAboutTheFleet(t *testing.T) {
	s, _ := agentTestServer(t)
	joinFleet(t, s, "site-b")

	r := httptest.NewRequest(http.MethodGet, agent.DesiredPath, nil)
	w := httptest.NewRecorder()
	s.handleAgentDesired(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "peer") {
		t.Errorf("the 401 leaked the fleet state: %s", w.Body.String())
	}
}

// CHECK 2 — THE ONE THAT MATTERS.
//
// The invariant: fleet topology is per-instance and never comes off the wire.
// The server here booted standalone, so check 1 looked at a config with no
// fleet and had nothing to say — the payload arriving afterwards is precisely
// the case a boot-only check cannot see.
func TestAPulledConfigCannotPutThisMachineInAFleet(t *testing.T) {
	s, _ := agentTestServer(t)
	logs := captureLogs(t)
	s.announceAgentGuardAtBoot() // check 1 runs, and sees nothing

	pulled := *s.cfg()
	pulled.PeerID = "site-b"
	pulled.Peers = []config.Peer{{ID: "site-a", WGAddr: "10.100.0.1:8080", Primary: true}}

	err := s.applyNewConfig(&pulled)
	if err == nil {
		t.Fatal("applyNewConfig accepted a config that configures a fleet")
	}
	if !errors.Is(err, errFleetFromTheWire) {
		t.Fatalf("want errFleetFromTheWire, got %v", err)
	}
	// Refused BEFORE the swap: everything after that line in applyNewConfig
	// either saves the config or writes /etc.
	if fleetConfigured(s.cfg()) {
		t.Error("the live config was converted anyway")
	}
	onDisk, loadErr := config.Load(s.configPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if fleetConfigured(onDisk) {
		t.Error("the refused config was saved to disk")
	}
	// And the agent is still served, because nothing about this machine
	// changed.
	if w := agentGET(t, s, ""); w.Code != http.StatusOK {
		t.Fatalf("after the refusal: want 200, got %d", w.Code)
	}
	if !strings.Contains(logs.String(), "refusing a pulled config") {
		t.Errorf("the refusal was not logged:\n%s", logs.String())
	}
}

// Both directions, because both change the guard's answer. A pull that took
// this machine OUT of a fleet would ARM an agent nobody armed, which is the
// same failure with the sign flipped.
func TestAPulledConfigCannotChangeAFleetItAlreadyHas(t *testing.T) {
	member := func() *config.Config {
		return &config.Config{
			PeerID: "site-b",
			Peers:  []config.Peer{{ID: "site-a", WGAddr: "10.100.0.1:8080", Primary: true}},
		}
	}
	cases := map[string]func(*config.Config){
		"peers added": func(c *config.Config) {
			c.Peers = append(c.Peers, config.Peer{ID: "site-c", WGAddr: "10.100.0.3:8080"})
		},
		"a peer's address rewritten": func(c *config.Config) {
			c.Peers = []config.Peer{{ID: "site-a", WGAddr: "10.100.0.9:8080", Primary: true}}
		},
		"the fleet removed":    func(c *config.Config) { c.PeerID, c.Peers = "", nil },
		"this machine renamed": func(c *config.Config) { c.PeerID = "site-a" },
		"promoted to primary":  func(c *config.Config) { c.ConfigPrimary = true },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := newTestServer(t, member())
			pulled := *s.cfg()
			mutate(&pulled)
			if err := s.applyNewConfig(&pulled); !errors.Is(err, errFleetFromTheWire) {
				t.Fatalf("want errFleetFromTheWire, got %v", err)
			}
		})
	}
}

// And the mirror: an ORDINARY pull is untouched. A guard that broke peer-sync
// for the fleets it protects would be swapped out for a comment.
func TestAFleetMembersOrdinaryPullIsNotRefused(t *testing.T) {
	cfg := &config.Config{
		PeerID: "site-b",
		Peers:  []config.Peer{{ID: "site-a", WGAddr: "10.100.0.1:8080", Primary: true}},
	}
	s := newTestServer(t, cfg)

	pulled := *s.cfg()
	pulled.Services = []config.Service{{Name: "something-new"}}
	if err := s.applyNewConfig(&pulled); err != nil {
		t.Fatalf("a fleet member's own pull was refused: %v", err)
	}
	if len(s.cfg().Services) != 1 {
		t.Error("the pull did not take effect")
	}
}

// CHECK 2 THROUGH THE REAL PULL PATH, AND WHY IT IS NOT DEAD CODE.
//
// A real primary, over a real HTTP peer API, through registerPeerAPI so
// peerOnlyMiddleware runs exactly as it does in production. The primary's
// config names peers this machine has never heard of.
//
// Today this converges without check 2 ever firing, because
// mergeRemoteIntoLocal pins PeerID, ConfigPrimary and Peers back from the local
// config. THOSE THREE LINES ARE THE WHOLE REASON THE BOOT CHECK IS SUFFICIENT,
// in a function whose documented failure mode (§9 of the investigation, and
// plan/icebox.md) is that its local-only list is opt-out-by-omission. So this
// test asserts the outcome — the machine's fleet is unchanged after a pull that
// carried a different one — rather than the mechanism, and it stays honest
// whichever of the two is doing the work: delete a pin and check 2 refuses the
// pull; delete check 2 as well and this test goes red on the peer list that
// landed.
func TestAPullCannotChangeThisMachinesFleet(t *testing.T) {
	primaryCfg := &config.Config{
		PeerID:        "site-a",
		ConfigPrimary: true,
		// The primary's own view of the fleet: itself, plus a third site this
		// machine has never been told about.
		Peers: []config.Peer{
			{ID: "site-b", WGAddr: "10.100.0.2:8080"},
			{ID: "site-c", WGAddr: "10.100.0.3:8080"},
		},
		Services: []config.Service{{Name: "grafana", Domains: []string{"grafana.example.com"}}},
	}
	primary := newTestServer(t, primaryCfg)
	primaryAddr := startPeerHTTPServer(t, primary)

	memberCfg := &config.Config{
		PeerID: "site-b",
		Peers:  []config.Peer{{ID: "site-a", WGAddr: primaryAddr, Primary: true}},
	}
	member := newTestServer(t, memberCfg)
	logs := captureLogs(t)
	member.announceAgentGuardAtBoot() // check 1 runs, against ONE peer

	member.pullConfigOnce()

	// The shared state converged — this is a working pull, not a broken one.
	if len(member.cfg().Services) != 1 || member.cfg().Services[0].Name != "grafana" {
		t.Fatalf("the pull did not converge: %+v", member.cfg().Services)
	}
	// And the fleet did not.
	if member.cfg().PeerID != "site-b" {
		t.Errorf("peer_id came off the wire: %q", member.cfg().PeerID)
	}
	if member.cfg().ConfigPrimary {
		t.Error("config_primary came off the wire")
	}
	if len(member.cfg().Peers) != 1 || member.cfg().Peers[0].ID != "site-a" {
		t.Errorf("the peer list came off the wire: %+v", member.cfg().Peers)
	}
	// Nothing to refuse, so nothing was refused. A green run here that logged a
	// refusal would mean the pull had stopped working.
	if strings.Contains(logs.String(), "refusing a pulled config") {
		t.Errorf("a correct pull was refused:\n%s", logs.String())
	}
	if status := member.peerSyncSnapshot(); status.LastError != "" {
		t.Errorf("the pull recorded an error: %s", status.LastError)
	}
}

// THE REVERSE DIRECTION.
//
// If the agent is armed and somebody configures a fleet, that fails at the
// point of configuration. Not 30 seconds later in a journal: an operator whose
// mental model is "I added a peer" and whose machine's behaviour is "I stopped
// converging" has no event linking the two.
//
// A SERVER PER SUBTEST, on purpose: each one is a separate way in, and a shared
// server would let the first refusal that failed configure the fleet and make
// the next two pass for the wrong reason.
func TestConfiguringAFleetIsRefusedWhileAnAgentIsApplying(t *testing.T) {
	armed := func(t *testing.T) *Server {
		t.Helper()
		s, _ := agentTestServer(t)
		armAnAgent(t, s, true)
		return s
	}

	t.Run("the funnel every config mutation goes through", func(t *testing.T) {
		s := armed(t)
		err := s.updateConfig(func(cfg *config.Config) {
			cfg.PeerID = "site-a"
			cfg.Peers = []config.Peer{{ID: "site-b", WGAddr: "10.100.0.2:8080"}}
		})
		if !errors.Is(err, errAgentArmed) {
			t.Fatalf("want errAgentArmed, got %v", err)
		}
		if fleetConfigured(s.cfg()) {
			t.Error("the fleet was configured anyway")
		}
		for _, want := range []string{"hz-agent", "--apply", "ha-and-the-agent.md"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not mention %q: %v", want, err)
			}
		}
	})

	t.Run("the first step of the join flow, not the last", func(t *testing.T) {
		s := armed(t)
		body := strings.NewReader(`{"peerId":"site-b","topology":"same-subnet"}`)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/ha/create-join-token", body)
		adminRequest(t, s, r)
		w := httptest.NewRecorder()
		s.handleAPIHACreateJoinToken(w, r)
		if w.Code != http.StatusConflict {
			t.Fatalf("want 409, got %d: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "hz-agent") {
			t.Errorf("the refusal does not name the agent: %s", w.Body.String())
		}
	})

	t.Run("and the callback, as a refusal rather than a 500", func(t *testing.T) {
		s := armed(t)
		s.joinTokens = newJoinTokenStore()
		s.joinTokens.put(&joinToken{
			Token: "tok", PeerID: "site-b", PrimaryPeerID: "site-a", CreatedAt: time.Now(),
		})
		body := strings.NewReader(`{"peer_id":"site-b","wg_addr":"10.100.0.2:8080"}`)
		r := httptest.NewRequest(http.MethodPost, "/admin/ha/join-complete?token=tok", body)
		w := httptest.NewRecorder()
		s.handleHAJoinComplete(w, r)
		if w.Code != http.StatusConflict {
			t.Fatalf("want 409, got %d: %s", w.Code, w.Body.String())
		}
	})
}

// An ENROLLED agent is not an armed one. Every gateway with the agent installed
// today reports Applying=false, and refusing HA on all of them would be a guard
// for a conflict that does not exist.
func TestAnInertAgentDoesNotBlockAFleet(t *testing.T) {
	s, _ := agentTestServer(t)
	armAnAgent(t, s, false)

	if err := s.updateConfig(func(cfg *config.Config) {
		cfg.PeerID = "site-a"
		cfg.Peers = []config.Peer{{ID: "site-b", WGAddr: "10.100.0.2:8080"}}
	}); err != nil {
		t.Fatalf("an inert agent blocked a fleet: %v", err)
	}
	if !fleetConfigured(s.cfg()) {
		t.Error("the fleet was not configured")
	}
}

// Nor does an armed agent block a machine that is ALREADY in a fleet. Its agent
// is already refused by checks 1 and 3, and stranding the operator with no way
// to add a third peer and nothing to fix would be the guard doing harm.
func TestAThirdPeerIsNotRefusedOnAMachineAlreadyInAFleet(t *testing.T) {
	s, _ := agentTestServer(t)
	armAnAgent(t, s, true)
	joinFleet(t, s, "site-b")

	if err := s.updateConfig(func(cfg *config.Config) {
		cfg.Peers = append(cfg.Peers, config.Peer{ID: "site-c", WGAddr: "10.100.0.3:8080"})
	}); err != nil {
		t.Fatalf("adding a peer to an existing fleet was refused: %v", err)
	}
	if len(s.cfg().Peers) != 2 {
		t.Errorf("the peer was not added: %+v", s.cfg().Peers)
	}
}

// The predicate itself, in one place, because both directions and all three
// check sites read it and an over-narrow version would leave a live bypass
// armed.
func TestWhatCountsAsAFleet(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
		want bool
	}{
		{"standalone", config.Config{}, false},
		{"a peer id alone", config.Config{PeerID: "site-a"}, true},
		{"a peer list alone", config.Config{Peers: []config.Peer{{ID: "site-b"}}}, true},
		{"both", config.Config{PeerID: "site-a", Peers: []config.Peer{{ID: "site-b"}}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fleetConfigured(&c.cfg); got != c.want {
				t.Errorf("fleetConfigured = %v, want %v", got, c.want)
			}
			if got := agentFleetGuard(&c.cfg) != ""; got != c.want {
				t.Errorf("agentFleetGuard disarms = %v, want %v", got, c.want)
			}
		})
	}
}
