package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/probe"
)

// Outside-in vantage points: the hz-probe agents hz polls.
//
// hz opens every connection to an agent, so these entries hold the agent's
// address and a token hz presents. The token is write-only across this API —
// it goes in, it never comes back out.

// testPollTimeout bounds a trial poll. Short: this runs while somebody waits
// on a dialog, and an agent that needs longer than this is not usable anyway.
const testPollTimeout = 10 * time.Second

// GET /api/v1/checks/remotes
func (s *Server) handleAPIRemotes(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	states := make(map[string]bool, 8)
	byName := map[string]int{}
	live := s.monitor.RemoteStates()
	for i, st := range live {
		byName[st.Name] = i
		states[st.Name] = true
	}

	// Count the check rows each vantage contributes, so the panel can say
	// "4 targets, 8 rows" rather than making the operator scan the table.
	rows := map[string]int{}
	for _, c := range s.monitor.GetStatuses() {
		if c.Vantage != "" {
			rows[c.Vantage]++
		}
	}

	cfg := s.cfg()
	out := make([]apitypes.RemoteProbeResp, 0, len(cfg.RemoteProbes))
	for _, rp := range cfg.RemoteProbes {
		item := apitypes.RemoteProbeResp{
			Name:       rp.Name,
			URL:        rp.URL,
			Enabled:    rp.Enabled,
			Poll:       rp.Poll,
			Probe:      rp.Probe,
			Timeout:    rp.Timeout,
			Resolvers:  rp.Resolvers,
			PinSHA256:  rp.PinSHA256,
			HasToken:   rp.Token != "",
			CheckCount: rows[rp.Name],
		}
		if i, ok := byName[rp.Name]; ok {
			st := live[i]
			item.Reachable = st.Reachable
			item.Polled = !st.LastPoll.IsZero()
			item.LastPoll = st.LastPoll
			item.LastGood = st.LastGood
			item.LastError = st.LastError
			item.AgentVantage = st.AgentVantage
			item.AgentVersion = st.AgentVersion
			item.TargetsVersion = st.TargetsVersion
			item.TargetCount = st.TargetCount
		}
		out = append(out, item)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// decodeRemote reads and validates a request body. The second return is a
// message for the operator, empty when the body is good — these are HTTP
// responses a person reads, not errors any Go caller inspects.
func decodeRemote(r *http.Request) (apitypes.RemoteProbeRequest, string) {
	var req apitypes.RemoteProbeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return req, "Invalid JSON"
	}
	req.Name = strings.TrimSpace(req.Name)
	req.URL = strings.TrimSpace(strings.TrimSuffix(req.URL, "/"))
	req.Token = strings.TrimSpace(req.Token)
	req.PinSHA256 = strings.TrimSpace(req.PinSHA256)
	req.OldName = strings.TrimSpace(req.OldName)

	if req.Name == "" {
		return req, "Name required"
	}
	// The name becomes a check-row prefix (ext:<name>:...), so a colon in it
	// would make the row unparseable and collide with another vantage's.
	if strings.ContainsAny(req.Name, ": \t") {
		return req, "Name cannot contain a colon or whitespace"
	}
	if req.URL == "" {
		return req, "URL required"
	}
	u, err := url.Parse(req.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return req, "URL must be http:// or https:// with a host"
	}
	return req, ""
}

// toConfig turns a request into a config entry, carrying the stored token
// forward when the request did not supply one.
func toConfig(req apitypes.RemoteProbeRequest, existingToken string) config.RemoteProbe {
	token := req.Token
	if token == "" {
		token = existingToken
	}
	return config.RemoteProbe{
		Name:      req.Name,
		URL:       req.URL,
		Token:     token,
		Enabled:   req.Enabled,
		Poll:      req.Poll,
		Probe:     req.Probe,
		Timeout:   req.Timeout,
		Resolvers: req.Resolvers,
		PinSHA256: req.PinSHA256,
	}
}

// POST /api/v1/checks/remotes/add
func (s *Server) handleAPIRemoteAdd(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	req, msg := decodeRemote(r)
	if msg != "" {
		writeJSONError(w, http.StatusBadRequest, msg)
		return
	}
	if req.Token == "" {
		writeJSONError(w, http.StatusBadRequest, "Token required")
		return
	}
	for _, rp := range s.cfg().RemoteProbes {
		if rp.Name == req.Name {
			writeJSONError(w, http.StatusBadRequest, "A vantage with this name already exists")
			return
		}
	}

	if err := s.updateConfig(func(cfg *config.Config) {
		cfg.RemoteProbes = append(cfg.RemoteProbes, toConfig(req, ""))
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.monitor.ReloadRemotes(s.cfg())

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// POST /api/v1/checks/remotes/update
func (s *Server) handleAPIRemoteUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	req, msg := decodeRemote(r)
	if msg != "" {
		writeJSONError(w, http.StatusBadRequest, msg)
		return
	}

	target := req.OldName
	if target == "" {
		target = req.Name
	}

	// A rename may not land on another entry's name.
	if req.Name != target {
		for _, rp := range s.cfg().RemoteProbes {
			if rp.Name == req.Name {
				writeJSONError(w, http.StatusBadRequest, "A vantage with this name already exists")
				return
			}
		}
	}

	found := false
	if err := s.updateConfig(func(cfg *config.Config) {
		for i, rp := range cfg.RemoteProbes {
			if rp.Name == target {
				cfg.RemoteProbes[i] = toConfig(req, rp.Token)
				found = true
				break
			}
		}
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeJSONError(w, http.StatusNotFound, "Vantage not found")
		return
	}
	s.monitor.ReloadRemotes(s.cfg())

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// POST /api/v1/checks/remotes/delete
func (s *Server) handleAPIRemoteDelete(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "Name required")
		return
	}

	found := false
	if err := s.updateConfig(func(cfg *config.Config) {
		for i, rp := range cfg.RemoteProbes {
			if rp.Name == name {
				cfg.RemoteProbes = append(cfg.RemoteProbes[:i], cfg.RemoteProbes[i+1:]...)
				found = true
				break
			}
		}
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeJSONError(w, http.StatusNotFound, "Vantage not found")
		return
	}

	// ReloadRemotes stops this vantage's loop and drops its check rows with
	// it. Leaving the rows behind would leave statuses nothing updates, which
	// read as current forever. Local checks and every other vantage keep
	// running, and keep their history.
	s.monitor.ReloadRemotes(s.cfg())

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// POST /api/v1/checks/remotes/token
//
// Mints a token for a vantage that does not exist yet, so the install command
// can carry it. hz generating it is what removes the copy-back step: by the
// time the agent is running, hz already holds the credential it will present.
func (s *Server) handleAPIRemoteToken(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	// No vantage is created here — the token becomes a stored credential only
	// when one is saved, so a dialog opened and abandoned leaves nothing
	// behind but an install grant that expires on its own.
	token := generateToken(48)

	// The same token is what the install command presents to download the
	// agent binary, so record it as redeemable. Without this the download
	// would have to stay anonymous to work at all.
	s.installGrants.issue(token)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(apitypes.RemoteProbeTokenResp{
		Token:       token,
		InstallBase: s.installBaseURL(r),
	})
}

// POST /api/v1/checks/remotes/test
//
// Polls the agent once and reports what came back, without saving anything.
// The point is to fail in the dialog rather than silently in the poll loop:
// a wrong token, a bad pin and an unreachable host all look the same from the
// Checks page ten minutes later.
func (s *Server) handleAPIRemoteTest(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	req, msg := decodeRemote(r)
	if msg != "" {
		writeJSONError(w, http.StatusBadRequest, msg)
		return
	}

	// Testing an existing entry whose token the UI never saw: fall back to
	// the stored one.
	token := req.Token
	if token == "" {
		lookup := req.OldName
		if lookup == "" {
			lookup = req.Name
		}
		for _, rp := range s.cfg().RemoteProbes {
			if rp.Name == lookup {
				token = rp.Token
				break
			}
		}
	}
	if token == "" {
		writeJSONError(w, http.StatusBadRequest, "Token required")
		return
	}

	client := &probe.Client{
		URL:       req.URL,
		Token:     token,
		PinSHA256: req.PinSHA256,
		Timeout:   testPollTimeout,
		// With no pin, look at the certificate rather than requiring it to
		// verify, and report it. That is what lets an operator adopt a
		// self-signed agent: they see the fingerprint here and decide. With a
		// pin already set, this is a real check that the pin matches.
		Observe: req.PinSHA256 == "",
	}

	ctx, cancel := context.WithTimeout(r.Context(), testPollTimeout)
	defer cancel()

	start := time.Now()
	// A bare poll, not a Sync: this must not install targets on an agent the
	// operator has not saved yet.
	resp, err := client.Poll(ctx, probe.PollRequest{Limit: 1})
	latency := time.Since(start).Milliseconds()

	out := apitypes.RemoteProbeTestResp{LatencyMS: latency}
	if client.Observe && client.Cert.SHA256 != "" {
		out.CertSHA256 = client.Cert.SHA256
		out.CertTrusted = client.Cert.Trusted
		out.CertSubject = client.Cert.Subject
		if !client.Cert.NotAfter.IsZero() {
			out.CertNotAfter = client.Cert.NotAfter.Format(time.RFC3339)
		}
	}
	if err != nil {
		out.Error = err.Error()
	} else {
		out.OK = true
		out.AgentVantage = resp.Vantage
		out.AgentVersion = resp.Version
		out.TargetsVersion = resp.TargetsVersion
		out.TargetCount = resp.TargetCount
		out.WantTargets = resp.WantTargets
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
