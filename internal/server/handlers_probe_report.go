package server

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/probe"
)

// Push ingest.
//
// The agent dials hz rather than the reverse, which is what lets a vantage
// live behind NAT with no public address, no inbound rule and no certificate
// of its own. hz accepting an authenticated POST adds no exposure: the edge
// it arrives on is the same public edge these checks exist to verify.
//
// Served on the public-facing vhost for the same reason the installer is —
// the caller is by definition outside, so it belongs on the hostname whose
// threat model assumes that, not on the admin name.

// maxReportBody bounds one report. A batch is capped at 500 results agent
// side; this is the backstop against a client that ignores that.
const maxReportBody = 8 << 20

// defaultPushProbeSeconds is the cadence a self-registered vantage gets.
const defaultPushProbeSeconds = 300

// POST /api/v1/probe/report
func (s *Server) handleProbeReport(w http.ResponseWriter, r *http.Request) {
	if !s.requirePublicVhost(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	token := installToken(r)
	if token == "" {
		writeJSONError(w, http.StatusUnauthorized, "missing token")
		return
	}

	var req probe.PushRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReportBody))
	if err := dec.Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Vantage = strings.TrimSpace(req.Vantage)

	// Match the token to a configured vantage. The name in the body is a
	// label, not an identity — the token is what authenticates, so a
	// misnamed agent cannot report as another vantage.
	rp, found := s.probeByToken(token)
	if !found {
		// First contact. An agent installed from a command hz issued arrives
		// holding an install grant, so convert it: the grant becomes a
		// registered vantage and the operator pastes nothing back.
		if !s.installGrants.valid(token) {
			// Grants live in memory and last an hour, so this is usually an
			// agent whose grant expired — or was wiped by an hz restart —
			// before it ever reported. Say what to do, because the agent
			// will otherwise retry this forever.
			writeJSONError(w, http.StatusUnauthorized,
				"unknown token: hz does not recognise this agent. Re-run the install "+
					"command with a fresh token from hz and HZ_PROBE_REPLACE_TOKEN=1.")
			return
		}
		var err error
		rp, err = s.registerPushVantage(req, token)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.Info("probe: registered a vantage from its first report",
			"vantage", rp.Name, "agent_version", req.Version, "results", len(req.Results))
	}

	if !rp.IsPush() {
		writeJSONError(w, http.StatusConflict,
			"vantage "+rp.Name+" is configured for pull; hz dials it rather than accepting reports")
		return
	}

	accepted := s.monitor.AcceptPushedResults(rp, req.Vantage, req.Version, req.Results)

	// Same version handshake as pull, from the other side: hz sends the set
	// only when the agent is holding a different one.
	want := s.monitor.TargetSetFor(rp)
	resp := probe.PushResponse{
		Accepted:       accepted,
		TargetsVersion: want.Version,
		Interval:       rp.Probe,
	}
	if req.TargetsVersion != want.Version {
		set := want
		resp.Targets = &set
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// probeByToken finds the configured vantage a token belongs to, comparing in
// constant time so a wrong token costs the same as a right one.
func (s *Server) probeByToken(token string) (config.RemoteProbe, bool) {
	for _, rp := range s.cfg().RemoteProbes {
		if rp.Token != "" && subtle.ConstantTimeCompare([]byte(rp.Token), []byte(token)) == 1 {
			return rp, true
		}
	}
	return config.RemoteProbe{}, false
}

// registerPushVantage turns an install grant into a stored vantage.
//
// The agent names itself, because in push mode hz has nothing else to go on
// — there is no URL an operator typed. A name that collides with an existing
// vantage is refused rather than merged: two agents reporting into one set of
// check rows would interleave into nonsense.
func (s *Server) registerPushVantage(req probe.PushRequest, token string) (config.RemoteProbe, error) {
	name := strings.TrimSpace(req.Vantage)
	if name == "" {
		return config.RemoteProbe{}, errNamelessVantage
	}
	if strings.ContainsAny(name, ": \t") {
		return config.RemoteProbe{}, errBadVantageName
	}
	for _, existing := range s.cfg().RemoteProbes {
		if strings.EqualFold(existing.Name, name) {
			return config.RemoteProbe{}, errVantageExists
		}
	}

	rp := config.RemoteProbe{
		Name:    name,
		Mode:    config.ProbeModePush,
		Token:   token,
		Enabled: true,
		// Five minutes rather than one. A vantage is usually a free-tier VM
		// with a metered egress allowance, and at 60s a handful of domains
		// gets close to it; nothing outside-in changes fast enough to need
		// the finer cadence.
		Probe: defaultPushProbeSeconds,
		Poll:  defaultPushProbeSeconds,
	}
	if err := s.updateConfig(func(cfg *config.Config) {
		cfg.RemoteProbes = append(cfg.RemoteProbes, rp)
	}); err != nil {
		return config.RemoteProbe{}, err
	}
	// Only the pull loops care about a config change; a pushing vantage
	// needs no goroutine, so this is just to keep the two in step.
	s.monitor.ReloadRemotes(s.cfg())
	return rp, nil
}

// Registration refusals, as values so the tests can name them.
var (
	errNamelessVantage = &probeRegisterError{"the agent did not send a vantage name"}
	errBadVantageName  = &probeRegisterError{"vantage name cannot contain a colon or whitespace"}
	errVantageExists   = &probeRegisterError{
		"a vantage with this name already exists, and this agent is not holding its token. " +
			"Either the agent's token file was replaced — delete the vantage in hz and let it " +
			"register again — or this is a second agent, which needs a different HZ_PROBE_NAME."}
)

type probeRegisterError struct{ msg string }

func (e *probeRegisterError) Error() string { return e.msg }
